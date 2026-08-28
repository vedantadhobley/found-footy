# Fixture writers own columns and observation order

## Context

Ingest, staging polling, active polling, activation, and completion all wrote a
whole fixture through one generic upsert. Each caller first read a row, mutated
an in-memory copy, and wrote every column back. A delayed response could
therefore regress newer provider state or overwrite a concurrent lifecycle
transition. Active and staging polling also omitted corrected team display,
league, and kickoff fields from their in-memory snapshots.

FF-070 made lifecycle state plus required audit evidence transactional. It did
not define ownership for ordinary provider refreshes or prevent an older
response from winning by commit order.

## Decision

The fixture repository exposes commands by write intent:

- `StoreFromIngest` inserts a fixture in its derived initial state. On conflict
  it applies all provider-owned fields only when the incoming `last_polled_at`
  is newer than the stored value. Equality is an idempotent no-op and gives a
  simultaneous active/staging poll precedence. It never updates lifecycle
  state or transition timestamps.
- `RefreshActivePoll` and `RefreshStagingPoll` require their named state and the
  same monotonic observation check. They persist the full provider metadata
  available to that poll. A false result is a stale write, so callers do not
  derive event votes or publications from it.
- `TransitionWithAudit` locks state and `last_polled_at`, requires the expected
  source state and a non-stale observation, updates only transition-owned
  fields, and commits the required audit record in the same transaction.

`last_polled_at` is the fixture's provider-observation version. Ingest and active
poll workflows fix it at cycle start, before their provider request, so response
latency and activity retry cannot make an older request outrank a newer cycle.
Staging poll fixes the same value before its in-activity request. Equal poll
refreshes are accepted for activity retry; equal ingest conflicts succeed as
no-ops. The concrete strict `Insert` primitive remains outside the domain
repository interface for
integration setup and the repository smoke test; production callers do not
receive a generic full-row write.

The existing direct-birth behavior remains: a newly discovered live fixture
may insert as active, and a newly discovered historical terminal fixture may
insert as completed. Those are initial states, not transitions of an existing
durable row.

## Consequences

- Response completion order cannot regress provider state, and no provider
  refresh can revive or demote a fixture.
- Competing activation/completion attempts produce at most one state change and
  one audit row. A stale transition waits for a later poll.
- Corrected active metadata emits a structural invalidation and reaches the
  public read model without waiting for the next daily ingest.
- The Temporal activity names and workflow command graph do not change.
- This is a repository/query contract change. It requires no schema migration
  or production data rewrite.
