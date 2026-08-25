# Winner state is derived from canonical scores

## Context

API-Football's nullable `teams.home.winner` and `teams.away.winner` fields do
not mean “final result.” During live play they identify the current leader. A
later equalizer changes both fields to `null`.

The Go monitor treated `null` as “no update” and retained the last non-null
value. A team that led before a draw could therefore remain the stored and
public winner after full time. A production audit on 2026-08-19 found stale
winner state on 10 of 12 completed draws. Non-draw completed fixtures in the
same sample agreed with their scores.

Winner state is display data. It must be deterministic from the score and must
not participate in fixture-completion debounce.

## Decision

For ordinary play, including `FT` and `AET`, derive the nullable winner pair
from the aggregate match score:

- home score greater than away: `true` / `false`;
- away score greater than home: `false` / `true`;
- tied or incomplete score: `null` / `null`.

For terminal `PEN`, derive the winner from `score.penalty`. A shootout with a
missing or tied penalty score has no winner and is not coherent enough to
advance or pass fixture completion.

For `CANC`, `ABD`, `AWD`, and `WO`, preserve API-Football's explicit nullable
winner flags. These exceptional outcomes do not have a reliable score
contract. Replace both stored values exactly, including `null`; never retain a
prior flag merely because the current response is null.

Ingest and active reconciliation apply the same domain operation. The public
API continues to expose the same nullable `winner` fields, so this is a
semantic correction without a schema or DTO migration.

## Consequences

- A live equalizer clears the previous leader on the same reconcile cycle and
  emits `fixture.update`.
- A normal completed draw cannot expose a winner, even if the provider flags
  or an earlier poll disagree.
- `PEN` requires a present, non-tied shootout score in both the provider-poll
  vote and the final Postgres readiness predicate.
- Winner state remains outside the three-poll completion debounce and the
  downstream-workflow gate.
- Existing stale production rows require a separate approved data repair and
  consumer invalidation after deployment. This change performs no implicit
  startup backfill.

## Superseded contract

This decision corrects the historical assumption that provider winner flags
appear only once a result is decided. It extends the
[score-backed completion decision](./2026-08-16-score-backed-goal-removal.md):
score evidence remains the played-result authority, and shootout score is now
also required for `PEN` coherence. Current behavior is recorded in the
[monitoring ledger](../orchestration/monitor.md) and tracked as
[`FF-055`](../todo.md#ff-055--live-leader-flags-survive-a-drawn-result).

The later [terminal-grace decision](./2026-08-25-terminal-observation-grace-bounds-completion.md)
retains score-derived winner display but supersedes a decided shootout as a
permanent fixture-completion gate; `PEN` decision state is now audit evidence.
