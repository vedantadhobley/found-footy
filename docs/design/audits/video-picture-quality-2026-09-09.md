# FF-081 picture-quality pilot — 2026-09-09

## Disposition

The OpenCV BRISQUE CPU baseline is implemented and measured. **The planned
learned image/video model comparison is not complete.** DOVER and the PyIQA
implementation remain held for confirmation of non-commercial purpose or
separate permission. No model was installed into a production image and no
shared inference node was called. Production selection is unchanged.

This extends the [cadence review](./video-cadence-review-2026-09-08.md) and
[aligned-section experiment](./video-overlap-review-2026-09-08.md), not their
accepted labels or previous predictions. Active policy work remains
[FF-081](../../todo.md#ff-081--pairwise-quality-policy-is-not-a-stable-cluster-order).

## New user observations

Review-page order is not always the retained corpus's left/right order.
[Separate review notes](../../../scripts/benchmark_video_quality/review-notes.json)
freeze these comments by asset ID:

- **Palacios:** The user tentatively prefers review A, the shorter 720p60 copy
  `a2e66274-1978-5e92-b8b8-94661627114e`, despite a little stutter. Whether the
  stutter is encoded or caused by local playback is unknown. The usefulness of
  the other cut's later celebration remains unresolved.
- **Mariano:** The user tentatively prefers review B, the longer 720p30 copy
  `9f37ee38-4df8-595f-86c4-f61e29f73388`. This is corpus **left**, whereas review
  A is the short 60-fps corpus-right copy. No discard decision was supplied.
- **Adams:** The earlier explicit shorter-copy replacement judgment remains
  unchanged. No benchmark result may relabel it.

## Inputs and method

All ten saved source MP4s passed their existing SHA-256 checks before the run.
The [tool contract](../../../scripts/benchmark_video_quality/README.md) owns
reproduction and dependency/model provenance.

- Five natural pairs, each measured as a whole clip and as the longest
  sustained-route shared section: twenty source windows.
- Eight native-resolution decoded frames per window at bin-center times.
  The prior dHash alignment supplies approximate temporal correspondence;
  it does not spatially register different crops, framing, or tilt.
- Full-frame scoring is the baseline. A fixed center crop is reported only
  as a sensitivity check, not chosen according to the desired result.
- Two 60-fps sources each generate ten four-second scenarios: lossless
  baseline, mild/strong blur, downscale/upscale, mild/strong H.264 compression,
  noise, sharpening, overlay, and 30-fps frames repeated into 60 fps.
- Total: forty windows/scenarios, 320 decoded sample frames, and 640 individual
  BRISQUE evaluations across full and center views. Lower predicts better.
  Raw scores and timestamps are retained; no fitted threshold or combined
  quality score was introduced.

The first six-second control-generation attempt stopped when a lossless noisy
file exceeded the enforced 256-MiB bound. Its incomplete outputs remain in
`run-01`; it has no success marker and contributes no results. `run-02` uses
four-second controls and completed all forty jobs. No original was modified.

## Natural results

These are full-frame medians. Scores are not calibrated across events, and
small differences are not proven perceptual differences.

| Pair | Whole-clip scores, corpus left / right | Whole preference | Shared-action scores, left / right | Shared preference |
|---|---:|---|---:|---|
| El Khannouss | 46.455 / 42.288 | Short 60-fps encoding | 44.160 / 41.265 | Short 60-fps encoding |
| Adams | 44.904 / 45.607 | Long keeper | 46.234 / 45.729 | Accepted short copy |
| Palacios | 44.324 / 44.017 | Long keeper | 40.452 / 43.679 | User-preferred short A |
| Mitchell | 58.956 / 60.591 | Long keeper | 58.674 / 59.864 | Long keeper |
| Mariano | 48.549 / 53.520 | User-preferred long B | 48.095 / 53.918 | User-preferred long B |

Shared-action scoring agrees with the one accepted and two tentative picture
preferences here. **This is not a three-example accuracy claim.** The sample
is small, selected for cadence disagreement, and shares an editing family.
Adams's shared-action difference is only about half a point.

Sampling/context matters: whole-clip scoring reverses Adams and Palacios.
Cropping matters too: Mariano's whole-center score instead prefers the short
copy (59.787 versus 61.943), while its shared-center score prefers the long
copy. We cannot select whichever aggregation reproduces a human label.

## Controlled failure evidence

Differences below are relative to each lossless baseline's full-frame median.
Positive is predicted worse; negative is predicted better.

| Transform | Adams score change | Palacios score change |
|---|---:|---:|
| Mild blur | +3.786 | +4.874 |
| Strong blur | +17.737 | +17.059 |
| Downscale to 320×180 and upscale | +14.596 | +13.204 |
| H.264 CRF 28 | +1.374 | +1.544 |
| H.264 CRF 40 | +8.525 | +5.257 |
| Added noise | **−15.863** | **−22.545** |
| Sharpening | −13.386 | −15.644 |
| Translucent overlay | +0.306 | +0.468 |
| Repeated 30→60 frames | −0.025 | −1.236 |

Blur, scaling loss, and stronger compression move in the expected direction.
But injected noise substantially improves the predicted score. The inspected
Adams control visibly adds grain to the same action; it does not restore
source detail. This is direct evidence against using BRISQUE alone as the
keeper ranking. Sharpening gains need human review, not an automatic "better"
label. The score is barely affected by the overlay, which is not evidence that
the overlay is acceptable. Repetition is outside a spatial metric's remit;
small differences also reflect which frames fall at the sampled timestamps.

## Cost and repeatability

Both complete runs produced **exactly identical individual scores**.

- Runtime: pinned Python image, Python 3.11.14, NumPy 1.26.4,
  OpenCV-contrib-headless 4.11.0.86, ffmpeg 7.1.5 on the CPU.
- Limits: two CPUs, 4 GiB, 128 PIDs; no GPU, network, or production mounts.
- Whole benchmark: 54.15 s and 53.47 s. First-run sample decoding consumed
  43.74 s; both-view inference consumed 10.40 s. Lossless controls make decoding
  much more expensive than the small source MP4s.
- Ten natural whole-clip/full-frame measurements: 3.87 s decode plus 1.57 s
  inference, about 0.54 s per clip in aggregate. This is eight sparse pictures,
  not full-video motion analysis or a production latency guarantee.
- First-run model load: 2.8 ms. Peak scorer-process RSS: 146.7 MiB.
  Cgroup-charged memory peaks were 109.7 and 107.7 MiB. RSS and cgroup accounting
  differ and must not be treated as interchangeable measures.

Host load and cache conditions were not isolated. No production-stage latency
or GPU speedup is inferred from this pilot.

## Separate compilation boundary: Demirović

Today's Stuttgart–Viking fixture `1635741` illustrates why picture quality
cannot decide event-specific substitution. For the 32′ goal, event
`3efba657-14b1-45bb-9442-fbead0a448a7`, CBS source tweet
`2097740357932888435` supplied a 49.493-second compilation of the 20′, 26′,
and 32′ goals. Asset `a35f448a-e202-597d-95a3-245b2f7f8416`, MD5
`d9cc3a761dcc5aeffac7de498ed3dfbf`, replaced the dedicated 28.053-second
asset `667417e0-3710-5e4d-bc8d-a185e5c0230d` at 17:37:08 UTC.

The retained Temporal output read `25:00`, `31:19`, and `31:32`; expected
minute was 31. The valid latter samples verified the clip. Local inspection
confirmed that it starts at 19′ but includes the third goal later. dHash
overlap was real; the duration-first comparator credited footage of other
goals as added completeness. This is not a wrong-clock normalization or
cross-event-dedup failure.

The compilation was inspected separately, **not scored in this ten-file
pilot**. Preserve this as an FF-081/FF-003 content-selection regression; a
quality model's high score cannot establish that the compilation should replace
the focused clip. No share or asset was repaired in production.

## Reproduction and next gate

Tool: [benchmark_video_quality](../../../scripts/benchmark_video_quality/README.md).
Ignored evidence: `scratch-audit-2026-09-09/quality-benchmark/` contains models,
license, generated controls, manifest, both NDJSON runs, resource logs, and
baseline/noise inspection frames.

| Artifact | SHA-256 |
|---|---|
| Benchmark image ID | `c36f5c5cf7f44c74cc5064ad9a927b4160e5b511e81a65d8f362b959f8c17972` |
| `run-02/manifest.json` | `ab06892e2ef8e62050672ac47f49f5dfa33f728b1314b4c8d2d5628f97e642b0` |
| `brisque-01.ndjson` | `efeca5e3507c042672acd62c9f73ed99a6677204588a47556355d6b6b2dd6b7e` |
| `brisque-02.ndjson` | `9a84ccc627086b26e6ea96f19568dd0b9e2fc41f2ddcde414fb81621012942bd` |

The next gate is license-cleared learned-model comparison on these unchanged
inputs, not deployment of BRISQUE. Preserve the noise/aggregation failures,
benchmark model loading and inference separately, and record stochastic score
variation where relevant. Use separate held-out examples before trusting any
replacement policy. Palacios and Mariano replacement decisions remain open.
