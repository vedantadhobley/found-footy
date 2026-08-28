# Accepted candidates commit as one placement

## Context

The accepted-candidate path changed public ranking inputs through several
independent activities. Promotion inserted an asset and share and then rewrote
stored ranks. Exact or perceptual duplicates incremented popularity without
rewriting ranks or publishing `event.video`. Candidate terminal outcome was a
separate write again. A retry could therefore count the same source twice, and
an otherwise successful write sequence could leave attribution, popularity,
share membership, rank, and frontend invalidation describing different states.

A production read audit found ten shares across five events whose stored order
no longer matched their current verified/popularity/size evidence. Thauvin's
clips, for example, retained stored order 22, 2, 5 instead of 22, 5, 2. The
defect exposed two older accepted issues: FF-011's retry-unsafe popularity
increment and FF-048's check-then-insert share identity.

The 2026-07-18 decision had already selected read-derived rank because rank is
a projection, not durable source data. The shipped schema and persistence path
later reintroduced a stored rank and its invalidation problem.

## Decision

New EventWorkflow histories treat one accepted candidate cluster as one
placement.

1. `CommitClipPlacement` owns candidate terminal outcome and asset attribution,
   popularity credit, asset/share creation, and loser supersession in one
   Postgres transaction. An event-row lock serializes placements for the same
   event.
2. `(event_id, tweet_url)` is the source-vote idempotency key. The new nullable
   `credited_asset_id` records the asset that received the vote. Popularity
   increases only when that candidate did not already have a credited terminal
   outcome. Supersession moves attribution to the winner and merges the loser's
   aggregate popularity exactly once.
3. `(event_id, asset_id)` is unique in `video_shares`. Placement uses an atomic
   conflict-safe insert instead of check-then-insert.
4. Public reads derive rank with `ROW_NUMBER()` from current active membership,
   timestamp verification, popularity, file size, creation time, and share ID.
   No new-history write recalculates rank.
5. Every successful placement publishes `event.video`, whether it changed
   public membership or only a ranking input. Consumers refetch the derived
   view, so repeated invalidations are harmless.
6. S3 copy precedes the database transaction for a new asset. Idempotent
   staging and superseded-object cleanup follow it as the activity's durable
   retry tail. A deterministic asset row proves the destination copy already
   completed.
7. Change ID `ff-066-atomic-clip-placement`, version 1, selects the new command
   sequence. Older histories retain `PromoteAndPersist`,
   `BumpAssetPopularity`, `SupersedeAssets`, and stored-rank rebalance commands
   for Temporal replay.

The `video_shares.rank` column and its active-rank index remain temporarily so
old histories can replay. New placements append a collision-free compatibility
value, but public reads ignore it. Remove both only after no retained history
can issue the old command sequence.

## Consequences

- Retry cannot double-count a candidate source or mint a second share for the
  same event/asset.
- A successful placement has one explainable database state: every accepted
  candidate names the live winner whose popularity includes it.
- Popularity-only duplicates immediately reorder the next API read and emit the
  same dirty signal as asset promotion or supersession.
- The database transaction cannot include Garage. The deterministic copy and
  cleanup tail retain the existing cross-system retry boundary.
- Historical rows lack complete candidate-to-asset attribution, so deployment
  does not rewrite popularity or infer old votes. Existing stale public order
  disappears because the API no longer reads stored rank.

This decision restores the public-ranking contract from the frozen
[read-derived rank decision](./archive-through-2026-08-16.md#2026-07-18--video-share-ranking-derived-at-read-time-no-stored-rank-column)
and supersedes the rank-rebalance portion of
[promotion retry repair](./2026-08-16-promotion-retries-complete-durable-tail.md)
for new histories. It leaves that older path intact only for replay.
