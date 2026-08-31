# Video cadence is independent quality evidence

## Context

`DownloadAndStage` already probes average frame rate and uses it for the
minimum-cadence admission gate. EventWorkflow then discarded the value before
asset persistence. Keeper selection could compare duration, resolution, and
`bitrate / (width × height)`, but could not distinguish a 30 fps encode from a
60 fps encode with the same spatial bitrate density.

Dividing spatial bitrate density by frame rate yields bits per spatial pixel
per frame. That is useful compression evidence, but it is not a complete
quality score: a 60 fps encode can have a lower per-frame budget and still
provide the better motion presentation.

## Decision

New EventWorkflow histories persist ffprobe's positive frame-rate value as
nullable `video_assets.frame_rate`. Existing rows remain unknown. The workflow
command change is guarded by `ff-082-cadence-metadata`; histories without that
marker keep their recorded activity payloads.

Frame rate remains independent from spatial bitrate density and derived bits
per pixel per frame. None changes keeper selection until FF-081 has reviewed
direct-pair labels. The review manifest records both media facts plus separate
human decisions for whether the pair should collapse and which clip is the
better keeper.

The current helper and documentation use the name *spatial bitrate density*
for `bitrate / (width × height)`. The rename changes no threshold or behavior.

## Consequences

- Migration `20260831_01_add_video_frame_rate.sql` adds one nullable column and
  a positive-when-present constraint. It does not rewrite historical rows.
- Old assets cannot participate in cadence comparisons unless their original
  bytes are deliberately probed later. Missing cadence remains explicit
  instead of being inferred from bitrate or container conventions.
- A future FF-081 policy must state the product tradeoff between motion
  cadence, compression, completeness, spatial resolution, and presentation.
  It cannot hide that tradeoff inside a mislabeled scalar.
- The direct-pair corpus can evolve independently from production behavior;
  unreviewed metadata scores remain diagnostics, not policy.
