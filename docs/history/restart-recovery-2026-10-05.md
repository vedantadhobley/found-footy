# October restart recovery

The October 3 host shutdown exposed independent inference-routing, Garage
metadata, and browser-recovery failures. The approved worker recovery on
October 3 and storage recovery on October 5 completed without changing the
application release. The browser retry fix remains a separate consumer rollout.

## FF-103 — local inference resolution depended on Tailscale boot state

**Status:** closed. Both worker replicas recovered on October 3 at 18:04:39 UTC
while retaining image `626469349dcddf1d1441df1f7096a219b3f554a9`.

After the host restart, workers repeatedly exited on DNS resolution of
`control-joi.luv`. Tailscale's startup arguments omitted its saved compute-subnet
advertisement. The gateway and all five models were ready, but the mandatory
model probe prevented worker startup, including fixture polling.

Both worker Compose services now map the canonical hostname to Docker's
`host-gateway`. A disposable container on the application network reached
`/ready` and `/v1/models` without Tailscale. Both Compose configurations passed
validation. Only the approved production workers were recreated; other Found
Footy and Control containers retained their identities. Tailscale was repaired
independently in its owning session.

Both workers connected to Gemma 12B and Temporal and remained running without
restarts. Polling advanced to 18:05:30 UTC, natural event searches returned
rendered timelines, and queued candidates reached promoted, duplicate, and
terminal reject outcomes. The static fallback's older reboot-time auth warning
did not prevent independent event browsers from processing clips.

See the [local inference routing decision](../decisions/2026-10-03-local-inference-does-not-require-tailscale.md).

## FF-104 — Garage metadata corruption after the October 3 shutdown

**Status:** closed. The approved storage-only recovery captured fresh stopped
metadata on October 5 at 23:15:42 UTC and restarted Garage at 23:15:50 UTC.

### Diagnosis

Garage v1.0.1 remained reachable but emitted `database disk image is malformed`.
A read-only SQLite transaction found corruption in eight of 62 metadata tables:
object/version lookup indexes, block references, and three derived Merkle trees.
`garage status` showed a reachable node; `garage stats` failed.

Raw-versus-indexed object lookups found 29 unreachable staging entries. None
matched a retained application asset. Six complete public playback downloads,
three predating the reboot and three recent, matched PostgreSQL size and MD5.
New uploads continued after the incident. These checks do not establish that
no earlier staging candidate was lost.

Both syncing settings were omitted. Exact v1.0.1 source defaults them to false
and selects SQLite WAL with `synchronous=OFF`. The abrupt shutdown is a plausible
trigger, not a proved physical cause. No Garage metadata snapshot directory
existed, and current-boot kernel checks found no disk/filesystem errors.

### Isolated rehearsal

Reindexing alone did not resolve corruption. A coherent capture at 19:16:48 UTC
was logically reconstructed using the exact production image, preserving all
56 non-reset table digests. Only the three derived Merkle trees and their queues
were reset and reseeded with the exact version's value-hash algorithm.
The 7,349 existing TODO records independently confirmed that algorithm.

The isolated server had no peers or exposed ports, and production blocks were
mounted read-only. Merkle queues drained, statistics succeeded, and the stopped
database passed integrity checks with its committed WAL included. Every one of
2,354 unreclaimed pre-capture assets matched its full size and MD5:
25,776,879,704 bytes, including hidden variants. Temporary servers were removed.
Read-only block-cleanup errors were expected. The first rehearsal also counted
four other error lines whose raw text was not retained; their causes remain
unclassified. A second isolated boot had no error lines in its retained log,
usable statistics, empty Merkle queues, and passing stopped-state integrity.
The media audit does not erase the first rehearsal's runtime-log evidence gap.

### Production acceptance

Recovery used a fresh stopped capture, not the older rehearsal snapshot. All
56 non-reset table digests and node/layout state were preserved. Only Garage's
container changed; application images, PostgreSQL, application S3 credentials,
and object paths remained unchanged.

Live SQLite integrity passed. Merkle and block-resync queues drained, inherited
block-resync errors cleared, and retained post-cutover logs contained no error
lines. All 2,406 unreclaimed pre-cutover assets matched their complete size and
MD5: 26,073,563,809 bytes. Six existing API playback links also matched. API,
both workers, Twitter, and the operator UI responded, and fixture polling
continued. Read-only October 9 statistics still showed empty Merkle/resync
queues and no block-resync errors.

The shared configuration now enables metadata and block syncing. Garage's three
internal server tokens were rotated after top-level CLI help exposed inherited
values; application S3 credentials were not rotated. Original metadata,
journals, configuration, dotenv, reconstruction scripts, manifests, and logs
remain in private rollback storage under the project's data root. The precise
location and safety boundary are in the
[recovery runbook](../operations/garage-recovery.md).

See the [syncing and recovery decision](../decisions/2026-10-05-garage-syncing-and-recovery.md)
and [exact-version SQLite adapter](https://git.deuxfleurs.fr/Deuxfleurs/garage/src/tag/v1.0.1/src/db/sqlite_adapter.rs).
Syncing and this repair do not provide replication, scheduled backups, or proof
that no earlier staging candidate was lost.
