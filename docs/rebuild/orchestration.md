# orchestration.md — Go rebuild ledger

**Purpose.** This doc records what has actually shipped in the
`internal/workflow/` and `internal/activity/` packages — the workflow
inventory, the activities each workflow orchestrates, the state
transitions each triggers, and any divergences from
[`../rebuild-plan.md`](../rebuild-plan.md) §5.

If code and plan diverge, the divergence is logged in
[`../decisions.md`](../decisions.md). This doc is the ledger; the plan
is the intent.

**Update rule.** Every workflow/activity commit updates this doc in
the same commit. Per the [2026-07-07 working rule](../decisions.md).

## Workflow inventory (2026-07-11, end of Phase O2 + workflow split)

| Workflow | Status | Trigger | Location |
|---|---|---|---|
| IngestWorkflow | ✓ O1c shipped + O1e scheduled daily 00:05 UTC | Temporal Schedule `ingest-scheduled-daily` (`5 0 * * *`) | `internal/workflow/ingest.go` |
| ActivePollWorkflow | ✓ O2 shipped + scheduled 2026-07-11 | Temporal Schedule `active-poll-scheduled` (IntervalSpec 30s) | `internal/workflow/active_poll.go` |
| StagingPollWorkflow | ✓ O2 shipped 2026-07-11 | Temporal Schedule `staging-poll-scheduled` (cron `*/15 * * * *`, runtime-tunable) | `internal/workflow/staging_poll.go` |
| DiscoveryWorkflow | ✓ O3/d shipped 2026-07-23 | Spawned by Monitor's `ReconcileFixture` via `DownstreamSpawner` when `downstream_triggered` flag flips (2026-07-16 decision — Temporal-direct spawn, not NATS-triggered) | `internal/workflow/discovery.go` |
| VideoValidationWorkflow | ⊘ O4 planned | Child of Discovery | — |
| AssetPersistenceWorkflow | ⊘ O5 planned | SignalWithStart from Validation | — |

**Note on the ActivePoll + StagingPoll split** (2026-07-11): plan §5 W2
speced a single `MonitorWorkflow` combining active + staging polling
via bucket-suppression. During implementation the bucket math emerged
as a workaround for cramming two cadences into one workflow. Split
into two workflows on independent Temporal Schedules — see
[`../decisions.md` 2026-07-11 workflow-split entry](../decisions.md)
for the full reasoning (failure isolation, runtime tunability, config
honesty). `PreActivateUpcoming` renamed to `ActivateUpcoming` at the
same time — the "Pre" prefix was misleading.

## IngestWorkflow — as shipped

Daily fixture ingest. Fetches a 3-day window from api-sports.io,
categorizes each fixture by API state, upserts to Postgres, ensures
alias placeholder rows exist for every team seen, prunes completed
fixtures beyond retention.

### Signature

```go
package workflow

type IngestWorkflowInput struct {
    ManualDate       *time.Time     // nil = today's anchor; set = re-ingest a specific day
    ManualFixtureIDs []int64        // non-empty = fetch by IDs, bypass the 3-day window
    FetchFuture      bool           // daily schedule sets true (today + N future days)
    ActivationWindow time.Duration  // kickoff-lookahead auto-activation; zero → 30m
    RetentionDays    int            // prune completed older than this; zero → skip
}

type IngestWorkflowOutput struct {
    Fetched         int
    Staging         int
    Active          int
    Completed       int
    ExistingAliases int
    InsertedAliases int
    PrunedFixtures  int
    Errors          []string  // aggregated per-fixture/per-team failure context
}
```

**Divergences from plan §5 W1 signature — see
[decisions.md 2026-07-07 IngestWorkflow](../decisions.md).**

### Activity sequence

```
1. Fetch (branches on ManualFixtureIDs):
     IF len(ManualFixtureIDs) > 0:
       FetchFixturesByIDs(IDs) → []APIFixture
     ELSE:
       from := anchor - 1d;  to := anchor + 3d
       FetchFixturesForWindow(from, to) → []APIFixture
2. CategorizeAndUpsertFixtures(fixtures, ActivationWindow)
     → {Staging, Active, Completed, TeamRefs, Errors}
3. IF len(TeamRefs) > 0:
     EnsureAliasPlaceholders(TeamRefs) → {Existing, Inserted, Errors}
4. IF RetentionDays > 0:
     threshold := anchor - RetentionDays days
     PruneOldFixtures(threshold) → Deleted
```

Anchor: `ManualDate` if set, else `workflow.Now(ctx)` — deterministic
across replays. Manual-date override propagates through the whole
workflow (fetch window AND retention cutoff both computed from the
anchor) so re-ingesting a past date behaves consistently.

All five activity methods live in
`internal/activity/ingest/activities.go`. Registered on the worker
as methods of `*ingest.Activities`.

### Reconcile logic — the load-bearing merge

`CategorizeAndUpsertFixtures` calls `reconcileFixture` per API fixture:

**Existing row present:** refresh only API-mutable fields (Status,
Elapsed, Extra, Kickoff, Home, Away, League, Scores) + LastPolledAt
+ UpdatedAt. Preserve domain-managed fields (State, ActivatedAt,
CompletedAt, LastActivityAt, CreatedAt). Rationale: a fixture already
active in our DB (activated_at set) MUST NOT have its activated_at
cleared by the daily 00:05 re-ingest. LastPolledAt DOES get updated
because ingest is a poll — future MonitorWorkflow bucket logic will
consult LastPolledAt to skip freshly-touched fixtures.

**Fresh row (Get returns ErrNotFound):** construct via `fixture.New`,
set `LastPolledAt = now` (before state transitions — Activate/Complete
don't touch LastPolledAt so it survives), then apply initial state by
API status:

- **Terminal** (`FT`, `AET`, `PEN`, `CANC`, `ABD`, `AWD`, `WO`) →
  `Activate(kickoff)` + `Complete(now)`. Missed-the-match case;
  ended before we noticed. Two-step transition maintains the
  invariant that completed rows have both activated_at and
  completed_at set.
- **Live** (`1H`, `HT`, `2H`, `ET`, `BT`, `P`, `LIVE`, `SUSP`, `INT`,
  `PST`) → `Activate(now)`. Emergency case: API says the match is
  already playing (or paused mid-play, or postponed with maybe-same-
  day resume) but our DB doesn't have it. Land as active so
  MonitorWorkflow starts polling next cycle. See
  [decisions.md 2026-07-07 status bucketing](../decisions.md) for
  why SUSP/INT/PST count as Live — matches Python.
- **Not started** (`NS`, `TBD`, etc.) → check
  `ShouldActivateNow(now, ActivationWindow)`. If true (kickoff within
  30 min), Activate before first Upsert (avoids the "manual ingest at
  14:55 for 15:00 kickoff sits in staging" Python bug — see
  [decisions.md 2026-07-07 Fixture activation triggers](../decisions.md#2026-07-07--fixture-activation-triggers--staging-poll-design)).
  Otherwise stays staging.

### Alias placeholder pattern (RAG deferral)

`EnsureAliasPlaceholders` inserts blank-resolution placeholder rows;
**Step 3.5 `ResolveAliasesForTeams`** (shipped) then resolves them via
Wikipedia CirrusSearch + Wikidata for teams without a `wikidata_qid`
(cache-hit skip; soft-fail per team so a Wikidata hiccup doesn't fail
ingest; NULL-QID teams auto-retry next cycle).

Rationale: keeps IngestWorkflow independent of joi + Wikidata
availability. If joi is down or the daily LLM quota exhausted,
ingest still succeeds; only the resolution job pauses.

This is a **deliberate departure** from plan §5 W1 which specified
`PreCacheAliasesBatch(teams)` doing full RAG resolution inline. See
[decisions.md 2026-07-07 RAG design deferral](../decisions.md).

### Timeouts + retry

Default activity options per workflow:
- StartToCloseTimeout: 60s
- Retry: exponential backoff 2s → 4s → cap 30s, max 3 attempts

Override for CategorizeAndUpsertFixtures: 120s timeout (DB-bound
over potentially 100s of fixtures per call).

No workflow-level retry policy — an ingest failure surfaces to the
Temporal UI; operator can manually re-run. Rationale: the workflow
is idempotent (UPSERT semantics + reconcile-merge preserve state)
so an aggressive retry adds no value, and hiding failures behind
auto-retry masks real problems.

### Wire-up (O1d)

`cmd/worker/main.go` constructs `*ingest.Activities` with real
dependencies:

```go
ingestActs := &ingestactivity.Activities{
    APIFootball: afClient,                 // *apifootball.Client
    FixtureRepo: pg.NewFixtureRepo(pool),  // domain/fixture.Repo
    AliasRepo:   pg.NewAliasRepo(pool),    // domain/alias.Repo
}
w.RegisterWorkflow(ffwf.IngestWorkflow)
w.RegisterActivity(ingestActs)
```

Registration happens BEFORE `w.Start(ctx)` — Temporal's reflection
walk runs on Start; anything registered after is silently ignored.

**Wired (O1e):** the daily Temporal Schedule `ingest-scheduled-daily`
(`5 0 * * *`) is registered by `ensureIngestSchedule` in
`cmd/worker/main.go`; Create is idempotent (swallows AlreadyRunning).
Manual trigger via `scripts/trigger_ingest/main.go` remains for ad-hoc
re-ingests.

## ActivePollWorkflow — as shipped

30s poll of ACTIVE fixtures. Schedule `active-poll-scheduled` (IntervalSpec 30s).
Per cycle: `GetMonitorConfig` → `ActivateUpcoming` (DB-only staging→active
promotion) → `ListActiveFixtureIDs` → `FetchLiveFixtures` (batched
/fixtures?ids=) → `ReconcileFixture` per fixture (the event set-diff +
3-poll debounce + downstream spawn + completion check). Location:
`internal/workflow/active_poll.go` + `internal/activity/monitor/`.

## StagingPollWorkflow — as shipped

15-min poll of STAGING fixtures. Schedule `staging-poll-scheduled` (cron
`*/15 * * * *`, runtime-tunable). Fires `PollStagingFixtures`: polls all
staging fixtures + handles vendor edge cases (kickoff-corrected activation,
Live()-emergency activation). Location: `internal/workflow/staging_poll.go`.

## DiscoveryWorkflow — as shipped

Spawned Temporal-direct by Monitor's `ReconcileFixture` when
`downstream_triggered` flips (NOT scheduled — 2026-07-16). Per event:
N attempts × M spacing (config, default 15 × 60s). Each attempt:
`GetDiscoveryConfig` → `FetchTeamAliases` → build query → `SearchTweets`
(accumulated `exclude_urls`) → `StoreCandidate` per hit → finally
`MarkDownstreamComplete`. Wall-clock `max_age_minutes` filter
(decisions.md 2026-07-23). Location: `internal/workflow/discovery.go` +
`internal/activity/discovery/`.

## Testing shape

Two-layer testing pattern matches plan §12:

**Unit tests for activities** —
`internal/activity/ingest/activities_test.go`. In-memory fake
`fixture.Repo` + `alias.Repo` + `fixtureFetcher`. Fast (<10ms across
11 tests). Tests state-transition edge cases (fresh terminal,
existing preserves domain fields, empty input, mixed existing/new
aliases, prune threshold).

**Workflow-level tests** — `internal/workflow/ingest_test.go` using
`testsuite.WorkflowTestSuite`. Mocks activities by name via testify
`OnActivity`. Tests the workflow's control flow: activity call
order, conditional-skip branches (empty TeamRefs skips alias step,
zero RetentionThreshold skips prune step), error propagation +
retry policy behavior.

**Live end-to-end via scripts/trigger_ingest** — dev-only script
that dials Temporal, fires a real IngestWorkflow with a tight
window, waits for completion. Exercises the whole chain against
real api-sports.io + dev pg. Used for O1d verification.

## Cross-refs

- Plan §5 (orchestration + workflow inventory) —
  [rebuild-plan.md §5](../rebuild-plan.md#5-orchestration-layer--temporal-workflows-and-activities)
- Plan §5 W1 (IngestWorkflow spec — the intent) —
  [rebuild-plan.md §5 W1](../rebuild-plan.md#workflow-1-ingestworkflow)
- Divergences from plan for IngestWorkflow —
  [decisions.md](../decisions.md)
- Activity inventory (plan) —
  [rebuild-plan.md activity inventory](../rebuild-plan.md#activity-inventory-by-domain-package)
- Architecture ledger (what packages exist) —
  [architecture.md](./architecture.md)
- Temporal specifics (client + worker shape) — [temporal.md](./temporal.md)
