# Ingest workflow

Current behavior for `IngestWorkflow`. See the
[orchestration index](./README.md) for the complete workflow map.

## IngestWorkflow — as shipped

Daily fixture ingest. Refreshes the tracked-teams filter, fetches the
relevant day(s) from api-sports.io with a smart timezone-lookahead,
categorizes each fixture by API state, upserts to Postgres, ensures a
canonical-name row for every team seen, then runs two-part retention
(hard-delete clipless completed fixtures + reclaim Garage bytes for
clip-bearing ones) beyond the retention window.

### Signature

```go
package workflow

type IngestWorkflowInput struct {
    ManualDate       *time.Time     // nil = today's anchor (scheduled path); set = re-ingest a specific day
    ManualFixtureIDs []int64        // non-empty = fetch by IDs (bypasses the tracked-teams filter + date scan)
    FetchFuture      bool           // daily schedule sets true → today + smart-lookahead future days
    ActivationWindow time.Duration  // kickoff-lookahead auto-activation; zero → config (WORKFLOWS_ACTIVATION_WINDOW, default 5m)
    RetentionDays    int            // prune completed older than this; zero → config, then skip
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
    PrunedFixtures        int  // clipless completed fixtures hard-deleted (Step 4a)
    ReclaimedEvents       int  // clip-bearing events byte-reclaimed via DestroyEvent (Step 4b)
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
0.  GetIngestConfig() → {ActivationWindow, RetentionDays, MaxLookaheadDays}
      Read once at start (workflows can't touch env). Input overrides
      (ActivationWindow, RetentionDays) win; zero falls back to config.
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
3.  IF len(TeamRefs) > 0:
      EnsureAliasPlaceholders(TeamRefs) → {Existing, Inserted, Errors}
4.  IF RetentionDays > 0 — two-part retention (#176, decisions.md 2026-08-11):
    4a. PruneOldFixtures(anchor - RetentionDays days)
          → {Deleted, ReclaimEventIDs}
        PG-only. Hard-deletes completed fixtures older than the threshold
        (keyed on completed_at) that have NO surviving video_shares — the
        clipless half (deleting share-less rows 404s nothing). ALSO returns
        the events of clip-BEARING aged fixtures that still have a live share
        (ListReclaimableEventIDs), for 4b.
    4b. FOR each ReclaimEventID: DestroyEvent(id, reason='policy') [2m timeout]
        Revoke the event's shares → 410 + delete its Garage bytes (the #172
        primitive), KEEPING all rows as tombstones so no shared URL ever
        404s. Best-effort per event (failures → out.Errors), never aborts
        ingest; idempotent (reclaimed events drop off tomorrow's list). This
        is where Garage bytes finally get reclaimed — closes audit G4.
```

Anchor: `ManualDate` if set, else `workflow.Now(ctx)` — deterministic
across replays. Manual-date override propagates through the whole
workflow (fetch window AND retention cutoff both computed from the
anchor) so re-ingesting a past date behaves consistently.

The ingest activity methods — `GetIngestConfig`,
`RefreshTrackedTeamsIfStale`, `FetchFixturesForDay`, `FetchFixturesByIDs`,
`CategorizeAndUpsertFixtures`, `EnsureAliasPlaceholders`,
and `PruneOldFixtures` — live in
`internal/activity/ingest/*.go`, registered on the worker as
methods of `*ingest.Activities`. Step 4b's `DestroyEvent` is the
video-package `PersistActivities` activity (shared with #172's VAR
teardown), not an ingest activity — the workflow calls it by string name.

### Reconcile logic — the load-bearing merge

`CategorizeAndUpsertFixtures` calls `reconcileFixture` per API fixture:

**Existing row present:** refresh only API-mutable fields (Status,
Elapsed, Extra, Kickoff, Home, Away, League, Scores) + LastPolledAt
+ UpdatedAt. Preserve domain-managed fields (State, ActivatedAt,
CompletedAt, LastActivityAt, CreatedAt). Rationale: a fixture already
active in our DB (activated_at set) MUST NOT have its activated_at
cleared by the daily 00:05 re-ingest. LastPolledAt DOES get updated
because ingest is itself a poll. (The planned bucket-suppression that
would have consulted it was abandoned in the ActivePoll/StagingPoll
split — see the note above.)

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
  the activation window), Activate before first Upsert (avoids the "manual ingest at
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
is idempotent (UPSERT semantics + reconcile-merge preserve state)
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
    RetentionDays:         cfg.Workflows.RetentionDays,
}
w.RegisterWorkflow(ffwf.IngestWorkflow)
w.RegisterActivity(ingestActs)
```

Registration happens BEFORE `w.Start(ctx)` — Temporal's reflection
walk runs on Start; anything registered after is silently ignored.

**Wired (O1e):** the daily Temporal Schedule `ingest-scheduled-daily`
(`5 0 * * *`) is registered by `ensureIngestSchedule` in
`internal/app/worker/worker.go`; Create is idempotent (swallows AlreadyRunning).
Manual trigger via `scripts/trigger_ingest/main.go` remains for ad-hoc
re-ingests.
