# Fixed-image clock prompt benchmark

Makes two sequential model requests per preselected sample. The default
`--comparison context` compares literal per-frame transcription with bounded
cross-frame field identification. `--comparison period-labels` compares the
local FF-094 default with experimental period-role wording only; the production
prompt and evaluator are not edited by either experiment.
Both use the actual Found Footy LLM adapter, schema and evaluator. Sampling
defaults remain model-owned; `reasoning_effort` is `none`. No retry occurs.

The tool requires explicit `--run-model`, `--endpoint`, `--model`, `--manifest`
and `--output` arguments. Verify the model ID from `/v1/models` first. It has
no SQL, object-storage, Temporal or NATS client. It reads local images only.
The output must not exist; completed calls are retained even when a later call
fails. The final `complete: true` marker means all planned calls finished;
inspect each result's `Error` separately.

The manifest is an array of `Name`, `Provenance`, exactly three `Images` paths,
and `Expected: {Elapsed, Extra}`. Optional `Gold` contains exactly three raw
timing observations. Expected time and gold are used **only after inference**,
never sent to the model. `TimingDifferences` reports literal differences in
clock, period, allowance and secondary timer, independently of soccer/screen
votes. Annotation spelling differences are not silently normalized away.
Missing files fail before inference. Limits are ten
samples, three images each and 16 MiB per image. Each request is capped at
60 seconds and concurrency is one.

Run in the pinned Go container with an explicit memory cap and `GOMEMLIMIT`.
Only this benchmark needs network access to the named gateway. Mount media
read-only and provide a writable output directory owned by the container user.
Do not load production environment files to obtain benchmark configuration.

The [September 15 evidence](../../docs/design/audits/clock-normalization-implementation-2026-09-15.md)
records the fixed images, prompt fingerprints, successful transcription checks
and remaining OCR/content errors. A single call per sample is not a measured
error rate; a repeated manual diagram is not natural footage or progression.

## Period-label controls — September 17

`fixtures/` generates code-native SVG scorebugs and manifests of at most ten samples.
Rasterize them offline with the existing ffmpeg/librsvg renderer. The generator
refuses an existing output directory. It writes vector sources, not edited match
footage. Three positions advance the main timer or secondary timer separately.
The corpus includes both halves, `1st`/`2nd`, `1re`/`2e`, actual superscript
suffixes, `HALF 1`/`HALF 2`, `1P`/`2P` and lowercase equivalents, isolated
ambiguous numbers, no label, `+1`/`+2`,
bare numbers explicitly labeled ADDED TIME, and frozen-main/secondary timers.
The bare period-position numbers have deliberately conservative **unknown**
gold labels; these test a proposed evidence rule, not a proven broadcast meaning.
The initial run used eighteen controls; four `P`/`p` controls were added after
the user asked about period notation. `fixtures/p-labels-20260917.json` records
that follow-up selection without rerunning the first eighteen or altering either
prompt. These are hypothetical visual variants, not recovered broadcast evidence.

`fixtures/natural-20260917.json` separately identifies four retained original
frame sets: the two wrong Barcelona admissions, the running +5′ allowance, and
the running +1 allowance. It requires those local artifacts; missing originals
must fail, never be substituted with synthetic imagery. Inspect images before
using their gold annotations. There are no captured production credentials,
signed URLs, or model network calls in the generator or unit suite.

The period-role wording lives only in `comparisonPrompts`. Its test asserts
that **only** the period instruction differs: main-clock, added-time,
secondary-timer, content and cross-frame instructions stay identical. The
recorded September 17 comparison used local `vision-evaluate-v2`, not production
v1. Future runs use the current evaluator and record its identifier. Accurate
transcription alone does not prove event identity. Do not change acceptance from a diagram's
result or treat graphics-only content votes as natural-video accuracy.

The [September 17 report](../../docs/design/audits/period-label-recognition-2026-09-17.md)
records the outcomes and limits. All 52 call records and their four explicit
batch completion records are preserved in
[`testdata/period-label-results-20260917.ndjson`](./testdata/period-label-results-20260917.ndjson).
This is an experiment snapshot, not a golden assertion that its wrong
classifications are desirable or that future model versions must repeat them.
`fixtures/policy_replay_test.go` replays those immutable responses with their
original expected times through v3. Only Yamal and the no-period reset control
change to rejected, under both prompt variants; no images or model calls are
required for this policy regression. Period-role wording remains experimental.
