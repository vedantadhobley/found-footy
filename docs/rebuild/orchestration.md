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

## Workflow inventory (2026-07-07, end of Phase O1)

| Workflow | Status | Trigger | Location |
|---|---|---|---|
| IngestWorkflow | ✓ O1c shipped | Manual (schedule not yet registered) | `internal/workflow/ingest.go` |
| MonitorWorkflow | ⊘ O2 planned | Every 30s via Temporal Schedule | — |
| DiscoveryWorkflow | ⊘ O3 planned | NATS `event.stable` subscriber | — |
| VideoValidationWorkflow | ⊘ O4 planned | Child of Discovery | — |
| AssetPersistenceWorkflow | ⊘ O5 planned | SignalWithStart from Validation | — |

## IngestWorkflow — as shipped

Daily fixture ingest. Fetches a 3-day window from api-sports.io,
categorizes each fixture by API state, upserts to Postgres, ensures
alias placeholder rows exist for every team seen, prunes completed
fixtures beyond retention.

### Signature

```go
package workflow

type IngestWorkflowInput struct {
    FetchWindowFrom    time.Time      // both required; brackets kickoff window
    FetchWindowTo      time.Time
    ActivationWindow   time.Duration  // typical: 30 * time.Minute
    RetentionThreshold time.Time      // zero value = skip prune step
}

type IngestWorkflowOutput struct {
    Fetched          int
    Staging          int
    Active           int
    Completed        int
    CategorizeErrors int
    ExistingAliases  int
    InsertedAliases  int
    AliasErrors      int
    PrunedFixtures   int
}
```

**Divergences from plan §5 W1 signature — see
[decisions.md 2026-07-07 IngestWorkflow](../decisions.md).**

### Activity sequence

```
1. FetchFixturesForWindow(From, To) → []APIFixture
2. CategorizeAndUpsertFixtures(fixtures, ActivationWindow) → {Staging, Active, Completed, TeamRefs}
3. IF len(TeamRefs) > 0:
     EnsureAliasPlaceholders(TeamRefs) → {Existing, Inserted, Errors}
4. IF RetentionThreshold NOT zero:
     PruneOldFixtures(RetentionThreshold) → Deleted
```

All four live in `internal/activity/ingest/activities.go`. Registered
on the worker as methods of `*ingest.Activities`.

### Reconcile logic — the load-bearing merge

`CategorizeAndUpsertFixtures` calls `reconcileFixture` per API fixture:

**Existing row present:** refresh only API-mutable fields (Status,
Elapsed, Extra, Kickoff, Home, Away, League, Scores) + UpdatedAt.
Preserve domain-managed fields (State, ActivatedAt, CompletedAt,
LastActivityAt, CreatedAt). Rationale: a fixture already active in
our DB (activated_at set) MUST NOT have its activated_at cleared by
the daily 00:05 re-ingest.

**Fresh row (Get returns ErrNotFound):** construct via `fixture.New`,
then apply initial state by API status:

- **Terminal** (`FT`, `AET`, `PEN`, `CANC`, `ABD`, `AWD`, `WO`) →
  `Activate(kickoff)` + `Complete(now)`. Missed-the-match case;
  ended before we noticed. Two-step transition maintains the
  invariant that completed rows have both activated_at and
  completed_at set.
- **Live** (`1H`, `HT`, `2H`, `ET`, `BT`, `P`, `LIVE`) →
  `Activate(now)`. Emergency case: API says the match is already
  playing but our DB doesn't have it. Land as active immediately
  so MonitorWorkflow starts polling next cycle.
- **Not started** (`NS`, `TBD`, etc.) → check
  `ShouldActivateNow(now, ActivationWindow)`. If true (kickoff within
  30 min), Activate before first Upsert (avoids the "manual ingest at
  14:55 for 15:00 kickoff sits in staging" Python bug — see
  [decisions.md 2026-07-07 Fixture activation triggers](../decisions.md#2026-07-07--fixture-activation-triggers--staging-poll-design)).
  Otherwise stays staging.

### Alias placeholder pattern (RAG deferral)

`EnsureAliasPlaceholders` deliberately does NOT resolve aliases via
Wikidata/LLM. It only inserts blank-resolution placeholders for teams
without existing rows. A separate resolution
activity/workflow — design deferred — will fill Wikidata + Twitter
aliases later.

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

**Not yet wired:** the daily Temporal Schedule (`5 0 * * *`). Manual
trigger via `scripts/trigger_ingest/main.go` for now. Schedule
registration lands as an O1e follow-up.

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
