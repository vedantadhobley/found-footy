# Fixture-monitoring workflows

Current behavior for `ActivePollWorkflow` and `StagingPollWorkflow`. See the
[orchestration index](./README.md) for the complete workflow map.

## ActivePollWorkflow — as shipped

30s poll of ACTIVE fixtures. Schedule `active-poll-scheduled` (IntervalSpec 30s).
Per cycle: `GetMonitorConfig` → `ActivateUpcoming` (DB-only staging→active
promotion) → `ListActiveFixtureIDs` → `FetchLiveFixtures` (batched
/fixtures?ids=) → `ReconcileFixture` per fixture (the event set-diff +
3-poll debounce + downstream spawn + completion check). Location:
`internal/workflow/active_poll.go` + `internal/activity/monitor/`.

**Durable transition audit (FF-070).** Activation, completion, known-event
detection, the first stable debounce crossing, and debounce-zero removal each
commit their typed `event_log` evidence in the same Postgres transaction as
the fixture/event mutation. Monitor no longer performs a second best-effort
emit. An audit failure rolls back the transition and lets the Temporal
activity retry it; idempotent debounce votes create at most one audit row.

**Fixture writer ownership (FF-040).** Active and staging poll responses write
through separate repository commands. Both require the expected fixture state
and reject a response older than the stored `last_polled_at`; a rejected active
refresh stops event voting and publication from that stale snapshot. Activation
and completion lock the current state plus observation version, update only
transition-owned fields, and commit their audit in the same transaction.
Active poll fixes the observation version at workflow-cycle start; staging poll
fixes it before its provider call, so response latency does not define order.
Active and staging responses also refresh provider-owned kickoff, team display,
and league fields. An active metadata correction selects `fixture.update` so
the consumer refreshes its authoritative snapshot. See the
[decision](../decisions/2026-08-28-fixture-writers-own-columns.md).

**Provider-integrity shadow phase (FF-075).** API-Football fixture responses now
pass a typed wire contract before Monitor receives them. Every envelope must
have empty `errors`, matching `results`, complete single-page paging, a valid
response array, unique fixture/team identity, nonnegative scores, and event
teams belonging to the fixture. A by-ID chunk must return every requested ID
exactly once and must send `events` as an array; missing and `null` are rejected
while explicit `[]` is valid. One rejected chunk follows the existing
`FailedIDs` next-poll retry path; an all-chunk rejection fails the fetch.

After a successful active refresh, `ReconcileFixture` translates its stored
pre-write snapshot, confirmed event history, and fresh observation into the
provider-independent `providerintegrity` facts. The pure evaluator returns an
advisory fixture policy and bounded reasons. `ActivePollWorkflow` aggregates
the verdicts, recommends a global `positive_only` policy after two regressed
fixtures or three missing confirmed events, logs anomalies, and retains the
batch verdict in its result. A coherent recent one-goal correction remains
trusted only when score decrement and complete event inventory agree.

This phase does **not** enforce its recommendation: fixture refresh, event
votes, completion, and cleanup still follow the existing path. Durable circuit
state, fixture quarantine, and positive-only reconciliation remain FF-075 work
after the shadow corpus is reviewed. See the
[wire-and-shadow decision](../decisions/2026-08-29-provider-fixtures-require-contract-and-shadow-trust.md).

**Typed live-feed classification (FF-077).** `ReconcileFixture` derives the
same consumer projection exposed by REST before and after refreshing provider
facts. Its output has one `FixtureFeedAction`, not independent booleans:
`status`, `update`, or the zero-value no-op. A clock/status change that
stays within one `presentation_state` selects `status`; a state boundary
or any new/removed/stabilized event, unknown-scorer drop, score, penalty,
winner, metadata, or completion change selects `update`. Update always wins if
both classes occur in one observation.

ActivePoll partitions the typed actions and calls one `PublishFixtureBatch`:
`fixture.status` carries the complete
`presentation_state`/`clock`/`status`/`display` projection inline, while
`fixture.update` carries IDs for an authoritative targeted REST fetch. The two
subjects remain disjoint. Publication is best-effort; reconnect recovery is a
full snapshot. Activation itself is not emitted, but the first `NS -> 1H`
reconcile crosses presentation state and therefore selects `fixture.update`.
See the [presentation-contract decision](../decisions/2026-08-30-backend-owns-fixture-presentation.md).

**Event mutable-field refresh (#199, decisions.md 2026-08-15).** For an existing
known-scorer event, `ReconcileFixture` also diffs the provider's mutable
NON-identity fields (`Event.MutableFieldsChanged` — assist, minute, extra, detail)
against the stored row and, on a real delta, calls `UpdateMutableFields` + sets
the typed feed action to `update` so the late value rides `fixture.update`. Assists arrive after the goal
(API-Football fills the assister post-match); minute/extra get VAR-corrected.
Identity (the `natural_key`) is never touched. Active-fixture only — the
completed-fixture backfill is tracked as [`FF-010`](../todo.md#confirmed-and-mitigated-backlog).

**Event debounce — scorer-aware 3-state (2026-08-05).** `natural_key` embeds
`player_id`, so an unknown scorer (`player_id` null) and its later-attributed
known scorer are *different* keys. A goal without a scorer is "not a full event
yet": it lands as a **placeholder at `debounce_count=0`**, casts no presence
vote, and never spawns a search (no player → no Twitter query). It's pinned at
0 while present and **hard-deleted the cycle it disappears** (`DeleteUnknownEvent`)
— normally because the vendor attributed the scorer and a fresh player-keyed
event superseded it. Only known-scorer events vote, debounce 1→3, flip
`downstream_triggered`, and (on absence to 0) soft-delete as `var` — which fires
the **VAR destroy** (#172, decisions.md 2026-08-10): `ActivePollWorkflow` Step 4.5
cancels the event's discovery, revokes its shares (→ the #167 redirect 410s the
clips), and reclaims its Garage objects. Cancellation reduces wasted work but
does not provide mutual exclusion. FF-067 makes the removal update and atomic
clip placement serialize on the same event-row lock: a placement that observes
`removed=true` creates no public state, terminalizes uncredited candidates as
`rejected/event_removed`, and reclaims its staging and deterministic final
keys. If placement commits first, removal waits and the ordinary teardown owns
that share and object. Mirrors Python (`monitor.py`
`initial_count` + `unknown_scorer_disappeared` + `mark_event_removed`); see
[decisions.md](../decisions.md) 2026-08-05. Surfaced per cycle as `unknown_dropped`.

**Stable event sequence identity (FF-027 + FF-062).** Sequence is no longer recomputed
from each provider array's position. Reconcile reads active and removed rows,
matches each scorer/type group to active stored events by ordered nearest match
clock, and allocates unmatched events above the complete historical maximum.
An incomplete score-backed goal inventory requires exact clock matching so a
nearby new goal cannot consume an omitted goal's identity. Exact removed-row
reappearances do not revive or map to the terminal tombstone: the old sequence
remains reserved and the evidence starts a fresh event generation with the next
sequence and a new UUID. The generation follows the ordinary three-presence-vote
and downstream lifecycle. Existing natural keys remain unchanged; a late
insertion or reappearance may receive a higher sequence than a chronologically
later stored event because sequence is durable allocation identity, not display
order. See the
[reappearance decision](../decisions/2026-08-24-removed-event-reappearance-starts-new-generation.md).

**Score-backed goal removal and terminal observation grace (FF-014/FF-063).** A
missing goal no longer receives an absence vote when the aggregate score in
that same provider response exceeds the current API goal count for its
beneficiary team. `ReconcileFixture` returns the protected natural keys as
`GoalAbsencesHeld`, and `ActivePollWorkflow` records them without running VAR
destroy. A true VAR drops the score and resumes normal absence debounce; a
replacement scorer/own-goal identity accounts for the unchanged score and lets
the old identity decay. Missing red cards and missed penalties retain ordinary
absence behavior because they do not affect the score.

The first successful terminal poll in an uninterrupted run sets
`terminal_observed_at`. Later terminal polls preserve it; a successful
non-terminal poll clears it. Failed or missing responses neither clear the
timestamp nor run completion. `WORKFLOWS_TERMINAL_GRACE_PERIOD` defaults to one
hour. After that interval, `AssessCompletion` requires the current fixture to
remain active and terminal, no named event to remain mid-debounce, and no open
`event_downstream_workflows` row. Unknown-player placeholders remain
non-blocking. A new event near the boundary therefore extends monitoring until
its event debounce and downstream work settle.

Provider score/event parity, durable surviving-goal parity, and `PEN` decision
state are recorded in the `fixture.completed` audit payload. They no longer
trap a terminal fixture in active polling when the provider permanently omits
events. Score evidence still guards destructive goal absence votes and identity
matching; the system never fabricates missing events. The legacy
`completion_counter` column remains only for one rollback window and is not
read or written by the new binary. See the
[terminal-grace decision](../decisions/2026-08-25-terminal-observation-grace-bounds-completion.md).

**Score-derived result state (FF-055).** API-Football's `teams.*.winner`
fields identify the current live leader, not only the final result. Normal and
`AET` reconcile therefore derive the nullable winner pair from the aggregate
score; a tie or incomplete score clears both fields. Terminal `PEN` derives it
from `score.penalty`. Exceptional `CANC`, `ABD`, `WO`, and `AWD` responses use
the provider's exact nullable flags because their aggregate scores are not
authoritative. Ingest applies the same domain operation, so daily refresh and
live poll cannot disagree. See the
[decision record](../decisions/2026-08-19-winner-state-is-derived-from-canonical-scores.md).

**Per-event Firefox fleet lifecycle (#160, gated on `FleetEnabled`; live in prod).**
Two hooks straddle the debounce, both gated on the monitor config's
`FleetEnabled` (default false → both inert):
- **Step 4.4 provision.** `ReconcileFixture` returns `NewNamedEventIDs` — the
  events that *this cycle* first arrived with a known player (goals, red cards,
  and missed penalties; debounce_count went to 1, so all data needed for a
  Twitter query now exists). ActivePoll fires
  `ProvisionFirefox` per ID: create+start a dedicated
  `<compose-network>-firefox-ev-<full-event-uuid>` container with the
  history-compatible `ff-firefox-ev-<8hex>` network alias (no blocking health
  wait — the ~30s warm-up hides behind the debounce window). Warming at count=1
  means the instance is ready when the event *triggers* at count=3.
- **Step 4.5 release.** The same step that runs the VAR destroy also calls
  `ReleaseFirefox` for every `EventsRemovedIDs` member — covering both a
  triggered event decaying to 0 (VAR) and a pre-trigger event that provisioned
  at count=1 but decayed before reaching 3. Release is idempotent, so the
  overlap with EventWorkflow's own finalize-release (the happy path) is harmless.
- **Reaper backstop** (StagingPoll, audit P0-5). The provision/release hooks only
  fire while the worker is alive; a crash between provision and release, or a
  failed release, strands a container. `ReapOrphanedFirefox` (below) reconciles
  the labeled container set against the DB every 15 min. See
  [decisions.md](../decisions.md) 2026-08-13 (audit P0-5) for the KEEP predicate and
  why the reaper lives in StagingPoll rather than at worker startup.

## StagingPollWorkflow — as shipped

15-min poll of STAGING fixtures. Schedule `staging-poll-scheduled` (cron
`*/15 * * * *`, runtime-tunable). Fires `PollStagingFixtures`: polls all
staging fixtures + handles vendor edge cases (kickoff-corrected activation,
Live()-emergency activation). Location: `internal/workflow/staging_poll.go`.

Also the home of the **fleet orphan reaper** (audit P0-5): each cycle ends with a
best-effort `ReapOrphanedFirefox` — it diffs the labeled Firefox containers
against `EventRepo.ListLiveFleetEventIDs` (the KEEP set: not-removed events whose
fixture is still active OR whose downstream is still in flight) and releases the
strays past a 120s min-age grace. No-op when the fleet is disabled, so the call
is unconditional. A sweep failure is recorded, never fatal — the next tick
retries. This is the only thing that cleans up a container the live-path
provision/release hooks stranded (worker crash, failed release).
