# Terminal observation grace bounds fixture completion

## Context

The score-backed completion contract prevented a transient API-Football event
omission from destroying a real goal or completing an incoherent fixture. It
also required permanent score/event parity. Zaragoza–Athletic fixture
`1607295` showed the failure boundary: API-Football permanently returned a 3–1
terminal result with no events, so the fixture stayed active and was polled
every 30 seconds for days. More polling could not create missing upstream data.

Fixture completion is an internal monitoring-retirement transition. The
frontend already derives finished presentation from provider status, and event
debounce plus the durable downstream checklist are the work-safety gates.

## Decision

Persist `fixtures.terminal_observed_at` as the start of the current
uninterrupted run of successful terminal polls. The first terminal poll sets
it, later terminal polls preserve it, and a successful non-terminal poll clears
it. Failed or missing responses leave it unchanged but cannot complete the
fixture.

`WORKFLOWS_TERMINAL_GRACE_PERIOD` defaults to one hour. On a fresh successful
terminal reconcile after that boundary, `AssessCompletion` permits
active-to-completed only when no surviving named event is mid-debounce and no
registered downstream workflow remains open. Unknown-player placeholders stay
non-blocking. No new fixture state or Temporal workflow is added.

Provider score/event parity, durable surviving-goal parity, and nullable `PEN`
decision state move from permanent gates to `fixture.completed` audit evidence.
Score still prevents destructive absence votes when an aggregate result
requires an omitted goal, and it still tightens identity matching for an
incomplete array. Found Footy never fabricates missing events.

Public recency uses terminal observation instead of the later retirement time.
Legacy and fresh historical completed rows without an observation fall back to
`completed_at`. Fresh fixtures first ingested in a terminal state retain their
existing direct `Activate(kickoff)` then `Complete(now)` path; FF-010 owns any
historical event backfill.

The schema rollout is additive. `completion_counter` remains for one rollback
window but the new binary does not read or write it. FF-013 owns its later
removal after durable environments converge. The prior binary is SQL-compatible
with the added nullable column, but its schema drift guard requires a deliberate
restamp to the prior fingerprint before rollback startup.

## Consequences

- Permanently incomplete terminal fixtures have a bounded polling lifetime.
- A late event near the grace boundary still blocks retirement until its own
  debounce and downstream work settle.
- Completed time now means monitoring retirement, not final whistle. Retention
  starts from that later timestamp.
- Vedanta Systems needs no DTO or presentation-state change. Stable recency
  prevents the active-to-completed rebucket from reordering an already-finished
  match.
- An event first published after grace remains a completed-fixture repair case
  under FF-010.

## Superseded contract

This decision supersedes only the fixture-completion portions of
[score-backed goal removal](./2026-08-16-score-backed-goal-removal.md) and the
`PEN` completion gate in
[winner-state derivation](./2026-08-19-winner-state-is-derived-from-canonical-scores.md).
Their goal-removal and result-display rules remain active. It also supersedes
the counter boundary in the historical
[fixture completion proposal](../design/proposals/completion-contract.md).
