# Garage syncing and stopped metadata recovery

Found Footy's single-node Garage must sync metadata and block files. A
reachable storage process is not proof that its metadata or media is intact.

## Context

After the October 3 abrupt host shutdown, Garage v1.0.1 remained reachable
but eight internal SQLite tables were corrupt. Its default metadata syncing
was disabled, which selects WAL with `synchronous=OFF`. Data syncing was also
disabled. The shutdown is a plausible trigger; the physical cause was not
proved. This SQLite database is Garage metadata, not application PostgreSQL.

## Decision

The shared, secret-free `garage.toml` sets `metadata_fsync = true` and
`data_fsync = true` for both environments. Garage v1.0.1 then selects SQLite
WAL with `synchronous=NORMAL` and syncs block writes. This preserves database
consistency through power loss; it does not guarantee every recent metadata
commit survives, provide replication, or replace backups.

Corrupt metadata recovery requires an explicitly approved storage cutover.
Stop the writer, preserve the complete old metadata and journals, and capture
a fresh coherent database. Preserve every readable non-derived key/value
pair. For the October 5 corruption, only the three derived Merkle trees and
their queues were reset, then seeded with the exact version's value hash.
Reindexing alone, a restart, or peer resync on a replication-factor-one node
cannot substitute for that reconstruction.

Keep the existing node identity, cluster layout, bucket, S3 application key,
and object paths. Do not install an older rehearsal snapshot into a store
that continued accepting writes. Use the exact installed Garage image; a
storage repair is not an application release or PostgreSQL migration.

Acceptance requires usable statistics, drained Merkle queues, SQLite integrity
including committed WAL, complete checked retained-media reads, and retained
logs. Keep rollback evidence private because it includes server and S3 keys.
Never expose credentials through CLI help or signed-request diagnostics.

## Consequences

Syncing can increase write latency. The consistency requirement takes
precedence over the previous unsynced default. No memory budget, model call,
event identity, NATS contract, or application image changes.

The October 5 cutover rotated only Garage's internal RPC, admin, and metrics
tokens after CLI help exposed inherited values. Application S3 credentials
remain unchanged, so API, workers, and the consumer do not require recreation.
The existing Prometheus configuration has no Garage token-dependent scrape.

Scheduled metadata snapshots, off-volume backups, and replication remain
separate operational work. This decision does not declare the single-node
store protected against disk loss or authorize future recovery mutations.

See the [recovery runbook](../operations/garage-recovery.md)
and [exact-version SQLite implementation](https://git.deuxfleurs.fr/Deuxfleurs/garage/src/tag/v1.0.1/src/db/sqlite_adapter.rs).
