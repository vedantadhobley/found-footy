# Terminal observation grace for fixture completion

**Status:** deployed in Found Footy release `5c105af` on 2026-08-25; natural
fixture validation remains open.
The as-built contract and rationale live in the
[monitoring ledger](../../orchestration/monitor.md) and
[decision record](../../decisions/2026-08-25-terminal-observation-grace-bounds-completion.md).
This proposal preserves the design and rollout boundary for
[`FF-063`](../../todo.md#ff-063--terminal-fixture-can-remain-active-on-a-permanently-incomplete-inventory).

## Problem

Found Footy currently completes a played fixture only after three consecutive
terminal API-Football responses whose event arrays exactly explain the reported
score. The final database gate repeats that score-to-surviving-event parity
check. This protects real goals from transient provider-array omissions, but it
has no bounded exit when API-Football never supplies the missing events.

Zaragoza–Athletic fixture `1607295` is the concrete failure. API-Football still
reports `FT`, 3–1, and no events from either the fixture or dedicated event
endpoint. The fixture therefore remained in Found Footy's `active` state and
the 30-second polling loop for three days. More polling cannot recover evidence
that the upstream source does not have.

The fix must preserve two separate guarantees:

1. Keep polling long enough for late provider events to complete their ordinary
   debounce and downstream workflow lifecycle.
2. Stop polling after a bounded terminal window even when score and event
   inventory never become coherent.

## Proposed contract

Add `fixtures.terminal_observed_at`. It records the start of the current
continuous terminal observation window for a monitored fixture.

- A successful terminal poll sets it when it is null.
- Later successful terminal polls preserve it.
- A successful non-terminal poll clears it.
- A failed or missing poll does not clear it, but cannot complete the fixture.
- A later fresh terminal response must still be present when completion runs.

Use a typed `WORKFLOWS_TERMINAL_GRACE_PERIOD` setting with a default of one
hour. This is a real policy knob: production evidence may justify changing the
window without changing fixture semantics or Temporal history.

A fixture may transition from `active` to `completed` when all of these are
true:

1. The current successful provider response is terminal.
2. `terminal_observed_at <= now - terminal_grace_period`.
3. No surviving known-player event is mid-debounce: there is no event with
   `downstream_triggered=false` and `debounce_count>0`.
4. No registered downstream workflow for any fixture event remains open.

Unknown-player placeholders remain non-blocking. They stay at
`debounce_count=0`, cannot start a useful player-based search, and must not keep
a fixture active forever.

The one-hour window replaces the fixture-level three-poll completion counter.
It does **not** replace the event-level three-poll presence and absence
debounce. If an event first appears at 59:30, that event blocks completion until
it either reaches stable and finishes downstream work or decays to removal.

No new fixture lifecycle state and no new Temporal workflow are required. The
existing active-poll schedule already provides the timer and the fresh terminal
observation.

## Score evidence after this change

Score parity stops being a permanent fixture-completion gate. It retains three
narrow roles:

1. **Goal-removal guard.** When the aggregate score still requires an omitted
   stored goal, do not cast an absence vote or classify that goal as VAR.
2. **Identity safety.** When the current goal array is incomplete, require exact
   identity matches so a newly reported goal cannot consume an omitted stored
   goal's sequence.
3. **Completion evidence.** Record whether the current provider array and the
   durable surviving goal inventory explain the result when completion occurs.

The system still does not fabricate an event. A 3–1 fixture with no reported
goals completes after the grace period with an auditable incomplete inventory;
it does not gain four synthetic goal rows.

For played results, the `fixture.completed` audit payload should record:

- provider score/event parity;
- durable score/event parity;
- whether a `PEN` result has a present, decided shootout score;
- `terminal_observed_at`, `completed_at`, and the configured grace period.

Exceptional terminal statuses (`CANC`, `ABD`, `WO`, and `AWD`) should record
those parity fields as not applicable. The fixture row plus its surviving event
rows can independently reconstruct durable parity after the transition, so a
new persisted completion-quality column is not required. The enriched
`event_log` row is the convenient forensic record.

## Time semantics and frontend behavior

The three timestamps have different meanings:

| Field | Meaning |
|---|---|
| `activated_at` | Found Footy began active monitoring. |
| `terminal_observed_at` | Found Footy first observed the current uninterrupted terminal status. |
| `completed_at` | Found Footy stopped active monitoring after the grace and work gates passed. |

`completed_at` is not the final-whistle time. With a one-hour grace it is
normally one hour or more after the fixture first becomes `FT`, and downstream
work may delay it further.

The public `last_activity_at` recency key therefore becomes:

```text
max(
  activated_at,
  terminal_observed_at,
  latest first_seen_at among surviving known-player events
)
```

For completed rows created before this field exists, use `completed_at` only
when `terminal_observed_at` is null. This compatibility fallback preserves the
ordering of retained historical fixtures. New fixtures use the terminal
observation, so the internal completion transition does not make a finished
match jump to the top one hour later.

The Go API does not need to expose `terminal_observed_at`. It only changes the
derived `last_activity_at` value. The Vedanta Systems BFF can preserve its
current DTO and transport buckets.

The existing live path already has the required behavior:

1. The first `2H -> FT` poll is structural and emits `fixture.update`.
2. The browser refetches and classifies `FT` as `finished`, even though the
   Found Footy process state remains `active` during grace.
3. The later `active -> completed` transition emits another
   `fixture.update`; presentation stays `finished` and ordering stays stable.
4. `event.video` remains independent and may refresh clips before or after the
   fixture's internal completion.

No Vedanta Systems runtime change is required. Its BFF comment and live-data
documentation should be updated with the new producer-side recency meaning,
and its ordering test should retain the invariant that process rebucketing does
not change presentation classification.

## Already-terminal ingestion boundary

Fresh fixtures first discovered in a terminal status currently take the
missed-match path: `Activate(kickoff)` followed by `Complete(now)`. They never
enter event reconciliation.

Changing that path to spend an hour in `active` would improve recovery for a
fixture missed during an outage, but it would also make an old manual ingest
hot-poll and potentially start searches for historical events. It has no
trustworthy first-terminal timestamp and could make an old fixture look newly
finished in recency ordering.

The first FF-063 implementation should therefore scope terminal grace to
fixtures that reached the active monitor before completion. Preserve the fresh
terminal ingest path. Design a bounded completed-fixture event/backfill policy
under [`FF-010`](../../todo.md#confirmed-and-mitigated-backlog) instead of
silently turning every historical ingest into an hour of hot work.

An existing staging fixture that was missed while the worker was down still
recovers: `ActivateUpcoming` promotes its past kickoff on worker recovery, then
the first successful active poll starts terminal grace.

## Affected implementation surface

| Surface | Required change |
|---|---|
| `internal/infra/pg/schema.sql` | Add nullable `terminal_observed_at`; correct stale winner/counter comments. Keep `completion_counter` temporarily for rollback compatibility. |
| `migrations/` | Add an idempotent additive migration and update the schema fingerprint. Do not backfill an observation time without a fresh provider response. |
| `internal/domain/fixture` | Add `TerminalObservedAt`; make `UpdateFromPoll` start, preserve, or clear it and remove the `completionVote` argument/counter behavior. |
| `internal/infra/pg/fixture_repo.go` | Scan and persist the new field; replace counter/parity eligibility with grace, event-settled, and downstream-settled predicates. Return enough inventory evidence for the completion audit. |
| `internal/activity/monitor` | Retain score-backed absence and identity guards; remove `completionVote`; emit the enriched completion evidence. |
| `internal/config` and worker composition | Add, validate, document, and inject `WORKFLOWS_TERMINAL_GRACE_PERIOD=1h`. The workflow itself does not need to read environment state. |
| `internal/api` | Derive recency from terminal observation, with the legacy completed-row fallback. No JSON field is added. |
| `internal/infra/event` | Enrich `FixtureCompletedPayload`; keep NATS as the existing ID-only dirty signal. |
| Scenario harness | Inject a grace period and add assertions for terminal observation and completion evidence. |
| Found Footy ledgers | Update monitor, API, orchestration, deployment/migration, observability, testing, and the landed decision in the same implementation change. |
| Vedanta Systems | No runtime contract change. Update BFF/live-data comments and retain presentation-order tests when the producer change lands. |

## Migration and rollout shape

This is an additive-first release:

1. Add nullable `terminal_observed_at` through a reviewed operational migration.
   The old binary ignores the column and continues to write
   `completion_counter`. Its older schema drift guard requires a deliberate
   restamp to the prior fingerprint before rollback startup; the additive
   column itself remains in place.
2. Deploy the new binary. On the first successful poll, an existing
   `active/terminal` fixture starts a real one-hour observation window.
3. Verify a coherent fixture, an incoherent fixture, a late event, completion
   audit evidence, and stable frontend ordering.
4. Remove `completion_counter` only in a later migration after the rollback
   window closes and durable environments converge. Fold that cleanup into
   [`FF-013`](../../todo.md#confirmed-and-mitigated-backlog), together with the
   already-unused `last_activity_at` column if safe.

The migration, application deployment, and any repair of existing production
rows remain separate production actions. Each requires its own approval.

## Regression matrix

The implementation is incomplete until tests cover:

- first terminal poll starts grace but does not complete;
- repeated terminal polls preserve the original timestamp;
- a successful non-terminal response clears it and a later terminal response
  starts a new window;
- a failed poll neither clears the timestamp nor completes the fixture;
- 59:59 is ineligible and 1:00:00 is eligible;
- an event at count 1 or 2 blocks completion after the hour;
- a stable event with open downstream work blocks completion;
- completion proceeds when downstream work closes;
- an unobserved goal completes after the hour with incoherent audit evidence;
- a provider-array omission preserves the stored goal and can complete with
  provider parity false but durable parity true;
- unknown-player placeholders do not block;
- `PEN` with missing/tied shootout data completes after grace but records the
  incomplete result;
- exceptional terminal statuses use the same grace and mark parity not
  applicable;
- an already-terminal fresh ingest retains its existing direct-complete path;
- legacy completed rows retain `completed_at` recency while new rows use
  `terminal_observed_at`;
- the existing FF-014, FF-055, FF-062, active-poll, retention, NATS, fleet
  reaper, and scenario suites remain green.

## Consequences and remaining limits

- One isolated fixture held for one extra hour adds at most 120 vendor batch
  calls at the default 30-second cadence. Terminal fixtures sharing a batch do
  not each add a call.
- The grace bounds provider inconsistency, not broken downstream work. A real
  open checklist row still blocks completion until its recovery contract
  resolves it.
- Completion ends event polling. A goal first published after the one-hour
  window remains a completed-fixture backfill problem under FF-010.
- Retention starts from the later `completed_at`, so its age threshold moves by
  the grace and any downstream delay. Public fixture date filtering remains
  kickoff-based.
- Keeping a terminal fixture in process state `active` for one hour does not
  keep it visually live. Vedanta Systems already separates process state from
  provider-status presentation.
