# Exact variants follow the live canonical asset

## Context

Video asset identity is deterministic on `(event_id, md5)`. Perceptual dedup
can later supersede that asset with a better clip. The retired row remains
durable, but failed-execution recovery historically restored only active
assets. Rediscovering the retired bytes therefore looked new and derived the
same retired asset ID. Atomic placement then failed correctly because its
winner must be live.

The same missing identity relation made supersession history unsafe to traverse
without a cycle check. Production contains one three-node cycle created by the
pre-FF-066 compatibility path. That quality-order defect is tracked separately
as FF-081.

## Decision

Supersession defines canonical identity for every persisted exact-byte variant
represented in an event's active dedup set. Each such MD5 resolves through
`superseded_by` to one terminal live asset in the same event.

`LoadEventAssets` restores both the live perceptual-dedup set and a map from all
eligible persisted MD5 variants to their live root. Recovery rejects a cycle,
cross-event edge, or missing asset. A chain without a live public root remains
outside the active dedup set. During a workflow execution, every successful
supersession redirects the losers' aliases to the selected winner.

A recurring known variant bypasses repeated dense hashing and vision because
that exact persisted asset already passed the accepted-candidate path. It still
uses `CommitClipPlacement` against the live root. Candidate attribution,
popularity, derived rank, staging cleanup, and `event.video` invalidation keep
their existing atomic contract.

The Postgres rule that an existing winner must be live remains unchanged. The
workflow command change uses Temporal marker
`ff-080-canonical-exact-alias`; older histories keep their recorded behavior.

## Consequences

- A retired deterministic asset is never recreated or reactivated by an exact
  recurrence.
- Replacement executions retain exact-dedup knowledge that previously vanished
  with the public share.
- Recovery performs cached asset reads proportional to the event's durable
  share history. Events have small bounded sets, so this does not justify a new
  table or denormalized root column.
- A legacy cycle fails recovery explicitly instead of looping or choosing an
  arbitrary root. Repairing existing production rows remains a separate,
  approved data operation.
- This decision does not choose a new quality comparator. FF-081 owns the
  production-derived total-order design and its corpus validation.
