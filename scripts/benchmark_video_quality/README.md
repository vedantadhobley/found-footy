# Offline picture-quality pilot

This is FF-081 research, not a production selector. It reads the five saved
September pairs (ten exact files), verifies SHA-256, and scores their decoded
pictures. It does not call production, Garage, X, or the shared LLM service.
It makes no keeper, popularity, visibility, or schema changes.

## Frozen measurement contract

- OpenCV BRISQUE trained on LIVE-R2 is the initial CPU baseline; lower is better.
  No quality-score threshold is selected or fitted to these pairs.
- Eight equally spaced bin-center samples from the entire clip and eight from
  the longest qualified sustained-route aligned section. Temporal alignment is
  the prior 100-ms dHash evidence, not exact scene or spatial registration.
- Native decoded BGR frames are passed to OpenCV. No resizing, equalization,
  sharpening, or dHash-sized image enters the natural full-frame baseline.
- Center-crop sensitivity (x=10–90%, y=20–85%) is reported separately, never
  fused into the baseline. It can remove actual action as well as overlays.
- All individual scores and requested/decoded sample timestamps survive.
  Median/mean/min/max describe the sample; they are not confidence intervals.
- Each pair retains the existing `human` field unchanged. The separate
  [review notes](./review-notes.json) record tentative Palacios-A/Mariano-B
  presentation preferences by asset ID. Neither authorizes replacement.
- BRISQUE is spatial. The separate
  [cadence experiment](../audit_video_cadence/README.md) owns repetition evidence;
  an unchanged picture score on repeated frames is not a failed motion detector.

Two original 60-fps sources generate four-second controls: lossless baseline,
mild/strong blur, downscale/upscale, mild/strong H.264 compression, noise,
sharpening, a translucent overlay, and 30-fps frames repeated into 60 fps.
FFV1 preserves filtered frames without another lossy generation except for the
explicit H.264 controls. All transformed copies are disposable; original
media is mounted read-only. Noise/sharpening/overlay are adversarial stress
cases, not invented human judgments. A baseline is a reference for its own
transform only, not proof of pristine broadcast quality.

## Dependencies and licensing

- Python base digest is pinned in [Dockerfile](./Dockerfile).
- NumPy 1.26.4 and OpenCV-contrib-headless 4.11.0.86 are pinned.
- OpenCV source/model commit:
  `0e5254ebf54d2aed6e7eaf6660bf3b797cf50a02` (4.11.0).
  [BRISQUE model](https://github.com/opencv/opencv_contrib/blob/0e5254ebf54d2aed6e7eaf6660bf3b797cf50a02/modules/quality/samples/brisque_model_live.yml),
  [range data](https://github.com/opencv/opencv_contrib/blob/0e5254ebf54d2aed6e7eaf6660bf3b797cf50a02/modules/quality/samples/brisque_range_live.yml),
  and [Apache-2.0 license](https://github.com/opencv/opencv_contrib/blob/0e5254ebf54d2aed6e7eaf6660bf3b797cf50a02/LICENSE).
  Both model SHA-256 values are enforced in `evidence.py`.
- Debian packages resolve at build time; save the image ID, ffmpeg version,
  package inventory, and build date beside the report. An image digest and
  model hashes make this run identifiable, not an indefinitely hermetic apt build.

DOVER/DOVER-Mobile and the PyIQA implementation of learned image metrics are
not installed by this baseline. Their published licenses restrict commercial
use. Non-commercial purpose or separate permission must be confirmed before
using them. Do not silently equate a published research model with a permissive
production dependency. Sources:
[DOVER license](https://github.com/VQAssessment/DOVER/blob/f1ddc96215bc7fbcf8f315c65d47905f339c3419/LICENSE),
[PyIQA license](https://github.com/chaofengc/IQA-PyTorch/blob/main/LICENSE).

## Execution

The saved input is `scratch-audit-2026-09-08/`; ignored outputs belong under
`scratch-audit-2026-09-09/quality-benchmark/`. Download the two pinned OpenCV
YAML files and its license into that output's `downloads/` first. No weights or
media are committed. [Compose](./compose.yml) declares the disposable runtime:
2 CPUs, 4 GiB, 128 PIDs, no network, no GPU, no host socket or credentials.

From the repository root:

```bash
DOCKER_BUILDKIT=0 docker build --memory=4g --cpu-period=100000 --cpu-quota=200000 \
  -t found-footy-quality-benchmark:2026-09-09 scripts/benchmark_video_quality
docker compose -f scripts/benchmark_video_quality/compose.yml run --rm -T \
  --entrypoint python benchmark -m unittest -v
docker compose -f scripts/benchmark_video_quality/compose.yml run --rm -T \
  benchmark prepare --output /output/run-01
docker compose -f scripts/benchmark_video_quality/compose.yml run --rm -T \
  benchmark score --manifest /output/run-01/manifest.json \
  > scratch-audit-2026-09-09/quality-benchmark/brisque.ndjson
docker compose -f scripts/benchmark_video_quality/compose.yml run --rm -T \
  --entrypoint python benchmark report.py /output/run-01/manifest.json \
  /output/brisque.ndjson
```

`prepare` requires a new output directory and never overwrites controls.
Failures are nonzero exits; success requires `prepare_complete` or
`benchmark_complete`, not merely process disappearance. The report rejects
missing/duplicate results and absent completion markers. Decode, inference,
model load, and process RSS are distinct; end-to-end wall time and container
peak memory should also be captured externally. Re-run scores to check
repeatability, without regenerating controls or moving the reference labels.

## Boundaries

This is a small, editing-family-biased pilot, not a representative accuracy
study. Shared footage may still differ in crop/geometry. Fixed samples miss
localized defects. A useful quality metric cannot by itself resolve the
Demirović compilation or determine whether buildup outweighs presentation.
Compare any eventual proposal against the older accepted corpus and
independent examples; do not choose weights from these three preferences.
