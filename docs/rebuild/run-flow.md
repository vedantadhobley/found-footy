# System run-flow reference

Narrative walk-throughs of what happens during a single Ingest + Monitor
run, explicit concurrency inventory, and the state machines that
govern fixture + event lifecycles. Companion to
[`architecture.md`](./architecture.md) (component tree) and
[`orchestration.md`](./orchestration.md) (as-shipped workflow/activity
ledger). Reading order: architecture → this doc → orchestration.

**Scope**: as-shipped in `rebuild/go` as of 2026-07-09. Deferrals + gaps
called out inline with `[GAP]` markers. Discovery / VideoValidation /
AssetPersistence workflows (O3) not included — those get their own doc
when they land.

---

## System at a glance

```
                     ┌─────────────────────────────────┐
                     │   API-Football (v3.9.3, HTTPS)  │
                     │   • /leagues  (team roster src) │
                     │   • /teams                      │
                     │   • /fixtures (date, ids, live) │
                     └────────────┬────────────────────┘
                                  │ x-apisports-key
                                  ▼
        ┌───────────────────────────────────────────────────────┐
        │  Adapter: internal/infra/apifootball (typed enums,    │
        │  chunk-parallel batch, unmarshal-time normalization)  │
        └───┬────────────────────┬──────────────────────────────┘
            │                    │
            ▼                    ▼
   ┌────────────────┐   ┌──────────────────────────────────────┐
   │ IngestWorkflow │   │ MonitorWorkflow                      │
   │ (00:05 UTC     │   │ (every 30s, schedule interval)       │
   │  daily)        │   │                                      │
   │ • refresh      │   │ • PreActivateUpcoming (DB-only)      │
   │   tracked      │   │ • ListActiveFixtureIDs                │
   │   teams        │   │ • FetchLiveFixtures (batch by IDs)   │
   │ • fetch        │   │ • ReconcileFixture (per-fixture,      │
   │   fixtures     │   │   parallel across active set)         │
   │   per-day      │   │                                      │
   │ • categorize   │   │ [GAP] 15-min staging poll             │
   │ • ensure       │   │ [GAP] Fixture completion detection    │
   │   aliases      │   │ [GAP] Semantic-event NATS emission    │
   │ • prune        │   │                                      │
   └───────┬────────┘   └──────────────┬───────────────────────┘
           │                           │
           └───────────┬───────────────┘
                       ▼
      ┌────────────────────────────────────────────────┐
      │  Postgres (17)                                 │
      │  • fixtures (state, kickoff, status, ...)      │
      │  • events (natural_key, debounce_count, ...)   │
      │  • tracked_teams_cache                         │
      │  • team_aliases                                │
      │  • video_shares / video_assets  [O3]           │
      │  • event_log / event_monitor_workflows          │
      └────────────────────────────────────────────────┘
```

Two Temporal Schedules drive the whole thing:

| Schedule ID | Cron / Interval | Workflow | Overlap policy |
|---|---|---|---|
| `ingest-scheduled-daily` | `5 0 * * *` (00:05 UTC) | `IngestWorkflow` (`FetchFuture=true`) | default |
| `monitor-scheduled-30s` | every `WORKFLOWS_ACTIVE_FIXTURE_POLL_INTERVAL` (default 30s) | `MonitorWorkflow` | `SKIP` (skip if prior still running) |

Neither Ingest nor Monitor blocks the other — they run on the same
Temporal worker but their activity queues + retries are independent.

---

## Concurrency inventory

Table of every place we do parallel work + every place we intentionally
don't. Read this before proposing "let's parallelize X" — some sequential
paths are that way for good reason.

### Concurrent (real parallelism)

| Location | Fan-out | Bound | Impact |
|---|---|---|---|
| `apifootball.Client.ListFixturesByIDs` internal chunking | ⌈len(ids)/20⌉ goroutines via `errgroup` | vendor's 20-ID cap per call | Verified live 2026-07-09: 50-ID call = 0.12s wall (~5-10× sequential) |
| `MonitorWorkflow` per-fixture reconcile | 1 goroutine per active fixture | Temporal worker concurrency cap (default 100 activities in-flight) | Cycle wall-clock ≈ slowest single reconcile, not sum |
| Monitor's activity retry (per chunk) | independent per chunk | client's `RetryPolicy` | Failed chunks retry without redoing successful ones |

### Intentionally sequential (design, not oversight)

| Location | Why sequential | Alternative + why we didn't |
|---|---|---|
| `MonitorWorkflow` step order (PreActivate → List → Fetch → Reconcile) | Each step depends on prior output | N/A — genuine dependency chain |
| `IngestWorkflow` step order (RefreshTeams → Fetch → Categorize → Aliases → Prune) | Same — chain of dependencies | N/A |
| `IngestWorkflow` smart-lookahead scan (day+2 → day+3 → ...) | Have to see if day+2 is empty before checking day+3; parallel scan of 30 days would burn vendor quota unnecessarily | Speculative parallel (fire N candidate days at once, take first non-empty) — quota-costly; not worth it |
| Ingest by-date loop for the "standard 3-day fetch" | Only 3 calls; parallel gain ~50ms; complicates workflow | Could parallelize; low ROI |

### Sequential but should probably be parallel (optimization opportunities)

| Location | Current cost | Parallel gain | Priority |
|---|---|---|---|
| `RefreshTrackedTeamsIfStale` per-league loop | 5 leagues × 2 API calls each (`/leagues` + `/teams`), sequential = ~5-10s | `errgroup` over leagues = ~1-2s (5× speedup) | P2 — only fires once per 24h |
| Ingest by-date fetch (today/tomorrow/day-after) | 3 sequential API calls per Ingest run | Parallel = ~200-400ms saved | P3 — fires once per day |
| Ingest categorize per-fixture upsert loop | Sequential DB roundtrips | Could batch as single INSERT ... UNNEST | P3 — <1s at current fixture counts (~15/day) |

---

## Fixture state machine

Every fixture is exactly one of three domain states. Transitions
governed by `internal/domain/fixture/state.go` primitives; the switch
on API-reported status happens at ingest categorize + monitor reconcile
time.

```
                ┌─────────┐
                │ staging │  ← Ingest categorize lands upcoming
                └────┬────┘    fixtures here IF kickoff > now+ActivationWindow
                     │
                     │ Fixture.Activate(now)
                     │   • Ingest: kickoff within ActivationWindow OR
                     │             API status ∈ {1h, ht, 2h, ...} (Live())
                     │   • Monitor: PreActivateUpcoming(30min) fires
                     │             on staging fixtures with imminent kickoff
                     ▼
                ┌─────────┐
                │ active  │  ← Monitor polls these every 30s via /fixtures?ids=
                └────┬────┘
                     │
                     │ Fixture.Complete(now)  [GAP — not yet implemented]
                     │   Design intent: fires when API status ∈ {ft, aet, pen, canc, ...}
                     │   AND all events on this fixture have hit terminal
                     │   downstream state (video captured or explicitly
                     │   abandoned). See decisions.md for the open design
                     │   conversation.
                     ▼
                ┌─────────┐
                │completed│  ← Terminal domain state. Retention prune
                └─────────┘    removes these after RetentionDays.
```

**Currently missing** ([GAP] — see workflow audit 2026-07-09 punch list):

- **Fixture completion transition**: Monitor never actually promotes
  fixtures from `active` to `completed`. Currently a fixture whose
  API status is `ft` stays in `active` state forever. Ingest catches
  this on the NEXT DAY if the fixture is still in the 3-day window
  (categorize sees Terminal → completed on FRESH fixtures), but
  fixtures that were `active` from the start of the day never
  transition. This is the biggest known gap right now.
- **PST → NS reschedule detection**: `fixture.Reschedule()` primitive
  exists in domain but no production caller. If a postponed fixture
  gets rescheduled, the new kickoff date isn't picked up.

Emergency-activation path (not shipped yet — see punch list): if a
staging fixture's next poll shows `APIStatus.Live()`, promote
immediately + refresh `Kickoff` from API. Prevents the "user sees
fixture jump from postponed to 15-min-in" UX pothole.

---

## Event debounce state machine

Every tracked event (goal, red card, missed penalty) has a symmetric
counter oscillating over `0..3` on presence/absence votes across
Monitor cycles. Repo state in `internal/infra/pg/event_repo.go`;
counter semantics in
[`docs/decisions.md`](../decisions.md) 2026-07-07 symmetric-counter
entry.

```
            counter: 0
              │  ↑
        insert│  │absence hits floor → SOFT DELETE
              │  │(fixture.event_repo sets Removed=true,
              │  │ RemovedReason='var', RemovedAt=now)
              ▼  │
            counter: 1  ← FirstSeenAt stamped on insert
              │  ↑
         presence  absence
         (+1 vote) (-1 vote)
              │  │
              ▼  │
            counter: 2
              │  ↑
              │  │
              ▼  │
            counter: 3  ← DOWNSTREAM TRIGGER FIRES ONCE
                          (DownstreamTriggered flips to true,
                           never flipped back)
```

**Key invariants**:

- Counter has hard bounds `[0, 3]`. Presence votes at 3 are no-ops
  (cap); absence votes at 0 don't fire (soft-delete already happened).
- `DownstreamTriggered` transitions FALSE→TRUE exactly once at the
  first cycle that pushes counter to 3. Never resets — the workflows
  it spawns run to their own completion regardless of subsequent
  counter oscillation.
- Vote idempotency is keyed by `(event_id, workflow_id)` — same
  MonitorWorkflow run's repeat calls don't double-count. Different
  runs each get one vote.
- Soft-delete on absence-to-zero: `Removed=true`, `RemovedAt=now`,
  `RemovedReason` currently hardcoded to `var` (see workflow-audit
  polish item — domain supports 3 reasons but only one is written).

**Live example from France v Morocco 2026-07-09** — all three events
followed the same path:

1. Cycle 1: event first seen, `Insert` → counter=1, `FirstSeenAt=now`.
2. Cycles 2-3: `RegisterEventPresence` (event still in API response
   set) → counter=2, then counter=3.
3. Counter hits 3 → `DownstreamTriggered=true`, but downstream
   activities aren't wired yet, so `MonitorComplete` stays false.

---

## Ingest run walkthrough

Fires at 00:05 UTC daily. The scheduled invocation passes
`RetentionDays=14, FetchFuture=true` as the workflow input.

### Step 0: `RefreshTrackedTeamsIfStale`

**When**: workflow start. Skipped on the manual-IDs path (that path
targets specific fixtures by ID, so no filter applies).

**What**:

1. Read `OldestRefreshedAt` from `tracked_teams_cache`. If younger
   than `TopFlightCacheHours` (default 24h) → skip, exit early.
2. Loop `TrackedLeagueIDs` (sequential — see concurrency inventory
   #P2). For each league:
   - `GetCurrentSeason(leagueID)` via `/leagues?id=X`, read
     `seasons[].current==true`.
   - `ListTeamsForLeague(leagueID, season)` via `/teams?league=X&season=Y`.
   - Union into an accumulator with dedup by team ID.
3. `TeamRepo.Replace(teams, now)` — single transaction, TRUNCATE
   `tracked_teams_cache` + `pgx.CopyFrom` the new set.

**Latency**: 5-10s on refresh (11 sequential API calls). Skip path is
<10ms (one Postgres MIN query). Since refresh only fires once per 24h,
even the slow path barely registers over the day.

**Failure modes**:
- Single-league failure: aggregated into `Errors` slice, other
  leagues continue. Cache still updated with the leagues that worked.
- All-leagues failure: activity returns error, workflow logs and
  continues with the OLD cache still in pg (fail-open).

### Step 1: fetch fixtures

Branch on input:

**Branch A: `ManualFixtureIDs` set** — targeted retry loop:
- Loop up to 3 attempts with `5s * attempt` backoff
- Each attempt calls `FetchFixturesByIDs(remaining)` — the client
  internally chunks at 20 IDs + parallelizes via `errgroup`
- Any IDs that didn't come back → retry ONLY those. Prevents API
  quota waste on already-successful IDs.

**Branch B: normal daily** — smart-lookahead scan:
1. Fetch anchor day (always).
2. If `FetchFuture=true`:
   a. Fetch anchor+1 (tomorrow).
   b. If tomorrow.Count > 0: also fetch anchor+2 (timezone cover for
      the far side of tomorrow). Standard 3-day path.
   c. If tomorrow.Count == 0: scan anchor+2 through anchor+MaxLookahead
      (default 30) sequentially, take the first non-empty day, then
      also fetch found_day+1 for timezone cover. Handles international
      breaks + sparse-schedule weeks.
3. Deduplicate fixtures by ID across all fetched days.

Each per-day fetch is a `FetchFixturesForDay` activity: reads current
`tracked_teams` set from pg, calls `/fixtures?date=YYYY-MM-DD`, filters
returned fixtures to those where home OR away team ID is in tracked set.
Returns `{Fixtures, Count, FilteredOut}`.

**Fail-open on empty tracked-teams cache**: if the refresh step failed
completely and cache is empty, `FetchFixturesForDay` skips the filter
and returns all fixtures unfiltered — better than silently returning
zero.

### Step 2: `CategorizeAndUpsertFixtures`

**What**: for each fetched fixture:
1. Read existing row from `fixtures` by ID.
2. If not exists → construct fresh `Fixture` domain object + apply
   initial state:
   - Terminal API status → `Complete()` at kickoff
   - Live API status → `Activate()` at now
   - Not started but kickoff within `ActivationWindow` → `Activate()` at now
     (this is Ingest-time pre-activation — matches decisions.md 2026-07-07)
   - Otherwise → stays in `staging`
3. If exists → refresh API-mutable fields (status, score, elapsed, kickoff)
   via `Fixture.UpdateFromPoll(...)`. Domain state (staging/active/completed)
   preserved.
4. Emit `TeamRefs` — list of unique teams observed across all fetched
   fixtures. Used by the next step.

**Latency**: sequential per-fixture upsert. ~15 fixtures/day = <1s total.

### Step 3: `EnsureAliasPlaceholders`

**What**: for each unique team, insert a placeholder row into
`team_aliases` if not present. Only `team_id + team_name` populated —
Wikidata + LLM alias resolution is DEFERRED to a separate batch job
(Python-side decision: don't block Ingest completion on LLM availability).

**Latency**: fast, sequential UPSERTs. <500ms.

### Step 4: `PruneOldFixtures`

**What**: DELETE from `fixtures` where `state='completed' AND
completed_at < (anchor - RetentionDays * 24h) AND NOT EXISTS any
video_shares for that fixture_id`. The `video_shares` guard preserves
public URL stability — a fixture with a shared video never gets pruned.

**Latency**: single DELETE. <100ms at current fixture counts.

### Ingest overall

- Total wall-clock for a normal daily run: **~10-15 seconds** (dominated
  by RefreshTrackedTeamsIfStale when it fires).
- Cache-hit day (no refresh): **~2-3 seconds**.
- API quota used per run: ~11 calls (refresh) + 3-5 calls (fetch) =
  **~14-16 calls per day**. Well under 7500/day Pro quota.

---

## Monitor run walkthrough

Fires every 30s via `monitor-scheduled-30s` Temporal Schedule. Empty
input. Overlap policy: `SKIP` — if the prior cycle is still running
when the next tick fires, skip. Prevents fan-out cascades.

### Step 1: `PreActivateUpcoming(ActivationWindow=30min)`

**What**: DB-only, no API call.

1. `FixtureRepo.ListStagingBeforeKickoff(now + 30min)` — get all
   staging fixtures with kickoff within the next 30 min.
2. For each: if `Fixture.ShouldActivateNow(now, 30min)` → call
   `Fixture.Activate(now)`, upsert.

**Latency**: <100ms (one indexed SELECT + up to a handful of UPDATEs).

**Not implemented** ([GAP]): emergency activation on API-status. See the
15-min staging poll design — a staging fixture whose LIVE status
appears in an API response should activate immediately, refreshing
kickoff.

### Step 2: `ListActiveFixtureIDs`

**What**: `SELECT id FROM fixtures WHERE state='active'`. One
indexed query.

**Latency**: <10ms.

### Step 3: `FetchLiveFixtures(ids)`

**What**: one activity call regardless of ID count. The client
(`apifootball.ListFixturesByIDs`) chunks at 20 IDs per HTTP request
and fires the chunk requests in **parallel** via `errgroup`.
Returns `{Fixtures, FailedIDs}`.

- Partial failure (some chunks succeed, some fail): `FailedIDs`
  populated, workflow logs the count, continues with what it has.
  Next 30s cycle re-requests the missed IDs naturally (they're
  still in ACTIVE state).
- All chunks fail: activity returns error. Workflow logs and exits
  cycle. Next cycle retries.

**Latency**: dominated by slowest chunk's round-trip. 3 chunks × ~300ms
each (parallel) = ~300ms total. Sequential would be ~1s.

### Step 4: `ReconcileFixture` per fixture — PARALLEL

**What**: for each fetched fixture, spawn one `ReconcileFixture`
activity via `workflow.ExecuteActivity`, collect all Futures, then
`.Get(ctx)` in a loop. Temporal dispatches all Activities
concurrently on worker slots.

Inside each `ReconcileFixture`:

1. Read fixture row from pg + apply `UpdateFromPoll(API state)`. 
2. Load all currently-pending events for this fixture from pg.
3. Compute `apiEventSet` (natural_keys from API response) and
   `dbEventSet` (natural_keys from pg pending set).
4. **NEW events** (in API, not in DB):
   - Call `TrackableEventType(Type, Detail, Comments)` — filter
     via the typed enum whitelist.
   - If trackable, `EventRepo.Insert(event, workflowID)` → counter=1,
     first presence vote recorded.
5. **PRESENT events** (in both API and DB): `RegisterEventPresence` →
   counter += 1 (cap 3). If reaches 3 AND `DownstreamTriggered=false`,
   flip trigger to true.
6. **ABSENT events** (in DB, not in API): `RegisterEventAbsence` →
   counter -= 1 (floor 0). If reaches 0, soft-delete the event row.

Output per fixture: `{NewEventsDetected, EventsBecameStable[], EventsRemoved[]}`.

**Latency**: dominated by DB round-trips per event. ~10 events per
active fixture, ~50ms per event round-trip = ~500ms per fixture.
Parallel across fixtures.

### Monitor overall (per cycle)

- Cycle wall-clock: **~1-2 seconds** typical, dominated by
  `ReconcileFixture` fan-out slowest tail. On a night with 30
  active fixtures worldwide: still ~2s.
- Cycles per day: 2880 (every 30s).
- API calls per cycle: 1 (via `/fixtures?ids=`, chunked). Total:
  **~2880 calls/day** for the active poll. Well under quota.

**Not implemented** ([GAP]):

- **15-min staging poll**: designed in decisions.md 2026-07-07,
  behavior not shipped. Should fire on `:00 / :15 / :30 / :45`
  boundaries, poll staging fixtures via API to catch
  postponements + kickoff drift + PST→LIVE transitions.
- **Semantic-event emissions**: no NATS. `EventsBecameStable`
  from output goes nowhere. Blocks entire O3 downstream pipeline.
- **Fixture completion detection**: fixtures never transition to
  `completed`. See fixture state machine section above.

---

## Latency profile

Rough time budgets per stage under normal load. Numbers from live
verification runs 2026-07-09 (France v Morocco day). Actual numbers
depend on network to vendor, DB load, and Temporal task queue depth.

### Ingest

| Stage | Wall-clock (normal) | Wall-clock (refresh path) |
|---|---|---|
| Overall | 2-3s | 10-15s |
| Step 0: RefreshTrackedTeamsIfStale | ~10ms (skip) | 5-10s |
| Step 1: Fetch (3-day path) | ~1s | ~1s |
| Step 1: Fetch (lookahead path) | ~2-3s (depends on scan depth) | same |
| Step 2: Categorize | <1s | <1s |
| Step 3: EnsureAliasPlaceholders | <500ms | <500ms |
| Step 4: PruneOldFixtures | <100ms | <100ms |

### Monitor (per cycle)

| Stage | Wall-clock |
|---|---|
| Overall | 1-2s |
| Step 1: PreActivateUpcoming | <100ms |
| Step 2: ListActiveFixtureIDs | <10ms |
| Step 3: FetchLiveFixtures | 200-400ms (dominated by vendor RTT) |
| Step 4: ReconcileFixture (per fixture, parallel) | ~500ms slowest tail |

---

## Known gaps + deferred behaviors

Cross-referenced from `docs/rebuild/proposals/workflow-audit-2026-07-09.md`
and `docs/rebuild/proposals/api-football-audit-2026-07-09.md`. Ranked by
impact.

### Blocks O2 completion

- **15-min staging poll** — configured, not shipped. Postponements +
  kickoff drift undetected between activations.
- **Fixture completion detection** — nothing marks fixtures completed;
  they stay active indefinitely.
- **Semantic event NATS emissions** — the O3 kickoff. Discovery /
  VideoValidation / AssetPersistence workflows all blocked until this
  lands.
- **Emergency activation on Live() staging** — small addition to
  `PreActivateUpcoming`, depends on staging poll design.

### Correctness follow-ups (small)

- **Removal reason hardcoded to `var`** in `event_repo.RegisterEventAbsence`.
  Domain supports 3 reasons; only VAR is written.
- **PST → NS reschedule detection** — `Fixture.Reschedule()` exists but
  no production caller.
- **429 body inspection** — currently drops the body; would help distinguish
  vendor-side rate limit from other 429 sources.
- **HTTP 200 + non-empty `errors` field** — silently ignored; Python logs
  as WARN.

### Optimization opportunities (P2/P3)

- **Parallelize `RefreshTrackedTeamsIfStale`** — 5 leagues in parallel
  saves ~5-10s per refresh (fires once per 24h).
- **Parallelize Ingest by-date fetch** — 3 sequential calls could be
  parallel; saves ~500ms per Ingest.
- **Batch categorize upserts** — could reduce DB round-trips.

None of these are urgent — daily Ingest at ~10s wall-clock is not a
user-visible bottleneck.

---

## Failure recovery patterns

Where cycles / activities fail and how recovery works.

| Failure | Handled by | Recovery path |
|---|---|---|
| Vendor 429 during Monitor cycle | Client-side `RetryPolicy` (2 attempts, exp backoff) then Temporal activity retry (2 attempts) | Cycle fails → next 30s cycle retries. No data loss (state is in pg). |
| Vendor 5xx during Monitor cycle | Same as 429 | Same |
| Partial batch failure (some chunks succeed, some 5xx) | `FailedIDs` return path | Next cycle picks up missed IDs naturally (they're still in Active state). |
| Postgres transient failure | pgx pool retry + Temporal activity retry | Next cycle retries. |
| Temporal worker restart mid-cycle | Cycle is a workflow with SKIP overlap; new worker picks up the next tick | One cycle skipped max. No data loss (workflow is stateless per cycle). |
| API-Football auth failure | `NewClient` /status probe fails at worker startup | Worker exits, docker restarts, cycle. Human intervention if key rotated. |
| tracked_teams_cache empty on Ingest fetch step | Fetch fails open (returns unfiltered fixtures) | Ingest still populates the categorize step with something rather than nothing. |
| Refresh step fails all leagues | Activity returns error, workflow logs, continues with existing cache | Old cache still used; refresh retries next Ingest (24h later). Or manual `trigger_ingest` triggers a fresh attempt. |

Not yet handled ([GAP]):

- Silent rate-limit blocks — vendor doc says "excess traffic may be
  temporarily or permanently blocked without notice." No anomaly
  detection today would catch a silent block cleanly. Would show up
  eventually via daily-quota gauge tanking or `calls_total{outcome=failure}`
  climbing, but that's inference, not detection.

---

## Related docs

- [`architecture.md`](./architecture.md) — component tree, package
  layout, adapter Instruments pattern.
- [`orchestration.md`](./orchestration.md) — as-shipped workflow +
  activity ledger.
- [`temporal.md`](./temporal.md) — Client + Worker adapter internals.
- [`observability.md`](./observability.md) + [`logging.md`](./logging.md)
  — typed vocabulary + metric families + Loki queries.
- [`../decisions.md`](../decisions.md) — architectural decisions log,
  particularly:
  - 2026-07-07 symmetric-counter debounce
  - 2026-07-07 fixture activation triggers + staging-poll design
  - 2026-07-09 cross-workflow config
  - 2026-07-09 Ingest regression fix
- [`proposals/workflow-audit-2026-07-09.md`](./proposals/workflow-audit-2026-07-09.md)
  — punch list feeding this doc's "Known gaps" section.
