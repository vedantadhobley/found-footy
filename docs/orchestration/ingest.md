# Ingest workflow

Current behavior for `IngestWorkflow`. See the
[orchestration index](./README.md) for the complete workflow map.

## IngestWorkflow — as shipped

Daily fixture ingest. Refreshes the tracked-teams filter, fetches the
relevant day(s) from api-sports.io with a smart timezone-lookahead,
categorizes each fixture by API state, stores it in Postgres, ensures a
canonical-name row for every team seen, then reclaims Garage objects that have
left the shared public-history window. Routine retention preserves every SQL
audit row.

### Signature

```go
package workflow

type IngestWorkflowInput struct {
    ManualDate       *time.Time     // nil = today's anchor (scheduled path); set = re-ingest a specific day
    ManualFixtureIDs []int64        // non-empty = fetch by IDs (bypasses the tracked-teams filter + date scan)
    FetchFuture      bool           // daily schedule sets true → today + smart-lookahead future days
    ActivationWindow time.Duration  // kickoff-lookahead auto-activation; zero → config (WORKFLOWS_ACTIVATION_WINDOW, default 5m)
}

type IngestWorkflowOutput struct {
    TrackedTeamsRefreshed bool // did RefreshTrackedTeamsIfStale re-fetch this run
    TrackedTeamsCount     int  // cache size after a refresh; 0 on cache-hit runs
    Fetched               int
    FilteredOut           int  // fixtures the tracked-teams filter dropped
    Staging               int
    Active                int
    Completed             int
    ExistingAliases       int
    InsertedAliases       int
    ReclaimedEvents       int  // events whose outside-window objects were reclaimed
    Errors                []string // aggregated per-fixture/per-team failure context
}
```

**Divergences from plan §5 W1 signature — see
[decisions.md 2026-07-07 IngestWorkflow](../decisions.md).**

### Activity sequence

Sequential — each step feeds the next; no parallel branches (daily
ingest isn't throughput-bound, and sequencing keeps failure attribution
simple).

```
0.  GetIngestConfig()
      → {ActivationWindow, CompletedFixtureDates, MaxLookaheadDays}
      Read once at start because workflows cannot touch environment config.
      ActivationWindow input overrides the configured activation value.
0.5 IF NOT manual-IDs path:
      RefreshTrackedTeamsIfStale()                         [120s timeout]
        → {Refreshed, TotalTeams, PerLeagueCounts, PreservedLeagues}
      Re-fetches each tracked league's current-season roster into
      tracked_teams_cache when stale. Non-fatal: on failure fetch proceeds
      with the preserved cache; an empty cache makes date fetches fail closed.
      Partial-refresh-safe (audit P1-1): a league that errors OR returns an
      empty roster (season rollover) keeps its PRIOR rows + original
      refreshed_at (so it's retried next run) instead of being wiped; only an
      all-league failure aborts without touching the cache. Preserved leagues
      surface in PreservedLeagues + a WARN log.
1.  Fetch (branches on ManualFixtureIDs):
      IF len(ManualFixtureIDs) > 0:
        FetchFixturesByIDs(IDs) → {Fixtures, FailedIDs}
          Targeted-retry loop: re-request only FailedIDs, up to 3 attempts,
          linear backoff (in-cycle recovery beats waiting 24h). By-ID
          bypasses the tracked-teams filter.
      ELSE (by-date, smart timezone-lookahead):
        FetchFixturesForDay(anchor)                          [always]
        IF FetchFuture:
          FetchFixturesForDay(anchor+1)                      [tomorrow]
          IF tomorrow non-empty: FetchFixturesForDay(anchor+2)
          ELSE: scan anchor+2 .. anchor+MaxLookaheadDays for the next
                non-empty day, then also fetch that day + 1
        Dedupe by fixture ID across days; each day's FilteredOut (dropped
        by the tracked-teams filter) accumulates into the output.
2.  CategorizeAndUpsertFixtures(fixtures, ActivationWindow) [120s timeout]
      → {Staging, Active, Completed, TeamRefs, Errors}
      The Temporal activity name remains stable for replay. Its repository
      write is StoreFromIngest, not a generic full-row upsert.
3.  IF len(TeamRefs) > 0:
      EnsureAliasPlaceholders(TeamRefs) → {Existing, Inserted, Errors}
4.  PlanMediaRetention(CompletedFixtureDates)
      → {Cutoff, EventIDs}
      Compute midnight UTC for the oldest of the newest N distinct completed
      fixture kickoff dates. Select events below it that still own at least one
      asset with object_reclaimed_at IS NULL. No fixture or audit row is
      deleted.
    FOR each EventID: DestroyEvent(id, reason='policy') [2m timeout]
      Revoke all shares → 410, attempt every unreclaimed Garage object, and
      stamp object_reclaimed_at only after a successful idempotent delete.
      Partial failures return an aggregate error so Temporal retries only the
      still-unstamped objects. Exhausted failures enter out.Errors without
      aborting unrelated ingest work and remain eligible on the next ingest.
```

Anchor: `ManualDate` if set, else `workflow.Now(ctx)` — deterministic across
replays. It controls fixture fetch only. Retention always derives its window
from current durable completed-fixture dates.

The ingest activity methods — `GetIngestConfig`,
`RefreshTrackedTeamsIfStale`, `FetchFixturesForDay`, `FetchFixturesByIDs`,
`CategorizeAndUpsertFixtures`, and `EnsureAliasPlaceholders` — live in
`internal/activity/ingest/*.go`, registered on the worker as
methods of `*ingest.Activities`. `PlanMediaRetention` lives in
`internal/activity/retention`; `DestroyEvent` is the video-package
`PersistActivities` activity shared with VAR teardown. Its stable Temporal
name is retained even though policy retention also invokes it.

### Reconcile logic — the load-bearing merge

`CategorizeAndUpsertFixtures` calls `reconcileFixture` per API fixture:

**Existing row present:** build the current API snapshot (status, clock,
kickoff, teams, league, score, shootout, and derived result). The repository
applies those provider fields only when the incoming `last_polled_at` is newer
than storage. Equality is an idempotent no-op, so an active/staging poll wins a
same-cycle tie. It never changes State, ActivatedAt, CompletedAt,
TerminalObservedAt, LastActivityAt, or CreatedAt on conflict. This preserves a
newer active poll and prevents daily ingest from clearing lifecycle state. The
workflow fixes that observation timestamp before starting its provider fetch;
activity latency and retry do not change the ordering. The
[FF-040 decision](../decisions/2026-08-28-fixture-writers-own-columns.md)
defines the shared writer contract.

**Live-feed emit (N6, decisions.md 2026-08-14).** `reconcileFixture` also reports
a `changed` bool — true for a fresh row, or for an existing row whose
frontend-meaningful fields moved (status / kickoff / score / penalty / winner; a
bare LastPolledAt/UpdatedAt bump does NOT count). `CategorizeAndUpsertFixtures`
collects the changed ids into `ChangedIDs`, and IngestWorkflow fires one
`PublishFixtureBatch` (update-only) for them → `fixture.update`. Best-effort; a
lost batch heals on the consumer's next window refetch.

**Fresh row (Get returns ErrNotFound):** construct via `fixture.New`,
set `LastPolledAt = now` (before state transitions — Activate/Complete
don't touch LastPolledAt so it survives), then apply initial state by
API status:

- **Terminal** (`FT`, `AET`, `PEN`, `CANC`, `ABD`, `AWD`, `WO`) →
  `Activate(kickoff)` + `Complete(now)`. Missed-the-match case;
  ended before we noticed. Two-step transition maintains the
  invariant that completed rows have both activated_at and
  completed_at set. This historical/direct path deliberately does not start
  terminal grace or set `terminal_observed_at`; FF-010 owns any bounded
  completed-fixture event repair.
- **Live** (`1H`, `HT`, `2H`, `ET`, `BT`, `P`, `LIVE`, `SUSP`, `INT`,
  `PST`) → `Activate(now)`. Emergency case: API says the match is
  already playing (or paused mid-play, or postponed with maybe-same-
  day resume) but our DB doesn't have it. Land as active so
  ActivePollWorkflow starts polling next cycle. See
  [decisions.md 2026-07-07 status bucketing](../decisions.md) for
  why SUSP/INT/PST count as Live — matches Python.
- **Not started** (`NS`, `TBD`, etc.) → check
  `ShouldActivateNow(now, ActivationWindow)`. If true (kickoff within
  the activation window), Activate before first StoreFromIngest (avoids the "manual ingest at
  14:55 for 15:00 kickoff sits in staging" Python bug — see
  [decisions.md 2026-07-07 Fixture activation triggers](../decisions.md#2026-07-07--fixture-activation-triggers--staging-poll-design)).
  Otherwise stays staging.

### Canonical team-name record

`EnsureAliasPlaceholders` creates a `team_aliases` row for each observed team
when one does not exist. The row supplies a stable canonical API-Football name
to EventWorkflow. The Wikipedia→Wikidata resolver was removed on 2026-08-16;
no resolution activity follows this step, and the stored `aliases` array is not
passed to the query builder. Discovery derives player tokens and a team
abbreviation with deterministic text operations.

### Timeouts + retry

Default activity options:
- StartToCloseTimeout: 60s
- Retry: exponential backoff 2s → cap 30s (coefficient 2), max 3 attempts

Per-activity timeout overrides (same retry policy):
- `RefreshTrackedTeamsIfStale`: 120s (~6 leagues × 2 API calls each)
- `CategorizeAndUpsertFixtures`: 120s (DB-bound over 100s of fixtures)

No workflow-level retry policy — an ingest failure surfaces to the
Temporal UI; operator can manually re-run. Rationale: the workflow
is idempotent (monotonic provider storage + lifecycle preservation)
so an aggressive retry adds no value, and hiding failures behind
auto-retry masks real problems.

### Wire-up (O1d)

`internal/app/worker/worker.go` constructs `*ingest.Activities` with real
dependencies:

```go
ingestActs := &ingestactivity.Activities{
    APIFootball:           afClient,      // *apifootball.Client
    FixtureRepo:           fixtureRepo,   // domain/fixture.Repo
    AliasRepo:             aliasRepo,     // domain/alias.Repo
    TeamRepo:              teamRepo,      // domain/team.Repo (tracked-teams cache)
    TrackedLeagueIDs:      cfg.APIFootball.TrackedLeagueIDs,
    TopFlightCacheHours:   cfg.APIFootball.TopFlightCacheHours,
    FetchWindowFutureDays: cfg.APIFootball.FetchWindowFutureDays,
    ActivationWindow:      cfg.Workflows.ActivationWindow,
    CompletedFixtureDates: cfg.History.CompletedFixtureDates,
}
w.RegisterWorkflow(ffwf.IngestWorkflow)
w.RegisterActivity(ingestActs)
w.RegisterActivity(&retentionactivity.Activities{
    Fixtures: fixtureRepo,
    Assets:   assetRepo,
})
```

Registration happens BEFORE `w.Start(ctx)` — Temporal's reflection
walk runs on Start; anything registered after is silently ignored.

**Wired (O1e):** the daily Temporal Schedule `ingest-scheduled-daily`
(`5 0 * * *`) is registered by `ensureIngestSchedule` in
`internal/app/worker/worker.go`; Create is idempotent (swallows AlreadyRunning).
Manual trigger via `scripts/trigger_ingest/main.go` remains for ad-hoc
re-ingests.
