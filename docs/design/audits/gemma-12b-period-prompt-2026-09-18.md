# Gemma 12B period-role prompt — September 18 UTC

## Result and disposition

The [follow-up experiments](#follow-up-experiments) retain the failed
normalized-period attempts and qualify a replacement literal-label contract.
That contract is now implemented locally with `vision-evaluate-v5`; it is not
deployed.

The original focused prompt remains historical evidence. All 37 preselected
requests completed without transport or JSON errors. Gueye's three repeats
retain the correct clocks, +4 allowance and null printed period. However,
Fernández acquires an invented `1H` in one repeat, and one ambiguous-digit
control acquires `2H`. This is not a clean extraction acceptance pass or proof
that prompt wording enforces the evidence contract.

Those failures blocked the prompt-only candidate and remain retained evidence;
they were not retried away or hidden by looser evaluation. The later literal-
label contract has its own completed qualification below.
The [Control admission handoff](./control-ocr-admission-handoff-2026-09-18.md)
is a separate FF-037 follow-up. No shared gateway changes, production writes,
workflow recovery or historical verdict changes occurred here.

## Original prompt-only boundary

Only the default prompt's period instruction changes. It names ordinal,
superscript and P/p half labels; distinguishes half context from isolated
digits; and excludes score digits, logos and added-time allowances. Explicit
extra-time identification is still required for ET1/ET2. All other prompt
lines, the JSON schema, `vision-evaluate-v4`, clock tolerance, sampling,
three-frame request shape, content rubric and deadlines remain unchanged.

The effective prompt hash already travels with validation evidence; no new
schema field or evaluator version is needed for a wording-only change. Tests
bind both the default and an explicit override to the submitted request and
retained fingerprint. Existing historical observations remain immutable.

The benchmark's `current` mode sends the exact runtime prompt once per sample.
Its `period-labels` comparison now embeds the exact old baseline, retaining
the original experimental line and report names. Historical `default` means
that frozen baseline, not the changed runtime default.

## Method

The real catalog ID is `gemma-4-12b`, reached through Control's public Joi
endpoint with the application adapter. Reasoning is `none`; output budget
and sampling remain model-owned. Each HTTP request has the existing 60-second
deadline. Calls are sequential and never retried. No API expectation or gold
annotation enters the model request.

The [restoration trial](./gemma-12b-restored-validation-2026-09-18.md) supplies
the same five independently inspected natural three-frame sets. Each is run
three times. The original eighteen synthetic controls and four P/p controls
run once each. All six reports have explicit completion markers: 5 + 5 + 5 +
10 + 8 + 4 calls. The natural corpus lacks visible half labels; synthetic
controls cover those forms but do not establish natural broadcast accuracy.

## Natural footage: 15 calls

Requests complete in 10.682–13.606 seconds. Eleven have exact literal timing
agreement; three differ only by the omitted prime on Maitland-Niles's +5′;
one has an invented period. These are repeated selected cases, not an error
rate estimate or an independent matched A/B accuracy study.

| Sample | Three-repeat result |
|---|---|
| Gueye, 45+2′ | Exact 46:35/37/39, +4 and null period every time; verified by v4 |
| Yamal, wrong-half 89′ candidate | Exact timing every time; rejected every time |
| Jesus, wrong fixture | Exact timing every time; clock gate still verifies the known wrong-fixture clip |
| Maitland-Niles, 90+6′ | Correct clocks and field roles; +5′ becomes +5 each time; verified |
| Fernández, 45′ | Two exact reads; second repeat invents 1H on all three frames while retaining +1; verdict remains verified |

The Fernández reading violates the visible-label contract even though it does
not change this candidate's verdict. The Jesus result is the existing semantic
limitation: a matching clock cannot identify the match or prove the goal is
present. Neither failure is fixed by this wording change.

## Synthetic field controls: 22 calls

Requests complete in 8.745–9.888 seconds. Twenty-one controls match all timing
fields. Ordinals, superscripts, explicit HALF 1/HALF 2, P/p, allowance and
secondary-timer roles are correct in this selected layout.

`ambiguous_two` returns `2H` on all three frames despite conservative null
gold. Its main clocks are 05:25/28/31. The claimed half causes the unchanged
evaluator to rebase and verify the second-half target. This is a hypothetical
ambiguity control, not captured proof of a wrongly accepted production clip;
it demonstrates that the prompt does not reliably enforce the intended rule.
`ambiguous_one` and `no_half_label` retain null and reject their wrong targets.

Nineteen controls are called soccer footage by a majority of frames, although
the inputs are graphics-only. Thus accurate timing is not content validation.
`second_2P` has correct timing but a non-soccer majority and rejects; this is
not a period-parsing failure. Diagram verdicts must not drive soccer policy.

## Verification and retained evidence

The new prompt wording guard failed before the change and passed afterward.
Scoped race tests, vet and pinned lint passed for the vision domain/activity,
LLM adapter, benchmark and fixtures. The unconstrained `go test -short ./...`
also discovers ignored September 14 scratch programs with duplicate `main`
functions and fails there; those unrelated files were not changed.
The explicit project-package suite (`./cmd/... ./internal/... ./migrations/...
./scripts/... ./test/...`) passes with `-short`, excluding only those scratch
directories. Database/live integration tests were not run for this prompt-only
slice.

Artifacts remain under `scratch-audit-2026-09-18/12b-period-roles/`, with a
local runner and preselected manifests. Each result includes prompt, schema,
image hashes, model, raw response, usage, elapsed time and evaluation.

- New prompt SHA-256: `9dbc5be9cb13c14cd90eee273a107d76242e491c5e9cad9670206b2ddcdfc1a5`.
- Frozen baseline: `7b80787118e7ec3f8114a1a8bab2a431831b792bc228cb544fc3e5ee9f38b0e7`.
- Unchanged schema: `00f178fc55b7fbd3a407eb83dbf28b1bed5d70b4d574ffd980bd9e7a34be763c`.
- Natural manifest: `ec608431626aad5f8d66f2b34b5822bc0a61939a1cae4ee733cd437b5edac45d`.

| Report | SHA-256 |
|---|---|
| `natural-1.ndjson` | `3a2d34c184b91a3dcea730d4c5229daaa251111d19f037474f3b82e0d0f34a42` |
| `natural-2.ndjson` | `d3c914c034e5cd4e16e07a61801e53ee6e4ab26c592510ee42982041039148fc` |
| `natural-3.ndjson` | `0aa8f6ccc23b9589de7a338579f8be81103323ec174e8d8e8002dee978236611` |
| `controls-1.ndjson` | `390ed4fe5a49289abdfe45e31c237b0751bf7a8b4b38f049972f7bc932ffc958` |
| `controls-2.ndjson` | `51c44705815361fffafdd63b8e5ca4a5d41897612fd888136e759bd841fab714` |
| `p-controls.ndjson` | `0eafd4414031218a38d33e1af76f168aea2c054bf2efc3b77dcfeaaf25280e14` |

## Follow-up experiments

The user chose to keep 12B and requested preparation for the next match day.
The same real model was rechecked in the live catalog: temperature defaults
to 1, explicit values from 0 to 2 are supported, and the gateway was idle
before this sequential workload. No Control setting or production state was
changed. Sampling omission was the application default when these follow-up
experiments began; the qualified local contract now sends exact zero.

All follow-up runs use original full frames, three images, no reasoning,
model-owned output budget, the 60-second HTTP deadline and no retries. Every
attempt is retained. Aliases/gold/expected times never enter requests. The
first-stage cases are Gueye, Fernández and the ambiguous-two diagram, three
repeats each. A passing stage advances to the five natural cases repeated
three times and the saved field controls; failures are not replaced by retries.

| Candidate | Result |
|---|---|
| Stronger explicit-label wording, existing schema, default sampling | Nine calls; no invented halves, but the third Fernández repeat loses +1. Wider stage not run. |
| Literal label plus deterministic normalization, default sampling | Nine targeted calls pass; wider 37-call screen loses Gueye's +4 once and reads superscript 2e as 2². Not qualified for promotion. |
| Existing local prompt/schema, explicit temperature zero | Nine calls; Gueye/Fernández exact, but all three bare-two controls still infer 2H. Sampling alone does not enforce label evidence. |
| Literal label plus deterministic normalization, explicit temperature zero | The interrupted run resumed without repeating completed calls: all 39 finish without transport/schema errors and preserve timing semantics. |
| Literal label, smallest positive `float32` temperature | Nine targeted calls finish, but all three Gueye repeats lose +4 and all three Fernández repeats lose +1 while misplacing `4'` in `period_label`. This SDK workaround is not equivalent to zero. |
| Final runtime prompt/schema/adapter at exact zero | Nine targets and a separate complete 39-call screen pass. This is the locally implemented contract. |

The literal model schema replaces the inferred `period` enum with raw nullable
`period_label` text. Benchmark-only code maps explicit half/extra-time aliases
to the existing enum. It never maps a bare number, an allowance or an unknown
label into a half. Raw wire JSON is retained alongside adapted observations;
no historical evidence is overwritten. Serializing the experimental schema
also orders its object keys; this is not an isolated causal proof about the
field name alone.

At default sampling, natural calls take 10.805–13.931 seconds. Three
Maitland-Niles results substitute an ASCII apostrophe for the prime. The
extra substantive Gueye omission rejects that genuine candidate. Among 22
field controls, superscript 2e is transcribed as 2² and safely supplies no
offset; the bare added-time 2 control gains an unprinted plus. The first is a
missed label, not permission to add 2² as an alias. Graphics-only content votes
remain outside this field-quality screen. Jesus still demonstrates wrong-match
footage passing a matching clock; this work cannot fix FF-003's identity gap.

## Runtime implementation

Literal transcription is now the local application's wire contract:

- The model returns literal label text; new evidence retains it unchanged and Go normalizes
  it in Go. Do not save an inferred enum as though it were printed text.
- Keep old `period` observations and completed Temporal results readable;
  old evidence must not be rewritten. This is an internal model/evidence
  change, not a fixture/event identity or frontend contract change.
- Test explicit labels, unknown/bare labels, allowances, both halves and extra
  time; preserve main/secondary timing, content gates, ±1 tolerance and the
  existing any-usable-match policy. No expected-time-driven offsets.
- The LLM port exposes typed optional temperature and proves omitted/default,
  explicit zero and nonzero values at HTTP. A client-scoped request repair
  preserves exact zero despite the pinned SDK's scalar `omitempty`; it does not
  mutate the process-global transport. Invalid values fail before HTTP.
- Retain sampling provenance and request/schema fingerprints. Preserve the
  one-call pipeline, local concurrency and deadlines. No database migration,
  extra model call or shared gateway setting is required by the proposed shape.

The activity sends exact zero, records it with `reasoning_effort: none`, and
uses `vision-evaluate-v5`. The strict new decoder rejects the retired inferred
`period` model field while durable observations and Temporal results still read
it. No database migration, extra model call, shared gateway setting, tolerance,
deadline or concurrency change is involved.

The final exact runtime screen completed 39 calls in 9.081–13.681 seconds with
no request or schema error. All 15 natural calls retain their clock decisions;
Maitland-Niles loses only the minute mark on `+5′`. All explicit half, P/p,
extra-time, allowance and secondary-timer controls retain timing roles. Bare
numbers remain raw/unknown and cannot supply a period offset. Bare announced
allowances gain a plus sign, which does not change arithmetic. Graphics-only
soccer votes and Jesus's wrong-match limitation remain outside this fix.

Runtime prompt SHA-256 is
`ca4a080ebed8233d0b8447dbd1179e8d2aba51ec35e2eccc586924dbf30504c5`;
schema SHA-256 is
`278e1ca1a8f3397f5c495b49f342c6e406978f06bd0fc83f063f8aa3fb051f0a`.

| Final runtime report | SHA-256 |
|---|---|
| `runtime-natural-1.ndjson` | `e6f69570ebfa3722ea5846a5eea2f48cb1f3050674a2a6802b1b2d6f81695c46` |
| `runtime-natural-2.ndjson` | `9977374a391afe03d4092147d431703d58b1b601a65b3eccd9d504f2e4ecbe7e` |
| `runtime-natural-3.ndjson` | `fc8d4281147cec4d23fa10bc3edf845c53df480cd3290860a2b727a3b9596fa0` |
| `runtime-controls-1.ndjson` | `2ab9ad8bb917bd0b9a5e9f37f742856708653691d8c51b16f44d2e50e1d21454` |
| `runtime-controls-2.ndjson` | `bb101baecbe01437ce20c31b307c8e449752729cfe3de14f57c9cd7bdf00d0f8` |
| `runtime-p-controls.ndjson` | `8edc37fa38c6069fdcf989a711424b4438817146ae58708be1ff9128d5156731` |
| `runtime-extra-time.ndjson` | `0601f465855ee8275081ace21df47d0d63227fcd434c31d3ed4f8784fabe0b2f` |

Production rollout, natural-event validation and scoped historical recovery
remain separate steps.

Follow-up artifacts are under `scratch-audit-2026-09-18/12b-explicit-period/`.
Each report binds the prompt/schema/images and records temperature when an
override is used. The local README and runners record the staged call budgets.
The user's Control rollout coordination stopped the original disposable caller.
After Control release `2026-09-18.3` passed its acceptance plan, the missing
preselected cases resumed into new output files. Completed calls were not
repeated. The final runtime screen then used separate reports and the actual
application request path.
