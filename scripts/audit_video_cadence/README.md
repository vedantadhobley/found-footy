# Offline video-cadence experiment

This command inspects regular local media files and writes diagnostic JSON.
It has no production adapter, network protocol, database, or media mutation
path. It does **not** select keepers or certify original capture FPS.
The accepted work is [FF-081](../../docs/todo.md#ff-081--pairwise-quality-policy-is-not-a-stable-cluster-order).

## Measurement contract

- Preserve decoded frame cadence with `-fps_mode passthrough`; do not insert
  an `fps` filter. Our production 10-fps dHash samples cannot answer this question.
- Compare adjacent 320×180 grayscale frames. Retain whole-frame and central
  region mean absolute differences (MAD, on the 0–255 scale). The center is
  x=10–90%, y=20–85%, excluding common edge graphics but potentially also real
  action; it is a diagnostic crop, not a new media preprocessing policy.
- Use approximately two-second, non-overlapping windows. PTS deltas outside
  0.5–1.5 times the reported frame interval make a window inconclusive.
- Require at least 48 transitions and center MAD p90 ≥0.6 before testing
  repeat factors 2–6. A repeat cycle has one change ≥0.6 and every intervening
  change ≤20% of that change. Require the same phase in at least 80% of a
  minimum twelve cycles. These fixed values define an experiment, not a
  production quality threshold.
- Emit `periodic_repeat_evidence`, `no_repeat_pattern_detected`, or explicit
  inconclusive timing, low-motion, and short-window classifications. Preserve
  mixed windows rather than averaging them into a single clip-level FPS.

The repeat factor describes a detected pattern, not an effective/source FPS.
For example, period two in a 60-fps stream is consistent with duplicated
30-fps motion. It does not prove that provenance. Interpolation, moving
overlays inside the center, compression noise, edits, periodic real motion,
and low-motion scenes remain limitations. No detected repeats is **not**
evidence of native capture. Static or cropped-out motion stays inconclusive.

The subprocess deadline is 45 seconds per file. Input must be a regular file
≤256 MiB, duration in (0,90] seconds, and contain 2–12,000 decoded frames with
finite timestamps and a finite average rate in [1,240]. Missing PTS, partial
frames, probe/decode count disagreement, and process failures return errors.
Output includes SHA-256, reported FPS, window measurements, and elapsed time.
It retains only two scaled frames plus scalar measurements in Go memory;
ffmpeg's own decode allocation is separately bounded by the tool container.

## Running and tests

Use the repository's pinned Go image to compile the command and tests. The
normal `make test-short` gate runs scalar/reader tests without ffmpeg. Build
artifacts and media stay in an ignored scratch directory, never Git.

```bash
docker run --rm --network=none --memory=6g --cpus=4 \
  -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache \
  -v "$PWD:/src" -v "$HOME/.cache/found-footy/gocache:/gocache" \
  -v "$HOME/.cache/found-footy/gomodcache:/gomodcache" \
  -w /src golang:1.25.11-bookworm sh -c '
    CGO_ENABLED=0 go build -buildvcs=false -o scratch-audit-2026-09-08/audit-video-cadence ./scripts/audit_video_cadence &&
    CGO_ENABLED=0 go test -c -o scratch-audit-2026-09-08/audit-video-cadence.test ./scripts/audit_video_cadence'
```

Choose a **locally retained** worker image containing ffmpeg, not a running
worker container. No app entrypoint, credentials, Docker socket, production
network, or production volume is needed. Supply its immutable commit tag:

```bash
cadence_image='found-footy-worker:<locally-retained-commit>'
docker run --rm --network=none --read-only --memory=2g --cpus=2 \
  --user=1000:1000 --tmpfs /tmp:rw,size=256m \
  -e CADENCE_REQUIRE_FFMPEG=1 \
  -v "$PWD/scratch-audit-2026-09-08:/audit:ro" \
  --entrypoint /audit/audit-video-cadence.test "$cadence_image" \
  -test.v -test.run TestFFmpegCadenceControls
```

For saved clips, use the same isolated invocation with entrypoint
`/audit/audit-video-cadence` and explicit `/audit/media/<asset-id>.mp4` paths.
Redirect stdout to an ignored local JSON artifact. Existing saved media is
mounted read-only, so this does not re-encode or replace it.

Controls generate native-rate test motion at 30/60 fps, 30/20/15→60 repeated
motion, lossy re-encoding, blended interpolation, static footage, and an
animated edge overlay. A central animation deliberately masks repeated
background motion: a passing test preserves that known false-negative
boundary, not a native-FPS claim. Pure tests add repetition phase shifts, factors 2–6,
mixed windows, low motion, scene cuts, malformed timing, partial reads, and
resource bounds. A control passing proves only the stated experimental
boundary; it does not validate a production selector.

See the [natural-pair review](../../docs/design/audits/video-cadence-review-2026-09-08.md)
for retained bytes, measured caveats, and the separate user quality judgments.
