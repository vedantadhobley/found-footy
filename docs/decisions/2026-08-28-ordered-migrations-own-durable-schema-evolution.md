# Ordered migrations own durable schema evolution

## Context

The flat `schema.sql` fingerprint detected an unapplied file edit but did not
prove that required objects existed. A crashed historical initialization or a
manually partial `ALTER` could still be stamped as current. Each post-cutover
change also required hand-running SQL and hand-copying the next schema hash.

FF-071 needs cross-table constraints and preflight checks. Adding them through
another one-off hash boundary would extend the same weakness.

## Decision

`schema.sql` remains the fresh-install snapshot. Every durable change also adds
one immutable, ordered SQL file under `migrations/`; its first line names the
resulting snapshot hash.

The dedicated `cmd/migrate` process validates the chain and applies all pending
files plus their ledger rows in one Postgres transaction under an advisory
lock. A required-object manifest gates adoption and final commit. Applied
checksums and target hashes are immutable, and a database newer than the binary
fails closed.

Worker and API use a read-only verification path. They wait on a shared
advisory lock, then require the exact ledger, schema stamp, and object manifest.
They never apply DDL. Dev Compose runs the migration process as a one-shot
dependency. Production keeps `make migrate-prod` and `make deploy-prod` as
separately approved actions.

## Consequences

- Interrupted DDL and its ledger record roll back together.
- An incomplete historical baseline refuses adoption; an idempotent partial
  additive migration can complete inside the transaction.
- Once a durable environment records a migration, that file is never edited,
  deleted, or flattened away.
- Transaction control and concurrent indexes are forbidden inside migration
  files because the runner owns the transaction.
- Rollback binaries must understand every live ledger row or fail closed.

## Superseded contract

This supersedes the archived choice of a
[schema drift guard without ordered migrations](./archive-through-2026-08-16.md#2026-08-13--schema-drift-guard-not-migration-files).
