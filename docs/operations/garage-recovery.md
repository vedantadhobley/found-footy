# Garage metadata diagnosis and recovery

Garage's internal SQLite metadata is separate from application PostgreSQL.
A reachable node in `garage status` does not prove metadata integrity. The
[October restart recovery](../history/restart-recovery-2026-10-05.md) records
the corruption, reconstruction, and accepted production cutover.

## Read-only diagnosis

Use a disposable, memory-capped diagnostic container with the metadata volume
mounted read-only. Open SQLite with `mode=ro` and `query_only`; bound integrity
checks and do not print key/value records. Read committed WAL data through
SQLite, not by copying only `db.sqlite` from a running instance. Never run
`REINDEX`, `VACUUM`, a logical rewrite, or Garage repair against production
as a diagnosis.

Garage CLI help can echo inherited environment secrets. For help inspection,
clear `GARAGE_ADMIN_TOKEN`, `GARAGE_METRICS_TOKEN`, and `GARAGE_RPC_SECRET` in
the diagnostic exec. Never dump container environment blocks or raw signed
request logs.

## Durability settings

Garage v1.0.1 defaults `metadata_fsync` and `data_fsync` to false. The shared
configuration explicitly enables both since the October 5 recovery. Disabled
metadata syncing selects SQLite WAL with `synchronous=OFF`; enabled syncing
uses `synchronous=NORMAL`. This protects consistency, not every recent commit,
and does not replace backups. Changing these settings requires production
approval. See the [syncing decision](../decisions/2026-10-05-garage-syncing-and-recovery.md).

## Isolated reconstruction

Use the exact production image with no peers or published ports. Write only
temporary metadata; mount production blocks read-only. Public GET signatures
can be sent privately into that namespace with the original Host header;
never print their URLs.

Preserve every readable non-derived key/value pair. Unexpected schema,
duplicate primary keys, unreadable primary records, or value mismatches must
stop the operation. The reviewed October exception was three damaged derived
Merkle trees: `object`, `version`, and `block_ref`. Clear only those copied
trees and their copied TODO queues. Enqueue each primary record using v1.0.1's
exact value hash: BLAKE2b-512 truncated to 32 bytes, **not** BLAKE2b with a
32-byte digest size. Let the same Garage version regenerate the trees.

Startup alone is not acceptance. Require drained Merkle queues, usable
statistics, metadata integrity, retained runtime logs, and complete media reads
checked against expected size and MD5. Classify read-only block-cleanup errors
separately from missing or corrupted blocks. Do not make production blocks
writable to silence rehearsal errors or invoke a destructive scrub.

## Capture and cutover

Obtain explicit approval for the exact production operation. Stop Garage's
writer before final capture and replacement. Preserve its database, WAL,
identity/layout state, configuration, and dotenv privately. Do not deploy an
older rehearsal database after production has accepted newer objects: rebuild
from the fresh stopped capture and validate it.

A clean stop can leave committed WAL data. Final read-only integrity checks
must include existing WAL and SHM, not use `immutable=1` on `db.sqlite` alone.
Preserve journals or create a coherent SQLite backup before treating a database
file as standalone. Do not discard WAL merely because its writer exited.

Keep the application S3 key, node identity, cluster layout, bucket, and object
paths. Restart only the approved storage service. PostgreSQL schemas, event
identities, and application images do not need to change for metadata repair.
Token rotation and durability settings are explicit cutover actions, not
incidental diagnostics.

The October recovery evidence remains in the private
`~/workspace/data/found-footy/recovery/garage-20261005T231045Z/` directory.
It includes credentials and is not a public artifact. A metadata-only capture
is not a media backup: subsequent writes and block reclamation can make it
unsafe to restore without matching data blocks and a reviewed recovery plan.

See [production acceptance](../history/restart-recovery-2026-10-05.md#production-acceptance)
for the completed cutover. The consumer's
[snapshot-retry defect](../todo.md#ff-105--failed-browser-recovery-snapshots-are-not-retried-on-healthy-sse)
is separate; repairing storage does not repair browser recovery.
