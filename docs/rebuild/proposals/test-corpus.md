# Test corpus + scenario harness — design proposal

**Status:** design-first draft. No code touched. Do not implement
anything from this doc until it's reviewed + signed off.

**Purpose:** replace the "we test methods in isolation and hope
the interactions work in prod" gap with a scenario-based end-to-end
harness. Feed the system YAML-defined match scenarios (with realistic
API responses cycle-by-cycle, including fault injection), execute
the real workflow/activity code against real Postgres + testcontainer
NATS + a mocked api-sports.io, assert the final DB state matches
what the scenario predicts.

Directly addresses the Python-era pain the user's currently
experiencing in prod ("goal stuck in debounce, video not found for
5 min"): scenarios that reproduce that class of bug would have
caught it before it shipped.

Cross-refs:
- Plan §12 Tier 3 (synthetic e2e YAML scenarios) — this proposal
  operationalizes what the plan gestured at
- Working discipline — [`../../AGENTS.md`](../../../AGENTS.md#working-discipline-mandatory-since-2026-07-07-retro)

## Design principles (non-negotiable)

1. **Fast.** Full corpus of ~50 scenarios runs in <60s. A single
   90-minute match with 180 monitor cycles runs in <2s. Achieved via
   accelerated time (see below) — no wall-clock waiting.
2. **Deterministic.** Same scenario input → same DB output, every
   run. No flakiness. No `time.Sleep`, no wall-clock-dependent
   comparisons.
3. **Real code paths.** The harness runs the ACTUAL MonitorWorkflow /
   IngestWorkflow / activities against REAL Postgres (testcontainer).
   Only the api-sports.io HTTP surface is mocked — everything else is
   the same code that runs in prod.
4. **Failure diagnostics that don't suck.** When a scenario fails,
   the harness dumps a cycle-by-cycle trace showing what the workflow
   saw, what it did, and where the state diverged from the
   scenario's expectation.

## Accelerated time — how the "180 monitor cycles in 2 seconds" trick works

The magic is that our production code never blocks on `time.Sleep` in
any logic that matters:

- **Workflows** call `workflow.Now(ctx)`, `workflow.Sleep(ctx, dur)`,
  `workflow.NewTimer(dur)`. Temporal's `testsuite.WorkflowTestSuite`
  has an INSTANT internal clock — `env.SetCurrentWorkflowTime(t)` and
  timers auto-fire without waiting. This is why our existing workflow
  tests complete in milliseconds even though they exercise
  "retry after 4-second backoff" logic.
- **Activities** that need time use INJECTED clocks (like ingest's
  `a.now = func() time.Time` pattern). The harness injects a clock
  that returns the scenario's current-cycle timestamp.
- **HTTP calls** to api-sports.io — mocked via `httptest.Server`.
  Instant, no network. Response is whatever the scenario says for
  this cycle.
- **pg queries** — real, against testcontainer Postgres. Fast because
  data volume per scenario is tiny (1 fixture + a handful of events).

Per-cycle cost:
- Advance clock → 0ms (in-memory)
- Configure mock API for this cycle → <1ms
- Execute MonitorWorkflow ONE cycle → ~5-10ms (workflow logic +
  pg transactions)

180 cycles × 10ms = 1.8s per scenario. Full corpus of 50 scenarios
runs in ~90s including testcontainer setup (which is shared across
the whole test binary — one Postgres container, tests share it via
schema-per-scenario or truncate-between).

**What breaks accelerated time.** Anything in the production code
that calls `time.Now()` directly (bypassing `workflow.Now` or
injected clocks) is a determinism bug. The harness catches this by
comparing "workflow's view of now" to "what actually happened" and
failing loudly. Every such call must be replaced with a clock-injection
pattern before that code is covered by a scenario.

## Scenario YAML format

Location: `test/scenarios/*.yaml`. One file per scenario. Filename
becomes the test name.

Example — `var_overturn_min_30.yaml`:

```yaml
name: VAR-overturned-goal-at-min-30
description: |
  Liverpool scores at min 30, debounce completes (count=3), downstream
  triggers. VAR review — event vanishes from API for 3 consecutive
  cycles. Count decrements 3→2→1→0, hitZero soft-deletes with
  removed_reason=var.

fixture:
  id: 999001
  kickoff: 2026-07-07T15:00:00Z
  home: {id: 40, name: Liverpool}
  away: {id: 42, name: Arsenal}
  league: {id: 39, name: Premier League, season: 2026}
  initial_state: staging  # ingest will have activated it before monitor takes over

# Cycles are the discrete moments MonitorWorkflow fires. Time is
# absolute UTC. Each cycle specifies what api-sports.io returns for
# our /fixtures?ids= call at that moment.
cycles:
  - t: 2026-07-07T14:30:00Z          # 30 min before kickoff — pre-activation
    api_response:
      status: {short: NS, long: Not Started}
      events: []
  - t: 2026-07-07T15:00:00Z          # kickoff
    api_response:
      status: {short: 1H, elapsed: 0}
      events: []
  - t: 2026-07-07T15:30:00Z          # goal scored
    api_response:
      status: {short: 1H, elapsed: 30}
      events:
        - {type: Goal, team: 40, player: {id: 999, name: Salah}, minute: 30, detail: Normal Goal}
  - t: 2026-07-07T15:30:30Z          # cycle 2 of debounce
    api_response:
      status: {short: 1H, elapsed: 30}
      events:
        - {type: Goal, team: 40, player: {id: 999, name: Salah}, minute: 30, detail: Normal Goal}
  - t: 2026-07-07T15:31:00Z          # cycle 3 of debounce — TRIGGERS DOWNSTREAM
    api_response:
      status: {short: 1H, elapsed: 31}
      events:
        - {type: Goal, team: 40, player: {id: 999, name: Salah}, minute: 30, detail: Normal Goal}
  - t: 2026-07-07T15:32:00Z          # VAR happens — event absent
    api_response:
      status: {short: 1H, elapsed: 32}
      events: []
  - t: 2026-07-07T15:32:30Z
    api_response:
      status: {short: 1H, elapsed: 32}
      events: []
  - t: 2026-07-07T15:33:00Z          # 3rd consecutive absence — hitZero, soft-delete
    api_response:
      status: {short: 1H, elapsed: 33}
      events: []

# What must be true after all cycles have run.
expected_final_state:
  fixture:
    state: active
    api_status_short: 1H
  events:
    - natural_key: "40_999_Goal_1"
      debounce_count: 0
      downstream_triggered: true    # was flipped at cycle 3
      removed: true
      removed_reason: var
  discovery_workflows_spawned: 1   # one workflow was triggered at cycle 3
  discovery_workflows_cancelled: 1 # and cancelled at cycle 8 (post-hitZero)
  video_shares:
    removed_count: 0               # no videos existed to soft-delete
```

**Alternate fault-injection example** — `api_500_mid_cycle.yaml`:

```yaml
name: API-500-mid-cycle
description: |
  api-sports.io returns 500 during cycle 4. Verify retry-with-
  backoff + no state corruption on the event that WAS being tracked.

fixture: {id: 999002, kickoff: 2026-07-07T15:00:00Z, ...}

cycles:
  - t: 2026-07-07T15:00:00Z
    api_response: {status: {short: 1H}, events: [...]}
  - t: 2026-07-07T15:00:30Z
    api_response: {status: {short: 1H}, events: [...]}
  - t: 2026-07-07T15:01:00Z
    api_response: {status: {short: 1H}, events: [...]}
  - t: 2026-07-07T15:01:30Z
    api_error: {code: 500, body: "internal server error", after_ms: 100}
  - t: 2026-07-07T15:02:00Z
    api_response: {status: {short: 1H}, events: [...]}

expected_final_state:
  # The 500 shouldn't cause a false absence vote — activity retries
  # against the mock, and if we exhaust retries, the workflow surfaces
  # the error rather than treating "we couldn't reach the API" as
  # "the event is absent."
  events:
    - natural_key: "..."
      debounce_count: 3    # cycles 1-4 all count, cycle-5 same
      downstream_triggered: true
  api_calls_made: 5        # 4 successful + 1 that got retried
```

**Scenarios can also specify SETUP** for state that must exist before
cycles begin — useful for scenarios that start with a fixture already
in active state:

```yaml
setup:
  # Fixture is already tracked (e.g. from a previous ingest run)
  fixture_state: active
  fixture_activated_at: 2026-07-07T14:45:00Z
  fixture_last_polled_at: 2026-07-07T14:59:30Z
```

## Harness architecture

**Location**: `test/harness/harness.go` (or `_test.go` — decide based
on whether we want to build/ship a standalone `ff-scenario` binary or
just a test runner). Tests live at `test/scenarios_test.go` and
iterate over YAML files.

### Components

```
YAML scenario file
    ↓
Loader (yaml.Unmarshal → Scenario struct)
    ↓
┌──────────────────────────────────────────────────┐
│ Harness runtime (one per scenario)               │
│                                                  │
│  ┌───────────────────┐    ┌───────────────────┐  │
│  │ Mock api-sports   │←───│ CycleController   │  │
│  │ (httptest.Server) │    │ (drives cycles,   │  │
│  └───────────────────┘    │  advances clock)  │  │
│           ↑                └─────────┬─────────┘  │
│           │                          │            │
│  ┌────────┴─────────────────────────▼──────────┐  │
│  │ REAL production code:                        │  │
│  │   MonitorWorkflow (via testsuite env)        │  │
│  │   ingest.Activities, monitor.Activities      │  │
│  │   apifootball.Client (URL → mock)            │  │
│  └──────────────────────────────────────────────┘  │
│           ↑                          │             │
│           │                          ↓             │
│  ┌────────┴─────────┐    ┌──────────────────┐     │
│  │ Testcontainer pg │    │ Assertion engine │     │
│  │ (real DB, shared │    │ (reads final     │     │
│  │  across corpus)  │    │  state, compares)│     │
│  └──────────────────┘    └──────────────────┘     │
└──────────────────────────────────────────────────┘
```

### Testcontainer sharing across scenarios

Postgres startup is the slowest part (~2s). Amortize by running ALL
scenarios in the same test binary, sharing one testcontainer.
Between scenarios, either:
- Truncate the app tables (fast, ~5ms per scenario)
- OR: prefix each scenario's fixture IDs so they don't collide, no
  truncate needed (simpler, but harder to reason about state)

Recommendation: **truncate between scenarios.** Explicit reset,
easier to write scenarios that assume a clean slate.

### Clock injection sites

Every place that currently calls `time.Now()` needs to be swappable
in the harness. Known sites (verify during Phase T):
- `activity/ingest.Activities.now()` — already injectable ✓
- New `activity/monitor.Activities.now()` — build this way from
  day one
- Any adapter that timestamps something — check on a
  case-by-case basis

Anything that CANNOT be made deterministic (e.g. `pg.NOW()` in SQL)
either gets replaced with a Go-computed timestamp OR the scenario
tolerates a small window in its assertions (e.g.
`removed_at: {within: 5s of, cycle: 8}`).

## Assertion vocabulary

What the `expected_final_state` block can assert. Start narrow, grow
as scenarios need it:

**Tier 1 (must have):**
- Fixture row: state, api_status_short, activated_at, completed_at,
  last_polled_at (absolute or "within N seconds of cycle X's time")
- Event row: natural_key, debounce_count, downstream_triggered,
  removed, removed_reason, monitor_complete, download_complete
- Row counts: `events.count = 3`, `event_monitor_workflows.count = 15`

**Tier 2 (add as workflow chain grows):**
- Discovery workflow spawn count / cancellation count
- Video shares row states + ranks
- Semantic events emitted (once NATS composer ships)
- Metric counter values (via prometheus registry query)

**Tier 3 (nice to have):**
- Log line assertions ("must have emitted `event_stable` action once")
- Timing bounds ("downstream trigger happened at cycle N ± 1")

Assertion mismatches print the scenario's cycle-by-cycle trace so
you can see where reality diverged from expectation.

## First 10 scenarios — the corpus starter

Ordered by build complexity:

1. `happy_path_single_goal.yaml` — one goal, debounce completes,
   downstream triggers. No fault injection. Baseline.
2. `flicker_recovers.yaml` — goal appears cycle 1, absent cycle 2,
   present cycles 3-4-5. debounce_count: 1→0? no wait, seeded at 1,
   then absent → 0 → soft-delete. Doesn't match; rewrite:
   goal appears 1-2-3 (debounce_count 1→2→3, triggers), absent 4,
   present 5, absent 6. Count: 3→2→3→2. No destroy.
3. `var_overturn_min_30.yaml` — goal triggers, then vanishes for 3
   consecutive cycles, soft-deletes with reason=var.
4. `late_game_goal_min_92.yaml` — goal at min 92 (stoppage time).
   Verify debounce still completes even though match will be FT soon.
5. `simultaneous_two_goals.yaml` — two goals in the same cycle,
   different players. Verify both get their own natural_keys +
   independent debounce state.
6. `postponed_fixture.yaml` — API returns PST mid-match. Monitor
   still polls at active cadence (PST is in Live() bucket).
7. `api_500_mid_cycle.yaml` — mock returns 500. Verify retries
   + no false absence vote.
8. `api_timeout.yaml` — mock hangs past client timeout. Verify
   activity fails cleanly, doesn't corrupt event state.
9. `api_rate_limited.yaml` — mock returns 429 with Retry-After.
   Verify backoff + no vote miscount.
10. `fixture_completes_after_goal.yaml` — goal detected at min 88,
    fixture transitions to FT at min 95. Verify event ends up with
    monitor_complete=true, video_shares exist (once video pipeline
    ships), fixture transitions to completed cleanly.

Later corpus items (post-Phase O):
- Own goal misattribution
- Player refinement (unknown → known)
- Duplicate video URL across events
- Perceptual hash near-collision
- 20+ live fixtures simultaneously (batch chunking)
- Fixture cancellation during monitoring
- API returns fixture we've never seen (mid-day emergency ingest)

## Speed target

- Base setup (testcontainer + schema load): ~2s (once per test run)
- Per scenario: ~1-3s (90-minute match, 180 cycles at 5-15ms each)
- Full corpus (50 scenarios): <90s total

Realistic: on `make test` we probably run corpus AFTER unit tests +
existing testcontainer tests. Full `make test` budget grows to
~3 min. Acceptable — corpus scenarios pay for themselves in
bug-avoidance vs the current "test in prod" model.

## Integration with existing test setup

- New Makefile target: `make test-corpus` — runs just the scenarios.
- `make test` includes corpus by default. `make test-short` still
  excludes anything using testcontainers.
- CI runs corpus on every push.
- New scenario = new YAML file. No Go code change unless a new
  assertion type is needed.

## What's out of scope for the harness

- **Twitter search + download validation.** We can't mock Twitter's
  DOM responsibly. VideoValidationWorkflow scenarios will need a
  separate approach (record real Twitter responses? use synthetic
  MP4 files? — deferred to when we build the video pipeline).
- **LLM validation calls.** joi's Qwen3-VL responses are
  non-deterministic. Scenarios that touch validation will either
  mock the LLM adapter entirely OR use a canned prompt→response map.
- **Real Temporal server.** We use `testsuite.WorkflowTestSuite`'s
  in-memory Temporal — no docker container for temporal-server.
  Trade-off: doesn't catch Temporal-side bugs (e.g. workflow ID
  conflicts across restarts). Live smoke tests via
  `scripts/trigger_ingest` still exercise real Temporal.

## Sequenced implementation plan

**Phase 1: minimal viable harness (one session, ~4-6 hours)**
- YAML loader + Scenario struct
- Mock api-sports.io HTTP server
- Testcontainer pg with truncate-between-scenarios
- CycleController (advance clock, invoke workflow, read state)
- Assertion engine (Tier 1 assertions only)
- Scenarios 1-3 (happy_path + flicker_recovers + var_overturn)
- Wire into `make test-corpus`

**Phase 2: expand corpus (as we build workflows)**
- When Monitor activities land: scenarios 4-10 pair with them
- When Discovery lands: video-related scenarios
- Ongoing: scenarios written FIRST, then activity code makes them pass

**Phase 3: assertion Tier 2 + 3 (as needed)**
- Metric assertions when we need to catch metric regressions
- Log line assertions when a specific action-emission ordering matters

## Open questions for review

1. **YAML shape** — is the scenario file format above readable?
   Should we split fixture setup / cycles / expected_final_state
   into separate files? Or keep them together for cohesion?
2. **Truncate vs prefix** — clean between scenarios or use scenario-
   prefixed IDs? (I recommend truncate.)
3. **Timing tolerance in assertions** — how strict? Exact seconds
   or "within N seconds of cycle X"?
4. **Where scenarios live** — `test/scenarios/` at repo root, or
   inside `internal/` next to code? I lean root — scenarios are
   integration-test-scoped, not internal.
5. **Do we run corpus in CI on every push, or gated behind a
   nightly?** Depends on how much CI cycle time we have. Push-time
   is stricter but slower.

Nothing implemented until this is signed off. Once signed off, the
"first scenario passes end-to-end" milestone is what defines
Phase T's beginning.
