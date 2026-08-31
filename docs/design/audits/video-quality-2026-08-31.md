# Video keeper quality and dHash graph audit — 2026-08-31

This is the production-derived research checkpoint for FF-081. It is evidence,
not a new keeper policy. The repeatable read-only audit lives in
[`scripts/audit_video_quality/`](../../../scripts/audit_video_quality/).

## Scope and method

The audit exported retained asset metadata, dense frame hashes, share state,
and supersession edges in a read-only Postgres transaction. Analysis then ran
offline. It rebuilt each `(event, timestamp_verified, hash_version)` graph with
the current 12/30/3 and 16/50/5 match policies, added historical supersession
edges that no longer pass the current matcher, and replayed arrival orders.
Components up to eight members were exhaustive; larger components received
100,000 deterministic permutations.

The retained corpus contains 1,815 assets across 584 events and 772 comparison
pools. Clips rejected before asset creation are not represented. Frame rate is
also absent because `DownloadAndStage` returns it but workflow and asset
persistence discard it.

## Results

- 1,051 current match edges form 376 reconstructed components containing 1,075
  assets. The retained graph has 675 supersession edges; 14 no longer pass the
  current matcher and none cross a comparison pool.
- One component contains a quality-comparator cycle and one persisted
  supersession cycle: K. Danso 67′ in Tottenham–Charlton. Both are the same
  three legacy assets. A later 62.159 s active asset directly matches all three
  and wins every replay order, so the cycle does not control the current public
  keeper.
- Fifty-four components contain a non-transitive dHash bridge. Twelve produce
  more than one final live set under different arrival orders. Replacing the
  pairwise fold with component-anchored 15% duration and 10% density bands
  leaves the same twelve topology-sensitive components; set-level bands are
  still non-associative when retired members leave the live set.
- Applying the anchored bands to each complete retained component selects an
  existing terminal asset in all 376 cases. Exact lexicographic duration first
  misses the terminal set in 38 components. Fixed logarithmic bands disagree
  with the anchored winner in 17 components because fixed bucket boundaries do
  not reproduce pair-relative thresholds.
- Simple total scores also change substantial retained behavior. A
  `duration × density` order reverses 27 acyclic supersession edges and chooses
  a non-terminal component winner 17 times. A bounded grid search over
  log-duration, density, and resolution weights still reverses 11 acyclic
  edges and misses five terminal sets. This is diagnostic fitting, not evidence
  that the fitted weights are a sound product policy.

## Visual review of bridge components

The graph is not an equivalence relation, so connected-component consolidation
is unsafe.

- Bellingham 9′ contains a short true subset of the 61 s canonical clip. The
  bridge leak leaves one redundant public clip.
- Fermin 71′, Saka 59′, and Isak 60′ contain alternate encodes or overlays of
  the same scoring sequence. Their extra roots are plausible dedup misses.
- Raphinha 14′ contains the goal, a different buildup/broadcast sequence, and
  a celebration-only cut. Collapsing the whole connected component would
  discard distinct presentations.
- Raphinha 67′ includes a bench-reaction clip; Marquinhos 90+5′ includes a
  post-match interview. Shared broadcast segments connect them indirectly to
  goal footage, but their real defect is FF-003 exact-event semantics, not
  keeper ordering.

## Conclusion

Do not add a durable perceptual `cluster_id` or treat dHash connected
components as one clip. That would make the current safe failure—an occasional
extra video—into false consolidation of distinct footage.

Do not replace `IsUpgrade` with strict, fixed-bucket, or fitted scalar ordering
from this corpus alone. Each candidate measurably changes retained keepers, and
the corpus has no human quality labels for retired bytes. FF-066 already
prevents a retired asset from becoming a new placement winner, so the observed
cycle is legacy data rather than a currently reproducible persistence path.

The next valid quality-policy experiment needs reviewed pair labels and the
missing cadence/presentation evidence. The legacy Danso cycle can be repaired
separately by pointing its three retired assets at the clear active winner,
after explicit production-data approval.
