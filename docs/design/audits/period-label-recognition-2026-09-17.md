# Period-label recognition and Barcelona admissions — September 17

## Scope

Read-only diagnosis and an isolated model benchmark for
[FF-003](../../todo.md#ff-003--candidate-can-pass-without-exact-event-semantic-evidence).
The original diagnosis and benchmark changed no runtime policy or production
state. The [FF-094 implementation](./clock-normalization-implementation-2026-09-15.md)
was local and undeployed; the experiment used that v2 evaluator. The later
[implementation follow-up](#implementation-follow-up) removed unsupported
period offsets as v3, deployed in the
[September 17 rollout](../../history/clock-validation-rollout-2026-09-17.md).

The original user-described reset-clock broadcasts were `05:25 2nd` and
`28:56 1st`. `1P`/`2P` were proposed as additional labels on September 17,
not recovered from those original examples.

## Confirmed production failures

All three manually registered Barcelona recovery workflows completed normally.
Raphinha ended `candidates_no_assets`; Jesus and Yamal ended `assets_surfaced`.
Pipeline completion is not proof of clip correctness. Workers remained on
`abfea7a`; both wrong admissions below retained `vision-evaluate-v1` evidence.

### Jesus 79′: wrong fixture, matching clock

- Event `96a507f2-ad12-48cb-bac4-044e63a6ef38`, share `s_80a1d7ebe2e8`,
  asset `19c8e7b7-3a29-540d-b64e-6faa5c31b17e`.
- [Source tweet](https://x.com/brotatochipsss/status/2100450780964938000)
  describes a joke comparing Ferran Torres with Jesus. The stored query was
  `(gabriel OR jesus OR Barcelona) filter:videos`.
- Original sampled frame visibly reads `MIA 1–0 CAZ`, not Barcelona–Racing.
  Model readings `77:27`, `77:29`, `77:31` were accurate.
- Expected completed minute 78 with ±1 admits 77; no match/team/action
  authenticity check runs. The verdict was verified. With both surfaced
  clips at popularity one, its 5,266,416-byte file outranks the legitimate
  candidate's 1,699,025 bytes under the existing tie-breaker.

### Yamal 89′: wrong half soft-kept without a half label

- Event `c1499da3-81a4-4c9b-9a8d-72ca551d3bce`, share `s_f1fab5939d08`,
  asset `63638a1c-0328-5da8-a971-4259317cf6b4`.
- [Source tweet](https://x.com/TactiqueBavarde/status/2100456754395926569)
  discusses Yamal's missed penalty. The stored query was
  `(lamine OR yamal OR Barcelona) filter:videos`.
- Original frames show that earlier miss, `BAR 4–1 RAC`, with clocks
  `44:48`, `44:51`, `44:53`, and **no explicit half label**. The model
  transcribed those clocks correctly and returned `period: null`.
- The evaluator's `relativeClockCouldMatch` tries `44 + 45 = 89` against
  expected minute 88 ±1, despite the absence of visible period evidence.
  It preserves the clip as unverified, and public reads can display it.
- The valid first-ranked clip reads `88:43`, `88:50`, `88:58`. Both public
  clips have support one, so the popularity singleton filter does not hide
  the unverified clip.

These are independent admissions, not deduplication replacement errors.
The historical search floor broadens eligible posts, but replay did not skip
vision. Neither the current ±1 rule nor unlabelled relative soft-keep changed
in FF-094. Initial characterization tests reproduced both admissions with v2;
the later target regression rejects Yamal under v3.

## Method

[Benchmark code](../../../scripts/benchmark_clock_prompt/README.md) uses the
real Found Footy LLM adapter, verified catalog ID `gemma-4-12b`, unchanged
structured schema, three images per request, no reasoning, one concurrent
call and a 60-second request limit. Expected API minute and gold annotations
never enter the model request. There is one call per sample/variant, no
retrying for a preferred answer, and failed calls remain recorded.

The two frozen prompt variants are:

- **default:** local FF-094 prompt, not the older deployed v1 prompt;
- **period_roles:** changes only the period instruction to name ordinal
  variants and distinguish period labels from scores, channel marks and
  allowances. Main/added/secondary/content instructions remain byte-identical.

The initial corpus has eighteen code-generated SVG scorebug diagrams plus
four original-media frame sets. The `P`/`p` follow-up adds four diagrams with
the same frozen prompts. Synthetic main or secondary clocks advance separately.
Isolated bare digits deliberately have conservative **unknown** gold labels:
these test the proposed evidence rule, not an established broadcast convention.
All diagrams use one layout and font. Natural half-label-positive coverage is
absent; existing original 1st/2nd frames were not available in this local corpus.

## Initial comparison findings

The initial 44 calls completed without request errors. Each row below is a
sample family, not a population accuracy estimate.

| Visible control | Default | Period-role wording |
|---|---|---|
| `1st`, `2nd`, superscript `1er`/`2e`, `HALF 1`/`HALF 2` | Correct half | Correct half |
| Plain `1re` | Incorrect `ET1` in all three frames | Same error |
| Plain `2e` | Incorrect `ET2` in all three frames | Correct `2H` |
| Unexplained isolated `1`/`2` | Infers `1H`/`2H` | Keeps null |
| No label beside a low clock | Period null | Period null |
| Running main clock with `+1`/`+2` | Correct allowance, period null | Same |
| Number `1`/`2` explicitly labeled ADDED TIME | Adds an unprinted `+`, but uses allowance field | Literal `1`/`2` in allowance field |
| Frozen main plus running secondary and `+2` | Correct independent fields | Same |
| Reset `45:00 2e`, secondary `01:23/26/29`, allowance `+2` | Correct half/timers but omits allowance | Retains all four fields |

The follow-up's eight calls also completed without errors. `1P`, `2P`, `1p`
and `2p` all mapped to the intended half in all three frames, under both
unchanged prompt variants. This establishes handling of those four constructed
examples, not their prevalence in actual broadcasts. Together the runs comprise
52 calls over 26 cases, with observed call durations of 8.737–13.505 seconds.

The actual Yamal and Jesus frame sets retain the correct transcription with
both prompts, but still produce unverified/verified respectively under the
unchanged evaluator. Better label wording alone does not fix either admission.

For Maitland-Niles, both variants preserve `95:04/15/27`, put `+5` in
`added`, and leave secondary null. Both omit the visible prime. For Fernández,
both preserve `45:17/25/33` and `+1`; the default invents `period: 1H`
without a visible label, while period-role wording leaves it null. We cannot
tell whether the invented half came from the clock or the allowance. Both
still verify through the conventional continuous clock.

The model marks graphics-only synthetic controls as soccer footage. Their
content votes and resulting clip verdicts are not natural-video acceptance
evidence; field recognition is the measurement here.

## Conclusions and remaining work

1. Require evidence for a half-relative offset; a matching hypothetical
   offset must not rescue a readable contradictory clock. This acceptance
   change was proposed by the benchmark and implemented in the follow-up below.
2. Preserve independent timing roles and ordinary continuous-clock behavior.
   Removing unlabelled offset fallback does not require changing added-time
   composition. Do not confuse an announced allowance with a period or an
   elapsed secondary timer.
3. Do not ship this experimental prompt as validated. It helps some controls
   but still confuses `1re` with extra time. Check natural labelled footage,
   small/obscured labels and explicit extra-time positive controls before
   claiming reliable period normalization. Retaining literal label evidence
   is a possible follow-up, not a new schema decision here.
4. Jesus requires independent event/match relevance evidence. Narrowing a
   clock window removes this example but cannot prove match identity.

The synthetic corpus cannot establish a global false-positive/negative rate,
that every broadcaster uses these labels, or that stronger wording always
preserves genuine no-clock or extra-time clips. No historical media removal
or revalidation is authorized by these results.

## Verification and artifacts

Scoped benchmark/fixture tests, clock-domain race tests, `go vet`, build and
pinned lint passed. Unit tests pin period-only prompt changes, literal role
comparisons, controlled fixture generation, no-overwrite output and the
original wrong-admission characterizations. No production environment or
database client is loaded by the tool.

Initial raw output and rendered controls remain under
`/tmp/ff-clock-period-controls-20260917/`; the `P`/`p` follow-up uses
`/tmp/ff-clock-p-period-controls-20260917/`. Original incident media is under
`/tmp/ff-barca-admission.vYbUXznT/`. These temporary paths are diagnostic
artifacts, not operational data locations.

The complete secret-free
[archived observations](../../../scripts/benchmark_clock_prompt/testdata/period-label-results-20260917.ndjson)
retain every response, comparison/manifest fingerprint, image hash, elapsed
time and evaluator result, including failures of the proposed wording. Four
explicit completion records account for all 52 calls. Rebuilding the SVGs is
deterministic; natural images still require the retained local originals.
Archive SHA-256: `7ee5ed47a05b370dbc7b639e2914f2f43df5bda1b9db837284f619b11e6a3c21`.

Prompt fingerprints:

- Default: `7b80787118e7ec3f8114a1a8bab2a431831b792bc228cb544fc3e5ee9f38b0e7`.
- Period roles: `f28544e26d7e2b8fb119fd5b8f72128af9c47b6a95b2d502d5c83482f8667ec7`.
- Schema: `00f178fc55b7fbd3a407eb83dbf28b1bed5d70b4d574ffd980bd9e7a34be763c`.

## Implementation follow-up

The user approved removing unsupported period-offset guesses after reviewing
the benchmark. Local `vision-evaluate-v3` removes `relativeClockCouldMatch`
and its soft-keep branch, including H2, ET1 and both ET2 conventions. It retains
FF-094 normalization, observed-period resets and ordinary continuous clocks.
No experimental period wording, tolerance, added-time rule or public ranking
change is included. See the [decision](../../decisions/2026-09-17-relative-clocks-require-visible-period.md).

The new Yamal and four reset-convention regressions failed on v2 before the
removal and pass afterward. Existing added-time, no-clock, single-match and
period-boundary suites still pass. Evidence tests preserve old accepted v1/v2
results; current replay accepts those known versions and rejects unknown ones.

Offline replay, without new inference:

- **September 17 archive:** all 52 saved responses are evaluated with their
  original expected times. Exactly four results change from unverified to
  rejected: Yamal and the no-half-label control under both prompt variants.
  All other evaluation fields and the remaining 48 full evaluations match.
  That includes `1P`/`2P` and lowercase controls, explicit half labels and
  allowance/secondary examples. The synthetic content classifications remain
  a known limit, not new acceptance evidence.
- **September 15 corpus:** captured current v2 results before editing, then
  replayed the identical local files under v3. Across 159 natural accepted
  records, 3,429 clock rejects and 108 saved workflow observations, there are
  no incremental changes to outcomes, reasons or matched minutes. The lists
  of changes relative to historical production verdicts and affected saved
  shares are identical. Existing FF-094 differences remain; they are not new
  effects of this offset removal. Exclusions stay identical: four manual
  evaluations, 1,515 rejects without clock expectations and thirteen with
  changed expectations. History can overlap candidate rows, and the corpus
  predates this Barcelona incident.

The current implementation therefore closes this admission path, not the
broader FF-003 authenticity problem. Jesus still verifies on a matching clock.
A genuine reset broadcast with missing/unreadable period evidence may reject;
this is the deliberate tradeoff. The model can still misread or invent labels.
Old exact-byte acceptances are not revalidated; existing public clips remain
unchanged until a separately approved repair.

The application short suite, command builds, scoped vet and pinned lint passed.
Race tests passed for vision domain/activity, workflows and all three offline
clock tools, including the archived-response replay. No schema change or new
database integration run was needed for this pure evaluator change. The before/after reports
are `/tmp/ff-clock-v2-before-period-fix-20260917.json` and
`/tmp/ff-clock-v3-period-fix-replay-20260917.json`; input fingerprints are in
both reports. The immutable benchmark archive and its checksum above are
unchanged. The offline work performed no production mutation; the later
application rollout is recorded separately above.
