# Fixture Completion Contract — design proposal

**Status:** SHIPPED (2026-07-11 machinery; auto-widened once EventWorkflow/#164c
began registering rows). The contract is live — see the **As-built notes** banner
below for the current deltas from this text.

> **FF-014 correction deployed 2026-08-17:** played terminal
> responses now advance completion only when their current scoring-event array
> exactly matches the reported score. The removal path holds an omitted stored
> goal while the score still requires it, and the final gate independently
> requires reported-score/surviving-goal parity.

> **⚠ AS-BUILT NOTES (2026-08-06).** The machinery below shipped and is live —
> `event_downstream_workflows`, `completion_counter`, the single-query check
> (`FixtureReadyToComplete` in `internal/infra/pg/fixture_repo.go` /
> [`schema.sql`](../../../internal/infra/pg/schema.sql)), and the
> `ReconcileFixture` call (see [`../../orchestration.md`](../../orchestration.md)).
> Current deltas from the original proposal:
> - **Winner data is wired for result display, not completion.**
>   `ReconcileFixture` calls `Fixture.UpdateWinners` from every active poll, but
>   FF-014 removes the winner-data bypass: every fixture requires three
>   coherent terminal votes.
> - **`RecordPollForCompletion` never existed under that name.** The 3-poll
>   counter logic shipped as the unexported `updateCompletionCounter()`, called
>   by `UpdateFromPoll`.
> - **The completion gate now excludes unknown-scorer placeholders (G1,
>   2026-08-06).** Contract item 3 / the events `NOT EXISTS` clause gained
>   `AND e.debounce_count > 0`, so a placeholder that never attributes a scorer
>   (debounce_count=0, never triggers downstream) no longer strands the fixture
>   in `active` forever (audit-2026-08-05 G1).
> - **Played terminal results require two score/event checks (FF-014).** `FT`,
>   `AET`, and `PEN` advance the counter only on same-response parity, then the
>   final gate requires exact per-team equality between the reported score and
>   surviving stored goals. Exceptional terminal statuses vote on terminal
>   status alone.

**Cross-refs:**
- Plan intent — [`../../rebuild-plan.md`](../rebuild-plan.md) §8 (fixture state machine), §5 (workflow coordination)
- Python behavior spec — [`../python-functional-spec.md`](../python-functional-spec.md) §8 (Fixture Completion Behavior)
- Prior decisions — [`../../decisions.md`](../../decisions.md):
  - 2026-07-11 workflow split (ActivePoll + StagingPoll)
  - 2026-07-07 symmetric-counter debounce
- Related workflow-audit item — [`workflow-audit-2026-07-09.md`](../audits/workflow-audit-2026-07-09.md) P0 #2 (fixture completion detection)
- Working discipline — [`../../../AGENTS.md § Working discipline`](../../../AGENTS.md#working-discipline-mandatory-since-2026-07-07-retro)

## Purpose

Move a fixture from `state='active'` to `state='completed'` when — and
only when — every downstream side effect has finished. Two consumers
of this transition:

1. **Retention prune** — `IngestWorkflow.PruneOldFixtures` only touches
   completed fixtures. Fixtures that never complete accumulate
   indefinitely in `active` state.
2. **Frontend/API** — anything reading `fixtures_active` for "live
   matches" should stop seeing finished ones. Anything reading
   `fixtures_completed` for "match history" should see them.

Getting this wrong is worse than not doing it. Moving a fixture to
`completed` while a downstream workflow is still updating its events
(adding videos, updating ranks, adding sentiment scores) means those
updates land on a "frozen" fixture — either they fail (if we add
constraints) or they silently corrupt state.

## Design goals

1. **Pluggability.** New downstream workflow lands (sentiment analysis,
   text summarization, whatever) → completion contract auto-widens
   without touching the completion-check code.
2. **Introspectable.** Fixture stuck in `active`? A single query should
   tell you which workflow is holding it up, not just "something is."
3. **Race-free.** Two workflows finishing concurrently should not
   both think they're the "last one" and both try to trigger the
   state transition.
4. **Polling, not event-driven.** ActivePollWorkflow's per-fixture
   reconcile step checks completion at the end. No signal wiring
   between downstream workflows and the completion trigger.
5. **Ship the machinery even before it's fully useful.** Retention
   prune stays broken pre-cutover regardless, so shipping a partial
   is fine as long as the shape is right.

## The contract

A fixture is **ready to complete** when all of the following hold:

1. **API status is Terminal** — `FT`, `AET`, `PEN`, `CANC`, `ABD`,
   `WO`, or `AWD` (per `fixture.APIStatus.Terminal()`).
2. **Completion counter debounce satisfied** — `completion_counter >= 3` after
   three consecutive coherent Terminal observations. For `FT`, `AET`, and
   `PEN`, coherent means exact same-response score/scoring-event parity;
   exceptional terminal statuses vote on terminal status alone. Winner data
   does not bypass the counter.
3. **Every non-removed *known-scorer* event has settled its debounce** —
   `NOT EXISTS event WHERE fixture_id=$1 AND removed=false AND downstream_triggered=false AND debounce_count>0`.
   Equivalent: every real event is either VAR'd (soft-removed) or crossed to
   stable (downstream_triggered). The `debounce_count>0` clause (G1, 2026-08-06)
   excludes unknown-scorer placeholders, which never trigger downstream and
   would otherwise strand the fixture in `active` forever.
4. **No downstream workflows still in flight for any event** —
   `NOT EXISTS event_downstream_workflows edw JOIN events e ON edw.event_id=e.id WHERE e.fixture_id=$1 AND edw.completed_at IS NULL`.
5. **Played-result score parity** — for `FT`, `AET`, and `PEN`, each team's
   reported score equals its count of surviving stored goal events. `CANC`,
   `ABD`, `WO`, and `AWD` bypass parity because they may not represent a played
   result.

### The pluggable checklist — `event_downstream_workflows`

The load-bearing extensibility mechanism. One row per (event,
workflow_type, workflow_id) that touches the event. Adding a new
downstream workflow requires ZERO schema change — it just picks a
new `workflow_type` string value.

```sql
CREATE TABLE event_downstream_workflows (
    event_id       UUID   NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    workflow_type  TEXT   NOT NULL,                        -- 'discovery', 'download', 'upload', 'sentiment', ...
    workflow_id    TEXT   NOT NULL,                        -- Temporal workflow ID
    started_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at   TIMESTAMPTZ,                            -- NULL = still in flight
    outcome_class  TEXT,                                   -- 'success', 'failed_geo_restricted', 'timeout', ...
    metadata       JSONB,                                  -- workflow-type-specific extras
    PRIMARY KEY (event_id, workflow_type, workflow_id)
);

-- Partial index optimized for the "any in-flight?" question.
CREATE INDEX event_downstream_workflows_pending
    ON event_downstream_workflows (event_id)
    WHERE completed_at IS NULL;
```

**Usage protocol** for every downstream workflow:

```
On workflow start:
  INSERT INTO event_downstream_workflows
    (event_id, workflow_type, workflow_id)
  VALUES ($1, $2, $3)
  ON CONFLICT DO NOTHING;   -- idempotent for Temporal replay

On workflow completion (success OR failure):
  UPDATE event_downstream_workflows
  SET completed_at = NOW(),
      outcome_class = $4,
      metadata = $5
  WHERE event_id = $1
    AND workflow_type = $2
    AND workflow_id = $3;
```

### Completion counter on fixtures

```sql
ALTER TABLE fixtures ADD COLUMN completion_counter INT NOT NULL DEFAULT 0;
```

Symmetric to the debounce counter on events:
- Reset to 0 when API status is not Terminal.
- For played results, reset to 0 when the terminal response's score and
  scoring-event array disagree or either score is nil.
- Increment (capped at 3) on a coherent Terminal response.
- Fixture is eligible for state transition only when the counter reaches 3.

Why 3? Same reason as event debounce — three consecutive polls give
high confidence the vendor has truly finalized status, not just
briefly flipped to FT then back.

### Winner-data fast-path — superseded by FF-014

Python spec §8: fixture moves to completed when the counter reaches 3
**OR winner data exists** (`teams.home.winner` or `teams.away.winner`
is non-null). This catches decided-score cases faster — vendor
sometimes sets winner flags before locking status.

Encoded on `fixture.Fixture`:

```go
func (f *Fixture) HasDecidedWinner() bool {
    return f.HomeWinner != nil || f.AwayWinner != nil
}
```

(New nullable-bool fields on the fixture row, populated from the API
poll's `teams.home.winner` / `teams.away.winner`.)

This was implemented after the original proposal, then removed by FF-014. The
winner fields remain populated for consumers, but completion eligibility is
always `completion_counter >= 3`.

### The full completion check as one query

```sql
SELECT
    f.api_status_short IN ('ft','aet','pen','canc','abd','wo','awd')
    AND f.completion_counter >= 3
    AND (
        f.api_status_short IN ('canc','abd','wo','awd')
        OR (
            f.api_status_short IN ('ft','aet','pen')
            AND f.home_score IS NOT NULL
            AND f.away_score IS NOT NULL
            AND f.home_score = (
                SELECT COUNT(*) FROM events score_home
                WHERE score_home.fixture_id = f.id
                  AND score_home.event_type = 'goal'
                  AND score_home.team_id = f.home_team_id
                  AND score_home.removed = false
            )
            AND f.away_score = (
                SELECT COUNT(*) FROM events score_away
                WHERE score_away.fixture_id = f.id
                  AND score_away.event_type = 'goal'
                  AND score_away.team_id = f.away_team_id
                  AND score_away.removed = false
            )
        )
    )
    AND NOT EXISTS (
        SELECT 1 FROM events e
        WHERE e.fixture_id = f.id
          AND e.removed = false
          AND e.downstream_triggered = false
          AND e.debounce_count > 0          -- G1 (2026-08-06): exclude unknown-scorer placeholders
    )
    AND NOT EXISTS (
        SELECT 1 FROM event_downstream_workflows edw
        JOIN events e ON edw.event_id = e.id
        WHERE e.fixture_id = f.id
          AND edw.completed_at IS NULL
    )
    AS ready_to_complete
FROM fixtures f
WHERE f.id = $1;
```

## Where the check runs

**ActivePollWorkflow's `ReconcileFixture` activity**, at the end of
its per-fixture work. After the event-diff loop:

```go
if ready, err := repo.FixtureReadyToComplete(ctx, f.ID); err == nil && ready {
    if err := f.Complete(now); err == nil {
        _ = repo.Upsert(ctx, f)
    }
}
```

Runs once per fixture per 30s cycle. If the fixture is not ready, the
check is cheap (returns fast). If it IS ready, the transition happens
same cycle.

## Historical first-commit snapshot (2026-07-11)

> This section preserves rollout history. It is not the current contract; use
> the as-built notes and contract above.

1. **Schema**: `event_downstream_workflows` table (unused for now —
   no downstream workflows exist), `completion_counter` column on
   `fixtures`, `home_winner` / `away_winner` nullable-bool columns.
2. **Domain**:
   - `Fixture.CompletionCounter int` field
   - `Fixture.HomeWinner *bool`, `Fixture.AwayWinner *bool`
   - `updateCompletionCounter()` (unexported, run by `UpdateFromPoll`) —
     increments/resets the counter based on `APIStatus.Terminal()`
   - `Fixture.HasDecidedWinner() bool`
3. **Repo**:
   - `FixtureRepo.FixtureReadyToComplete(ctx, id) (bool, error)` — the query above
   - `FixtureRepo.Upsert` writes the new columns
4. **Activity**: `ReconcileFixture` runs the completion check at end
   of its work; transitions state on ready.
5. **Tests**: unit tests for each transition condition. pg integration
   tests for the query.

**What the first commit delivered before O3-O5:**

The `event_downstream_workflows` table is empty (no downstream
workflows to register). So the completion check reduces to:

- API status Terminal
- Completion counter >= 3 (or winner data)
- Every non-removed event has downstream_triggered=true

For fixtures with **zero non-removed events** (0-0 matches, all-VAR'd
fixtures, CANC/ABD with no goals) — this trivially passes. Those
fixtures complete correctly.

For fixtures with events that stabilized (crossed debounce to 3) —
this ALSO passes today, because there are no downstream workflows to
register in `event_downstream_workflows`. **These fixtures will
prematurely complete if O3-O5 aren't yet built.** In prod this would
be a real bug; pre-cutover it's fine (no user-facing consequence).

**Mitigation for pre-cutover: nothing.** We're not exposing completed
fixtures publicly yet. When O3-O5 land, they start registering rows
and the completion check auto-widens.

**If we wanted a belt-and-suspenders**, we could add a config gate:

```
WORKFLOWS_REQUIRE_DOWNSTREAM_FOR_COMPLETION=false
```

When true, refuse to complete any fixture where any event has
`downstream_triggered=true` unless there's at least one row in
`event_downstream_workflows` for that event. Flip to `true` in prod
config when O5 ships. Skipping this in the first commit — the pre-
cutover safety window doesn't need it.

## Historical O3-O5 rollout expectation

As soon as each downstream workflow follows the register/complete
protocol above:

- **O3 Discovery**: `INSERT` at workflow start, `UPDATE completed_at`
  at end (either exit reason). Adds one row per event.
- **O4 VideoValidation** (per DownloadWorkflow): each download
  workflow inserts one row, updates on end. Adds up to 10 rows per
  event.
- **O5 AssetPersistence** (UploadWorkflow): inserts on first signal
  received, updates on idle-timeout exit. One row per event.
- **Post-MVP Sentiment / TextAnalysis / whatever**: same pattern.

The completion check code doesn't change. Just more rows to consider.

## Rejected alternatives

- **Reference counter (`active_downstream_count int` on fixture).**
  Single integer. Simpler shape but no introspection ("which
  workflow is stuck?" is not answerable) and race-prone if
  increment/decrement isn't paired exactly.
- **JSONB blob on events.** `downstream_pending TEXT[]` on the event
  row. Adds a partial index dance and heavier writes than a separate
  table. No natural completed_at timestamp for audit.
- **Per-workflow-type tables** (extending the existing
  `event_download_workflows` pattern). Each new workflow type =
  new table + query branch in the completion check. Anti-
  pluggability.
- **Event-driven completion trigger** (last workflow signals
  ActivePoll). Adds signal wiring, race-prone ("am I the last?").
  Polling every 30s costs nothing.

## Open questions (none right now)

Everything in this doc is committed. If something changes during
implementation, an entry lands in `../decisions.md` per the
working discipline.

## Change log

- 2026-07-11 — initial version. Ships with the first commit of the
  minimum-viable machinery.
