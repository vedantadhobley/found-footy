# Event sequences match stored identity instead of provider array position

## Context

The event natural key remains
`{team_id}_{player_id}_{event_type}_{sequence}`. The archived Python system and
the initial Go rebuild assigned `sequence` from each provider response's array
position within a scorer/type group. That kept an event stable across a small
minute correction only while every earlier event remained present and ordered.

The premise fails for a same-player brace. If the first goal is removed, the
surviving second goal moves from sequence 2 to sequence 1 in the next response.
The reconciler then votes presence for the removed identity and absence for the
real survivor. Provider array reorder can similarly swap mutable fields between
the two durable rows. PostgreSQL keeps soft-removed rows as immutable natural-
key tombstones, so reusing a positional sequence can also collide forever.

## Decision

Keep the natural-key format and existing keys, but make sequence assignment a
durable reconciliation operation:

1. Read the fixture's complete event identity history, including soft-removed
   rows, in one repository query.
2. Group provider and stored events by team, player, and domain event type.
3. Sort both sides by effective match clock and compute an order-preserving,
   maximum-cardinality match. Prefer the least total clock correction, then
   matching detail. Active rows may match within five minutes so ordinary
   provider clock corrections keep their identity.
4. When the aggregate score proves a team's goal array is incomplete, require
   exact clock matching for that team's goals. A nearby new goal must not
   consume the identity of an omitted stored goal.
5. An exact clock-and-detail reappearance of a removed row maps to its terminal
   tombstone and remains skipped under the existing removal contract.
6. Allocate every unmatched event above the maximum sequence in the complete
   active and removed group history.

The sequence now means durable allocation order, not a promise that sequence
numbers always reproduce chronological order after a late insertion. Event
UUIDs remain the internal identity, natural keys remain immutable, and no
schema or Temporal history change is required.

## Consequences

Removing either goal from a brace cannot renumber the survivor. Reordered
provider arrays cannot swap stored rows. A later same-player event allocates a
new key above every tombstone instead of colliding with one. The complete-
history query also makes the existing removed-key guard real; the prior helper
claimed to read removed keys but called `ListPending`, which excluded them.

API-Football does not provide an event ID, so two same-player events with the
same type, clock, and detail remain intrinsically ambiguous. The matcher uses
score coherence to avoid the observed omission class and otherwise preserves
the closest durable identity. If live evidence exceeds that boundary, the
natural key will need an additional provider-independent identity signal rather
than a wider clock heuristic.

## Superseded contract

This supersedes the historical plan and Python behavior that treated provider
array position as stable sequence identity. It does not change scorer
refinement, three-vote debounce, score-backed absence protection, or terminal
VAR teardown.
