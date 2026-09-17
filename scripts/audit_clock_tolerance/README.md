# Offline clock-tolerance comparison

Compares the production `vision.Evaluate` function at ±1 and zero minutes
against saved `ValidateClip` inputs and raw frame observations. It has no
network, database, model, media, or production write path. It does not change
the deployed evaluator or introduce a new acceptance policy.

## Run

FF-094 changed the evaluator to v2. The default strict mode still refuses
historical baseline drift. Use `--replay-current` to retain the recorded result,
compare current ±1 versus zero, and count policy changes separately. The old
corpus is unchanged; the Wöber auxiliary-clock repair is its single v2 policy
difference. Do not describe that difference as a tolerance effect.

Pass explicit local **EventWorkflow** history exports in Temporal's base64
`json/plain` JSON format. No credentials or `.env` are needed. For example:

```bash
docker run --rm --network none --memory 2g --cpus 2 \
  -e GOMEMLIMIT=1400MiB -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache \
  -v "$PWD:/src:ro" \
  -v "$HOME/.cache/found-footy/gocache:/gocache" \
  -v "$HOME/.cache/found-footy/gomodcache:/gomodcache" \
  -w /src golang:1.25.11-bookworm \
  go run -buildvcs=false ./scripts/audit_clock_tolerance \
  scratch-audit-2026-09-11/mastantuono/second-history.json
```

Go dependencies must already be cached; network access stays disabled. Missing
local evidence is an error, not permission to export fresh production data.
Inputs are limited to 128 files and 64 MiB per file. Only workflow context and
vision activity input/output payloads are decoded. Unrelated payloads, including
service configuration and signed download URLs, are not copied into reports.

The report retains source checksums, event/run identities, schedule/completion
counts, raw frames, both evaluations, and transition/reason counts. Identical
run exports count once; conflicting snapshots fail. Every evaluated observation
must reproduce the recorded outcome, reason, and matched minute at tolerance
one. Otherwise the whole report fails rather than mixing policy drift into
the tolerance experiment. Missing frame arrays are explicitly excluded; partial
nonempty arrays fail. Unfinished/failed activities are not counted as rejections.

## Meaning and limits

- This changes **only** the tolerance argument. Normalization, sample positions,
  soccer/screen voting, first-matching-frame selection and fallback rules stay
  unchanged. Zero tolerance is not a seconds-level comparison.
- Under v2, unpinned frozen clocks remain unverified unless other evidence
  resolves the verdict; they no longer verify on period alone. OCR rebasing
  and other ambiguous/no-clock behavior remain separate.
- Counts describe vision activity completions, not tweet popularity, independent
  content, public videos, or a full replay of placement/ranking after rejection.
- An old accepted verdict is not a human correctness label. The tool does not
  label new rejections as false positives or retained clips as true positives.
- Shared footage and matching clocks cannot establish whole-clip replacement
  suitability. FF-081 remains separate from this FF-003 admission experiment.

## Reproducible evidence

[`testdata/recorded-histories.json`](./testdata/recorded-histories.json) is a
generated, allowlisted projection of fifteen saved exports: source/checksum,
event/run context, schedule count, and each completed vision input plus raw
frames and original verdict. It contains no media, credentials, signed URLs,
full histories, tweet text, or new human labels. Two exports duplicate earlier
ones; the tests require thirteen distinct runs, the separately counted Wöber
policy change, and the exact current tolerance-transition table.
The projection remains runnable by the test suite if local full histories expire.

`TestKnownClockCases` separately covers Mastantuono, Lens, the existing
Abdelkarim lower-edge regression, and constructed clock/period controls. The
Abdelkarim case repeats one user-reported `28:56` observation as a regression;
it is **not** three newly recovered OCR readings and is excluded from the
historical sample. Decoder and comparison tests cover identity, baseline drift,
duplicate exports, missing/partial evidence and no partial output on failure.

[`review_test.go`](./review_test.go) separately preserves eight agent-read
main-timer controls from original media and the FF-094 running-timer/added-time
regression. These are not recovered model outputs or user-confirmed
labels and are excluded from the saved-history totals. The
[broader review](../../docs/design/audits/clock-tolerance-broader-review-2026-09-12.md)
records selection, original identities, replay losses and the durability gap.

Run `go test ./scripts/audit_clock_tolerance ./internal/domain/vision` through
the pinned Docker toolchain. Read the
[September 12 comparison](../../docs/design/audits/clock-tolerance-2026-09-12.md)
for results and the limits on changing production from this sample.
