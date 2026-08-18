# Dense frame hashing uses a versioned bounded working image

## Context

The dHash algorithm was already grayscale → histogram equalization → 9×8 area
reduction → adjacent-pixel comparison. Its implementation still transported
every sampled frame from ffmpeg as a full-resolution color PNG, then decoded
and traversed every pixel in Go. A 44.6-second 3808×2146 Huijsen candidate
reached the 100-second dense-extraction deadline on all three attempts. The
stored `frame_hashes` bytes did not identify their algorithm, preprocessing, or
sample interval, even though changing any of those choices makes two sequences
incomparable. The archived Python asset model did store a hash version.

## Decision

- ffmpeg samples at the configured interval, converts to grayscale, and
  area-reduces to a fixed 640-pixel width before lossless PNG serialization.
  Go retains histogram equalization and the final 9×8 area reduction. The
  served MP4 is never resized.
- `FrameHashVersion` identifies preprocessing plus sample interval. The current
  default is `dhash-v2-gray640-equalized-area9x8@0.1s`; rows written before this
  contract normalize to `dhash-v1-unversioned`.
- EventWorkflow compares perceptual sequences only when their versions match.
  Exact MD5 ownership remains version-independent.
- `HashVideo` requires at least `MinRunFrames` readable hashes. A shorter
  sequence returns `insufficient_hash_frames` as a deterministic content
  outcome. Every byte-identical waiter receives the same outcome without
  another extraction.
- The additive `video_assets.hash_version` column keeps a legacy default so the
  old binary can continue writing during the migration-to-release window. The
  one-time migration stamps the exact embedded schema hash and remains separate
  from the application deployment.

The per-frame Hamming threshold, 30-frame/three-gap window, category boundary,
and offset matcher do not change.

## Evidence

A capped synthetic ffmpeg comparison used a five-second 3808×2146 source at 10
fps with four CPUs and 2 GiB. Full-resolution color PNG emitted 40,075 KiB in
1.549 seconds with 1,259,812 KiB max RSS. The bounded grayscale path emitted
779 KiB in 0.457 seconds with 139,128 KiB max RSS. That is 51× less pipe data,
3.4× lower ffmpeg wall time, and 9× lower ffmpeg RSS. The real Huijsen source or
an equivalent natural 4K candidate remains the production validation.

## Consequences

This change may shift Hamming values because the working-resolution resampler
is now part of the algorithm. Version isolation prevents silent mixed-corpus
comparisons. It may improve an overlay or color-grade near miss, but it does not
normalize crop or layout changes and therefore does not close FF-004. Re-hash
that pair under v2 before changing thresholds or matcher semantics.

Rollback does not need to drop the additive column. Restore the prior embedded
schema hash in `schema_version`, then recreate the prior application image. The
migration file and its contract test are deleted only after every durable
environment has applied the flattened schema change.
