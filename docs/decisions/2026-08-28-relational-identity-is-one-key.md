# Relational identity is one key

## Context

Candidates, assets, and shares stored event and fixture identifiers for useful
queries and object paths. Independent foreign keys proved that each referenced
row existed, but not that the identifiers described the same aggregate. A row
could therefore point to a real event and a different real fixture, or credit a
real asset from another event, without violating the schema.

Application guards covered normal write paths. They did not protect manual
repair, an old Temporal history, a future repository, or a partial retry that
reached SQL through another path.

## Decision

Postgres owns aggregate identity through correlated keys:

- assets reference `(event_id, fixture_id)` on one event;
- shares reference `(asset_id, event_id)` on one asset;
- candidates reference `(event_id, fixture_id)` and any credited asset through
  `(credited_asset_id, event_id, fixture_id)`;
- supersession references a successor in the same event and fixture.

The schema also names and enforces complete removed-state pairs, stored-media
shape, exact digest/hash encoding, positive popularity, non-negative candidate
measurements, and the no-self-supersession rule. Domain validation mirrors
single-row value and state rules. Cross-row truth remains database-owned.

The ordered migration preflights all existing rows. It aborts the transaction
instead of guessing how to repair inconsistent historical identity or missing
timestamps. New clip placement seeds popularity at one, then adds only the
remaining newly credited votes in the same transaction.

## Consequences

- Every write path, including old histories and operator SQL, receives the
  same aggregate-identity enforcement.
- Composite reference targets add small redundant unique indexes beside UUID
  primary keys. Their write cost buys declarative correlated foreign keys and
  is accepted for these low-volume tables.
- An inconsistent durable environment requires an explicit reviewed data
  repair before migration; application rollout cannot conceal it.
- Event deletion and share URL retention keep their prior cascade/restrict
  behavior. Deleting a supersession winner clears only `superseded_by`.
