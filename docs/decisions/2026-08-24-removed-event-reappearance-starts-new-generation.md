# Removed event reappearance starts a new generation

## Context

The symmetric debounce model soft-removes an event generation when its presence
counter reaches zero. FF-027 then treated an exact later reappearance as the
same terminal tombstone. That assumed a removed event never returns in the
provider feed.

Production fixture `1550681` disproved the assumption. Baku's goal appeared,
disappeared long enough for its first generation to be removed, and later
returned in API-Football's score-coherent six-goal inventory. Reconciliation
mapped the returned evidence to the removed natural key and skipped it. The
database therefore retained only five active goals for a 0–6 result, so the
durable fixture-completion gate correctly refused to complete the fixture even
after the provider completion counter reached three.

Reviving the old row would erase the meaning of its removal, reconnect a
canceled Temporal lifecycle, and make already-revoked shares ambiguous. Ignoring
the returned evidence loses a real event and can hold the fixture active
forever.

Later production evidence qualified the incident without restoring the unsafe
contract. Before this decision deployed, API-Football corrected Baku's clock
from the removed generation's 45+2 to 45+1. FF-027 already treated that changed
clock as a non-exact event, allocated sequence 2, and completed the fixture at
04:20 UTC. The fixture therefore proves that a removed event can return and can
be suppressed while its identity stays exact; it is not natural validation of
this decision's exact-reappearance branch. The regression scenario remains the
deterministic proof, and a natural exact match is still required.

## Decision

Treat each post-removal reappearance as a new event generation:

1. Match current provider evidence only against active stored events.
2. Include active and removed history when calculating the group's maximum
   sequence. Never reuse a removed sequence.
3. Allocate unmatched returned evidence above that historical maximum. The new
   natural key and UUID enter the ordinary presence debounce at count one.
4. Trigger a new downstream EventWorkflow only after the new generation earns
   three presence votes.
5. Keep the removed row, its removal reason, revoked shares, deleted objects,
   and prior Temporal history unchanged.

`sequence` therefore means durable generation allocation order. It does not
mean the chronological ordinal of the event in the match.

## Consequences

- A real event can recover automatically while its fixture remains active and
  API-Football continues to report it.
- The provider must show the reappearance for three polls before discovery
  restarts, preserving the existing false-positive boundary.
- The old generation remains an auditable tombstone. No schema mutation or
  Temporal replay version is required because the change is inside a retried
  activity and creates new durable identities instead of changing an existing
  workflow history.
- Repeated disappear/reappear cycles can allocate further generations. That is
  deliberate: each terminal removal keeps its own teardown history.

## Superseded contract

This supersedes step 5 of
[the FF-027 sequence decision](./2026-08-17-event-sequences-match-stored-identity.md)
and the “event never returns” consequence in the
[archived symmetric-debounce decision](./archive-through-2026-08-16.md#2026-07-07--symmetric-counter-debounce-go-rebuilds-improvement-over-python).
It does not change active-row matching, score-backed absence holds, the
three-vote debounce threshold, VAR teardown, or fixture completion gates.
