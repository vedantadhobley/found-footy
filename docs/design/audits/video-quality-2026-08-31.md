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
also absent because workflow and asset persistence discarded the value at the
time of export.

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

## Prioritized human-review pass

The follow-up review covered the legacy quality cycle, all twelve
arrival-sensitive components, and five previously retained threshold-policy
comparisons. Dedup identity and quality preference were judged separately.
Cadence below was probed directly from the still-retained media; it was not
available in the production export.

Seven arrival-sensitive components still had two or more unreclaimed terminal
assets to inspect. The other five retained only one terminal asset because
supersession had already reclaimed the comparison bytes. The historical direct
edges therefore cannot all receive honest visual labels from this snapshot.

| Event | Dedup/semantic label | Quality label | Evidence |
|---|---|---|---|
| K. Danso 67′, Tottenham–Charlton | Legacy three-way cycle is visually unavailable | Unresolved for the three retired clips; the later 62.159 s active asset remains the safe winner | All three cycle members were reclaimed. Metadata alone must not invent a human label. |
| J. Bellingham 9′, Espanyol–Real Madrid | Collapse the 11.328 s subset into the 61.354 s canonical sequence | `cde279c4` | Complete goal sequence and 60 fps versus a short 50 fps subset. The two active roots do not directly match; this is a topology false negative. |
| Raphinha 14′, Elche–Barcelona | Keep the 54.288 s DAZN and 33.240 s Movistar presentations separate | Not applicable across presentations | Different buildup, broadcast, and edit. Both are 50 fps; the Movistar copy is 1080p with materially more encoding budget. The 5.248 s celebration-only root is an FF-003 semantic survivor, not a cluster member to consolidate blindly. |
| Fermin 71′, Elche–Barcelona | Collapse the 15.116 s cut into the 63.320 s sequence | `0eba4533` | Complete 1080p broadcast at 29.97 fps versus a user-confirmed display recording at 59.97 fps. Cadence does not compensate for presentation and completeness loss. |
| Raphinha 67′, Elche–Barcelona | Keep the goal and bench reaction out of one transitive cluster; reject the reaction semantically | `9ff6caff` is the only valid goal clip | The 63.785 s goal is 50 fps. The 5.674 s 60 fps root shows only a player on the bench and has very low encoding density. |
| B. Saka 59′, Aston Villa–Arsenal | Collapse the alternate-overlay copy | `73016217` | Same master scoring sequence and 29.97 fps cadence; 49.110 s is more complete than 19.923 s at nearly equal per-frame compression. |
| Marquinhos 90+5′, Lille–PSG | Do not consolidate the goal and interview by transitive closure; reject the interview semantically | `7cb07fe1` is the only valid goal clip | Complete 50 fps goal sequence versus a 25 fps post-match interview. |
| Alexander Isak 60′, Liverpool–Forest | Collapse the intrusive-overlay copy | `9b528816` | Same master and effectively the same 30 fps cadence; 37.779 s clean presentation versus a 13.056 s gambling-overlay cut. |
| Lee Kang-In 70′; M. Olise 55′; D. Welbeck 50′; L. Messi 83′; K. Schade 41′ | Unreviewable from retained bytes | Unresolved | Each component has one unreclaimed terminal asset. Arrival sensitivity comes from a retired bridge member, so graph metadata cannot establish visual identity or quality. |

The retained threshold corpus adds five useful policy labels:

- V. Lindelof: collapse the two copies and prefer the cleaner, longer rank-1
  720p60 broadcast over the shorter cropped/watermarked 720p60 copy.
- K. Mbappe 80′: collapse the two copies and prefer the 31.701 s 1080p50
  sequence over the 7.659 s 720p30 cut with a large overlay.
- K. Havertz: collapse the copies, but leave quality unresolved. The 1080p30
  copy has materially better encoding quality while the 720p30 copy avoids a
  CapCut tail; this needs an explicit product preference rather than a guessed
  metadata weight.
- J. King: collapse the copies and prefer the cleaner 7.573 s 720p60 cut over
  the 21.824 s 1080p60 screen/player-chrome copy. This is the first reviewed
  counterexample to duration-first metadata ordering: `IsUpgrade` would keep
  the longer, visibly worse presentation if the matcher joined them.
- Raphinha 37′: keep the short goal edit and longer tactical-analysis edit
  separate. They depict the same play but are distinct presentations, not
  interchangeable encodes.

The reviewed duplicate bridge leaks that have comparable semantics—Bellingham,
Fermin, Saka, and Isak—all favor the keeper the current comparator would choose
if the two roots directly matched. Cadence improves the explanation but does
not reverse one of those choices. J. King instead shows that a stable metadata
total order cannot solve screen capture, editorial chrome, or presentation
quality. Those signals require reliable validation/presentation evidence; the
existing FF-052 screen-detection gap prevents using them as a keeper tier yet.

This pass therefore does not justify a production `IsUpgrade` change. It does
justify a compact non-media regression set containing frame hashes, metadata,
and the accepted human labels. New post-FF-082 matches must supply retained
cadence and be reviewed before supersession reclaims the losing bytes.

## Conclusion

Do not add a durable perceptual `cluster_id` or treat dHash connected
components as one clip. That would make the current safe failure—an occasional
extra video—into false consolidation of distinct footage.

Do not replace `IsUpgrade` with strict, fixed-bucket, or fitted scalar ordering
from this corpus alone. Each candidate measurably changes retained keepers, and
the corpus has no human quality labels for retired bytes. FF-066 already
prevents a retired asset from becoming a new placement winner, so the observed
cycle is legacy data rather than a currently reproducible persistence path.

The next valid quality-policy experiment needs the reviewed labels encoded as
regression fixtures plus new post-FF-082 cadence/presentation evidence. The
legacy Danso cycle can be repaired separately by pointing its three retired
assets at the clear active winner, after explicit production-data approval.

The follow-up FF-082 implementation retains nullable cadence for new assets and
adds a direct-pair review-manifest mode to the audit command. It deliberately
does not alter `IsUpgrade`; historical rows from this checkpoint remain cadence
unknown.
