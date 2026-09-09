# FF-081 aligned-section experiment — 2026-09-08

## Scope and disposition

This is the next offline experiment after the
[cadence and keeper review](./video-cadence-review-2026-09-08.md). It measures
where two clips share hash evidence and what remains unsupported. It does not
choose a new keeper or change production matching, placement, popularity,
visibility, ranking, or retention. Active work remains
[FF-081](../../todo.md#ff-081--pairwise-quality-policy-is-not-a-stable-cluster-order).

Inputs were the saved September retained-asset CSV, the ten-pair August review
corpus, and the five-pair September cadence corpus. No production query,
download, model call, media rewrite, or schema change was needed. Current
keeper observations refer to the saved captures, not today's public ranks.

## Measurement contract

The [offline command](../../../scripts/audit_video_quality/README.md#aligned-section-report)
now emits `aligned-sections-v1` NDJSON. For each production-qualified primary
or sustained route, it examines the route's strongest window offset. It does
not combine the two routes or join unrelated offsets.

- A section joins similar hashes separated by at most two failed samples.
- A section needs at least ten samples of span and 80% similar samples.
- Every tolerated miss remains listed and does **not** count as supported
  coverage. Short or sparse groups remain unassigned raw matches.
- Each section records half-open start/end sample indexes in both clips.
- Each side reports supported coverage, section-span coverage, unsupported
  prefix/suffix, and gaps between sections. Gaps within sections are separate.
- An unsupported region is not proven new footage, an intro, or an outro.
  Cropping, overlays, compression, edits, and alignment error can also prevent
  hash support.

These grouping settings are diagnostic, not replacement thresholds. A section
does not certify goal content. A large gap cannot establish whether the missing
part is important; a high supported percentage cannot establish that it is not.

For explicit v2 hashes at 100 ms, ten sample bins represent approximately one
second. The report does not pretend to have exact source-frame or audiovisual
cut boundaries. Legacy hash rows retain sample coordinates but emit
`hash_cadence_ms: null`; their rate is not reconstructed from clip duration.
Other sample contracts are rejected. CSV pairs stay event-, category-, and
hash-version-scoped; curated pairs preserve their original left/right order.

Every row also retains the unchanged current comparator result, the earlier
contiguous/stable-offset/cadence-aware predictions, encoded FPS, dimensions,
bitrate, aggregate popularity, known exact observations, and any accepted
human judgment. Source-copy checksums travel with curated evidence. No human
judgment is generated from an experiment; an unlabelled pair emits `human: null`.

## Natural pair results

The table uses the sustained route to keep comparisons consistent. Times refer
to the sampled hash timelines, not exact MP4 cut points.

| Pair | Shared section | Unsupported edges outside that section | Interpretation |
|---|---|---|---|
| El Khannouss | Long 1.6–8.9 s ↔ short 0–7.3 s; every aligned hash matches | Long adds about 1.6 s before and 1.8 s after | Short containment evidence does not establish its claimed 60-fps quality |
| Adams | Long 1.5–8.9 s ↔ short 0–7.4 s; one tolerated miss | Long adds about 1.5 s before and 0.3 s after | Preserves the accepted cleaner, shorter sole keeper |
| Palacios | Short 4.2–9.3 s ↔ long 0–5.1 s; two tolerated misses | Short has about 4.2 s before; long has about 5.7 s after | Different-footage tradeoff, not a longer-superset relationship |
| Mitchell | Long 1.1–9.3 s ↔ short 0–8.2 s; three isolated tolerated misses | Long adds about 1.1 s before and 3.0 s after | Extra celebration remains an editorial question |
| Mariano | Long 9.0–14.4 s ↔ short 0–5.4 s; every aligned hash matches | Long adds about 9.0 s before and 0.6 s after | Strong short-within-long evidence; buildup versus presentation remains open |

Palacios's primary route instead aligns at a 4.4-second offset and yields a
4.9-second section. Mariano's primary route leaves the first short sample
unsupported. The report keeps these differences visible; it does not silently
turn the looser route into more precise timing evidence.

### Accepted judgments still constrain any future policy

- **Adams:** Supported hashes cover 79.35% of the long clip, whereas its
  section span covers 80.43%. Blindly moving the previous 80% boundary from
  span to support would reject the already accepted shorter sole keeper.
  This is not permission to fit another percentage to Adams.
- **Reviewed Mbappé 80′:** The accepted longer 1080p50 winner still has the
  same human label. The new sections support only 59.2–63.2% of the short
  clip; the older stable-offset span was 84.2%. Short fragments and matching
  failures leave part of the evidence outside the qualified section. A low
  section-coverage number must not automatically force both clips to remain.
- **El Khannouss:** The earlier native-frame experiment found repeated-motion
  evidence in its approximately-60-fps encoding. Complete short containment
  does not erase that finding or justify an FPS bonus.
- **Other August cases:** Existing direct-matcher misses remain misses. The
  report includes curated non-matches with no qualified routes instead of
  omitting them from review or inventing new edges.

No accepted label or earlier experimental snapshot was changed. Palacios,
Mariano, Mitchell, and El Khannouss still have no accepted keeper judgment.

## Broader saved-corpus evidence

The report emitted 2,030 current direct pairs, including 100 legacy pairs with
unknown hash cadence. Of those pairs:

- 466 have multiple sections under at least one qualified route.
- 228 have an interior gap of at least ten samples under at least one route;
  210 have explicit 100-ms cadence, so that is approximately a second or more.
- The old stable-offset experiment classified 1,497 pairs as equivalent or
  containing. Of these, 92 have such an interior gap under that experiment's
  selected route; 82 also have known cadence.
- Primary and sustained strongest offsets differ on 511 pairs.

These are review signals, **not counts of incorrect production deduplications**.
A missing section can be real editorial difference or a perceptual matching
failure. The current production policy does not use either offline coverage
threshold. Repeated or static footage can produce alternative plausible
offsets; this experiment retains one anchor per route and does not exhaust all
alignments. Insertions, reordered edits, and speed changes can require more
than one offset and remain outside this measurement's guarantees.

## Cost and verification

The pure segmentation pass is linear in the aligned sample span and reuses
existing dHashes and offsets. A local synthetic 60/63-second benchmark measured
about 2.04 µs and 1,920 allocated bytes per pair for both section routes, versus
about 1.81 ms for the existing two-route offset search. The Go container had a
four-CPU limit. This is a microbenchmark, not a production latency claim or a
load test of heavily fragmented clips. No new decode pass is required.

Tests cover containment, asymmetric unmatched edges, a 2.5-second interior
hole accepted by the old full-span baseline, brief tolerated misses, isolated
and sparse hits, insertions that change offset, route qualification, sample
accounting, reversal, compatible scopes, unknown legacy cadence, corpus drift,
output failures, and label preservation. Natural-pair positions are pinned
separately from quality judgments.

Validation passed: `make check-short` (including lint with zero issues), the
uncached audit-package tests and benchmark, local documentation link targets,
`git diff --check`, and SHA-256 checks of all ten preserved source MP4s. The
production matcher, placement code, earlier substitution experiments, and
August human-label corpus have no diff from this experiment.

Ignored local results are `overlap-natural.ndjson`, `overlap-reviewed.ndjson`,
and `overlap-retained.ndjson` under `scratch-audit-2026-09-08/`. The command's
README owns reproduction. The raw pair corpora were not rewritten.

## Next work, without losing the keeper-policy thread

Use the shared sections to guide playback of the unmatched Palacios and
Mariano footage, then record replacement and presentation judgments separately.
Do not adopt the section filter as a deletion gate. Compare any proposed
replacement relation against the whole accepted corpus, including Adams,
Mbappé, matcher misses, and distinct clips.

The [public-read interaction](./video-cadence-review-2026-09-08.md#selection-must-agree-with-the-public-read-model)
also remains unresolved: a deliberately retained clean singleton can still be
hidden by FF-078, popularity still controls ordering inside timestamp buckets,
and one source vote must not be copied onto two selected outputs. The
direct-cover experiment remains non-transitive research, not deployed graph
selection. Encoded FPS, density, resolution, watermark preference, and useful
content remain separate inputs rather than one fitted score.
