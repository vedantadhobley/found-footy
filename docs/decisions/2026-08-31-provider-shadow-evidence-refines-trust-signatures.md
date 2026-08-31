# Provider shadow evidence refines trust signatures

## Context

The first FF-075 production shadow window proved the systemic thresholds but
also exposed false trips around period boundaries and ordinary provider
corrections. The exact evidence is preserved in the
[shadow audit](../design/audits/provider-integrity-shadow-2026-08-31.md).

The original shadow decision recognized only the first poll of a score-backed
goal cancellation. It did not define scorer replacement, phase-aware clock
progress, isolated rejection, or how typed contract failure survives a total
Temporal activity failure.

## Decision

- Clock regression compares within a playing phase. A forward phase transition
  and `ET ↔ BT` boundary may clear stoppage time without becoming regression.
- A score-backed goal cancellation remains supported through the existing
  partial absence debounce while score, event inventory, and correction age
  stay coherent. No second debounce is added.
- One player-attribution replacement is supported only when it is the sole
  unmatched old/new pair and team, event type, detail, clock, and unchanged
  score agree.
- Fixture identity conflict uses `rejected` for that fixture. It does not reject
  unrelated fixtures unless the existing systemic batch thresholds trip.
- By-ID fetch results preserve bounded `transport` versus `contract` evidence.
  A total fetch failure remains a retryable activity error and carries that
  evidence in Temporal error details.

All policies remain advisory until FF-075's durable enforcement phase.

## Consequences

- Normal halftime and extra-time boundaries no longer pollute shadow alerts.
- A legitimate correction can complete the existing three-vote removal instead
  of becoming quarantined after its first vote.
- Provider scorer refinements remain destructive only under a narrow positive
  replacement signature.
- The future circuit can distinguish malformed successful responses from
  downtime without parsing error strings or bypassing Temporal retries.
