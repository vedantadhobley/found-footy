# Postponed score absence is not play regression

## Context

The second FF-075 shadow window repeatedly compared Cincinnati–DC United's
retained 0–0 scoreboard against fresh null scores. Both observations were PST,
with no clock or events. `UpdateFromPoll` retains scores when the provider
sends null, so this produced the same `populated_field_cleared` warning every
poll. It could combine with one unrelated anomaly to meet the two-fixture
global threshold. The [audit](../design/audits/pre-rollout-evidence-2026-09-08.md#ff-088-and-ff-075-postponed-polling-pollutes-circuit-evidence)
records the exact comparison and the separate polling-lifecycle issue.

## Decision

Ignore only score-field clearing when both stored and observed facts:

- have the unchanged `pst` status and are non-terminal;
- have no elapsed or extra clock field, including an explicit zero;
- have only zero or null aggregate scores; and
- have no event evidence.

Event evidence includes all retained event rows, even pending, anonymous, or
removed rows, and every raw provider event, even an untracked yellow card.
Monitor carries a `HasEvents` fact for each side before filtering the lists
used for confirmed-event matching. Those lists also disqualify the exception
when present. No extra query or persistent column is needed.

This does not classify every postponed fixture as trusted. Identity conflicts,
cleared names, nonzero-score loss, missing confirmed events, and clock or phase
regression retain their existing rules. Entering/leaving PST, other deferred
statuses, and any observation with play evidence do not use this exception.
Zero and null are not globally interchangeable, and canonical score storage
does not change.

The batch thresholds remain two anomalous fixtures or three missing confirmed
events. Removing a false fixture warning leaves an unrelated anomaly isolated;
two genuine anomalies still meet the global threshold.

## Consequences

- Repeated empty PST scoreboards no longer pollute shadow recommendations.
- The change remains advisory. It does not enable the circuit, quarantine
  fixtures, suppress reconciliation, or change discovery/cleanup behavior.
- No schema, wire payload, schedule, or workflow command changes are required.
  Historical activity results retain their recorded verdicts on replay.
- FF-088 still owns bounded postponed polling and reactivation. This change
  does not release a long-postponed fixture from active polling.
- FF-075 still needs durable causal state and mutation enforcement. Current
  facts cannot prove that a fixture has never played if older evidence has
  already vanished; this narrow signature does not replace that future state.

Regression coverage pins the recorded Cincinnati comparison, repeated Monitor
refreshes, the two-fixture false trip, retained/raw event evidence, and the
negative boundaries. The first shadow corpus remains covered unchanged.
