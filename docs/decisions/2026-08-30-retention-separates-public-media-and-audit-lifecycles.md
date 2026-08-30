# Retention separates public, media, and audit lifecycles

## Context

The daily ingest workflow used one elapsed-day threshold for two unrelated
decisions. It hard-deleted completed fixtures without shares, cascading through
their event and candidate evidence, while events with shares survived as
tombstones after their objects were removed. This biased durable history toward
successful discovery. Reclamation also used non-removed shares as its worklist,
so a delete failure after share revocation could disappear from later runs.

The unfiltered REST snapshot independently read every retained completed row
and assembled child resources one fixture and event at a time. Preserving audit
history would therefore make the public response and query count grow without
bound.

## Decision

Retention has three independent contracts:

1. The public fixture window contains every staging and active fixture plus the
   newest configured number of distinct UTC kickoff dates that contain
   completed fixtures. `PUBLIC_HISTORY_COMPLETED_FIXTURE_DATES` owns the count
   and defaults to 14. Snapshot and search queries compute their cutoff in the
   same SQL statement that selects fixtures. Targeted ID reads can still reach
   older retained rows.
2. Garage objects remain while their fixture is inside that public window.
   Daily ingest asks `retention.PlanMediaRetention` for events with unreclaimed
   assets below the same cutoff. The existing Temporal activity name
   `DestroyEvent` remains stable: policy retention revokes shares, deletes each
   object idempotently, and records `video_assets.object_reclaimed_at` only
   after a successful delete. Failed or interrupted assets remain on later
   worklists regardless of share state. Direct share resolution also treats a
   reclaimed terminal object as removed, so it cannot presign known-missing
   bytes across a concurrent cleanup boundary.
3. Routine retention never deletes fixtures, events, candidates, workflow
   records, validation evidence, assets, or shares. Removed shares remain `410`
   tombstones. Any future SQL archival or deletion policy requires measured
   growth and a separate decision.

The shared history count is execution-time configuration used by the API and
worker. It is not serialized into the create-only Temporal schedule. Existing
schedule payloads that contain the removed `RetentionDays` field remain
decodable because unknown fields are ignored.

Public fixture assembly uses four bounded batch reads: fixtures, events,
discovery completion, and visible clips. It does not issue per-fixture or
per-event child queries.

## Consequences

- Failure evidence and empty fixtures remain auditable after leaving the
  public site.
- Media storage is bounded by the same product-visible date window, with a
  durable retry boundary for partial Garage failures.
- PostgreSQL growth is intentionally not bounded by this policy. FF-074 owns a
  measured schema and archival audit before any narrower expiration policy.
- Previously deleted fixture hierarchies are not reconstructed by this change.
- The migration adds one nullable timestamp, one check constraint, and the
  indexes used by completed-window and unreclaimed-asset reads.

## Superseded contract

This supersedes the hard-delete and live-share worklist portions of the
[2026-08-11 retention decision](./archive-through-2026-08-16.md#2026-08-11--retention-revises-url-stability-reclaim-bytes-keep-410-tombstones-176).
Its permanent-share and `410` URL guarantees remain.
