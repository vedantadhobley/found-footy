# Offline frozen-clock policy screen

The original mode tests only whether a frozen-compatible period boundary
without a readable elapsed timer should verify a clip. It requires saved
baseline reproduction and refuses records that today's evaluator cannot reproduce.
Its earlier results remain historical; no copied legacy evaluator is retained.

`--replay-current` instead compares the actual current evaluator against
immutable recorded verdicts. It reports every changed outcome/reason/minute,
explicit exclusions and affected live shares. It does not compute a new public
graph/support projection or rewrite evidence. Neither mode changes runtime data.

## Inputs and run

`query.sql` is an explicitly read-only, repeatable-read projection of accepted
raw evidence, clock-rejected candidate observations and current live-share
inputs. Running it against production needs authorization for that inspection.
The Go program only reads explicitly provided local NDJSON files: no network,
database, media, model or mutation path exists in the program.

Run with the pinned toolchain and already cached dependencies:

```bash
docker run --rm --network none --memory 2g --cpus 2 \
  -e GOMEMLIMIT=1400MiB -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache \
  -v "$PWD:/src:ro" \
  -v "$HOME/.cache/found-footy/gocache:/gocache" \
  -v "$HOME/.cache/found-footy/gomodcache:/gomodcache" \
  -w /src golang:1.25.11-bookworm \
  go run -buildvcs=false ./scripts/audit_frozen_clock --replay-current \
  scratch-audit-2026-09-15/frozen-clock-evidence.ndjson \
  scratch-audit-2026-09-15/saved-history-clock-evidence.ndjson
```

The optional second file is a generated projection of the earlier
[saved history corpus](../audit_clock_tolerance/testdata/recorded-histories.json),
deduplicated by event/run. It preserves schedule identity, original expected
time, raw frames and verdict, without copying staging URLs or unrelated
Temporal payloads. Those old outputs have no recorded evaluator identifier;
strict mode requires baseline reproduction; current replay keeps their original
outcome alongside the new result instead.

Inputs are limited to 16 files, 64 MiB each, and 1 MiB per line. Duplicate
identities, unsupported accepted-evidence versions, partial frames and baseline
drift fail the whole report. Manual evaluations and rejected rows without
matching retained expectations are counted separately. A rejected row's current
provider fields are used only if their normalized minute/period match its saved
expectation; the accepted rows retain their original expectation and tolerance.

## Original experimental boundary (strict mode)

Both controls require an existing unpinned reading, literal zero seconds and a
supported main-clock boundary. They do not treat an advancing `90:37`, bare
`90`, or an unrecognized display as proven frozen. Reset H2 and ET boundaries
require the independently visible period. This is a raw-observation screen,
not proof that a broadcaster actually froze its image or timer.

The **naive** control excludes same-period frozen readings from minute checking
while preserving content votes and all other readings. The production evaluator
then treats a clip with no remaining clock evidence as unverified. This exposes
the danger of converting a clearly too-late clock into soft acceptance.

The **bounded** control additionally preserves the period-end lower bound:
if `expected completed minute + tolerance` is earlier than that boundary, the
frame contradicts the target. It cannot soften rejection merely because its
exact elapsed stoppage minute is unknown. An independently matching exact
reading can still verify the clip, consistent with the existing any-match rule.
Cross-period handling, other parser defects and all non-targeted fallbacks stay
unchanged. No clock-progression requirement is introduced.

Raw fields are never rewritten in the report. The counterfactual works on a
copy with selected `clock` values excluded; `ExcludedFrames` identifies those
indices. It is not a new OCR output or a full alternative evaluator.

## Original public projection and limits

The public projection mirrors current FF-078 filtering and rank ordering but
holds selected roots and scores fixed. It is not a historical deployment replay
or a dHash/category-support recomputation. A change to an unshared variant could
also change support elsewhere; investigate that separately if any accepted
evaluation changes. Missing old raw evidence remains unknown, not unchanged.

The [September 15 results](../../docs/design/audits/frozen-clock-policy-2026-09-15.md)
record the coverage gap and the retained Nico counterexample. The allowlisted
generated fixture in `testdata/nico-boundary.ndjson` preserves that model output;
it is not a visual correctness label. All other policy examples are explicitly
constructed.

Tests cover allowances and period/reset boundaries, the minimum-time guard,
content gates, wrong halves, genuine secondary timers, repeated/reversed frames,
one readable match, contradictory readings, input exclusions, baseline drift
and fixed-score visibility. Run `go test ./scripts/audit_frozen_clock` with the
same Docker toolchain.

The [v2 implementation report](../../docs/design/audits/clock-normalization-implementation-2026-09-15.md)
records the subsequent full replay and its extra malformed-clock effects.
The [v3 follow-up](../../docs/design/audits/period-label-recognition-2026-09-17.md#implementation-follow-up)
compares this same corpus before and after removing unlabelled period offsets.
Current replay accepts known v1/v2/current evidence without rewriting its
recorded verdict; unknown evaluator versions still fail the report.
