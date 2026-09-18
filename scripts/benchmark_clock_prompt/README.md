# Fixed-image clock prompt benchmark

`--comparison current` makes one request per preselected sample with the exact
current runtime prompt. Other modes make two sequential requests. The default
`--comparison context` compares literal per-frame transcription with bounded
cross-frame field identification. `--comparison period-labels` compares the
frozen pre-promotion default with the original experimental period-role wording
only. Its result label `default` means the frozen September 18 baseline, not
today's runtime prompt. The embedded baseline's fingerprint is unit-tested so
later prompt changes cannot silently redefine historical comparisons.
No mode edits runtime code or the evaluator.
`--comparison transcription` compares the default with shorter timing wording
that keeps the soccer/screen rubric, removes numerical clock examples and
makes missing printed period labels explicitly null. It is benchmark-only.
All comparisons use the actual Found Footy LLM adapter, schema and evaluator.
Sampling defaults remain model-owned. No retry occurs.

`--comparison period-explicit` retains the failed pre-promotion normalized
period experiment. `--comparison period-literal` follows the current nullable
literal `period_label` contract and retains complete wire JSON as `RawResponse`.
Bare digits, allowances and unknown labels supply no period offset. Historical
normalized-period modes use a private legacy schema; current/runtime calls do
not accept that retired model field.

`--temperature N` is an optional, recorded sampling override within the
verified Gemma catalog range [0,2]. Omission preserves the gateway default.
The benchmark uses the application LLM adapter's typed optional field, whose
client-scoped HTTP repair preserves explicit zero despite the pinned SDK's
scalar `omitempty`. It opens no listener and changes no shared setting or
process-global transport. Zero temperature does not guarantee correct or
bit-identical output across executions.

`--reasoning none|high` defaults to `none`; `--max-tokens N` defaults to zero,
which omits the override and preserves the model default. Verify support and
limits in the catalog before each run. Both the report header and each result
record the chosen reasoning and budget; successful responses retain token usage.
Reasoning text is not captured. An invalid/truncated JSON response or timeout
remains a failed result, never a reason to silently retry or raise the deadline.
Use equal budgets when comparing reasoning modes so output caps are not an
unrecorded second variable.

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
Two later `ET1`/`ET2` controls check extra-time label preservation. They are
appended by the generator; the original manifests and reports remain unchanged.

`fixtures/natural-20260917.json` separately identifies four retained original
frame sets: the two wrong Barcelona admissions, the running +5′ allowance, and
the running +1 allowance. It requires those local artifacts; missing originals
must fail, never be substituted with synthetic imagery. Inspect images before
using their gold annotations. There are no captured production credentials,
signed URLs, or model network calls in the generator or unit suite.

The original period-role experiment remains frozen in `comparisonPrompts`.
The runtime prompt now has a focused role instruction with explicit P/p forms;
`current` tests that exact wording. Tests require **only** the period instruction
to differ from the pre-promotion default: main-clock, added-time,
secondary-timer, content and cross-frame instructions stay identical. Tests
also pin the unchanged schema and v4 evaluator for this prompt-only change. The
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
original expected times through v4. V3's Yamal and no-period reset controls
remain rejected under both prompt variants; v4 additionally verifies the two
Maitland-Niles running-stoppage responses. No images or model calls are
required for this policy regression. The frozen comparison remains an
experiment snapshot; runtime promotion and its limits are recorded separately.
The [September 18 qualification](../../docs/design/audits/gemma-12b-period-prompt-2026-09-18.md)
records the failed normalized-period prompts, sampling experiments and the
complete 39-call literal-label runtime screen. The code remains undeployed.
