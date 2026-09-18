# Restored Gemma 12B validation — September 18 UTC

## Result

This records the initial restoration trial. The later
[local prompt implementation and 37-call repeat](./gemma-12b-period-prompt-2026-09-18.md)
retains remaining extraction failures; the
[Control handoff](./control-ocr-admission-handoff-2026-09-18.md) records the
separate admission source review. Neither follow-up is a production rollout.

The restored model is reachable and runs Found Footy's real request contract.
Thirty sequential requests completed without transport/JSON errors; one of
eight requests in the four-client repeat hit the 60-second HTTP deadline.
12B reads these selected scorebugs substantially better than the tested E4B
configurations, but current-prompt extraction and shared admission still have
separate gaps. No production prompt or application setting was changed.

## Scope and method

The user restored `gemma-4-12b` through Control, replacing E4B while retaining
31B and the other resident models. A fresh public catalog and metrics check
confirmed the real 12B ID, image and strict-schema support, four backend slots
and one shared cold-long permit. The user reports four 8,192-token slots;
this application-side check does not inspect or change node configuration.

Found Footy validation uses its actual LLM adapter, unchanged current prompt,
strict schema and `vision-evaluate-v4`. The comparison's `period_roles`
wording already exists in the isolated benchmark; it is not the runtime prompt.
Reasoning is `none`, sampling and output budget remain model-owned (catalog
default 4,096 output tokens), HTTP deadline is 60 seconds, and failed calls
are retained without retries. API expectations and gold never enter requests.

The natural corpus is the same five original three-frame sets used for the
[E4B trials](./gemma-e4b-vision-trial-2026-09-18.md). No crops, enlargement,
literal-label schema, or image-order adapter experiment is carried forward.
The ten synthetic controls are separately labeled field-recognition tests,
not natural footage or content-accuracy evidence. All natural samples lack
visible half labels, so positive-label controls remain necessary.

This pass changes no runtime code, production application configuration,
model deployment, database, media, workflow or historical verdict. Control's
restoration is the user's separate infrastructure operation.

## Sequential natural footage

All ten calls completed without transport or JSON errors in 10.562–13.519
seconds. Each row contains one call per prompt, not an error-rate estimate.

| Sample | Current prompt | Existing period-role experiment |
|---|---|---|
| Gueye, 45+2′ | Reads 46:35/37/39 but omits visible +4; genuine clip rejected as H2 | Correct clocks, +4 and null period; v4 verifies |
| Yamal, wrong-half 89′ candidate | Exact timing; rejected | Exact timing; rejected |
| Jesus, wrong fixture | Exact timing; matching-clock gate verifies | Same |
| Maitland-Niles, 90+6′ | Correct clocks/roles, omits the prime from +5′; verified | Correct clocks/roles, ASCII apostrophe instead of prime; verified |
| Fernández, 45′ | Correct clocks/+1 but invents 1H; verified | Exact timing, period null; verified |

Literal timing agreement is 2/5 for current wording and 4/5 for experimental
wording. The experimental mismatch is punctuation only; current Gueye and
Fernández failures are substantive field errors. Jesus demonstrates the
existing semantic limit: correct clock extraction does not identify the
fixture or prove that the requested goal is in the clip.

## Synthetic field controls

All twenty calls completed without transport or JSON errors in 8.870–10.084
seconds. Current wording matches all timing fields in eight of ten controls;
experimental wording matches ten of ten. Current wording maps plain `1re` to
`ET1` and plain `2e` to `ET2`; experimental wording maps them to `1H` and `2H`.
Both correctly handle `1st`/`2nd`, absent labels, running +1/+2 allowances,
independent secondary timers at 45:00/90:00, and the selected reset-half clock
with a simultaneous allowance and secondary timer.

These are one-layout diagrams. They do not establish natural-label coverage,
general recognition rates or broad content acceptance. Current wording calls
all ten graphics-only controls soccer footage; experimental wording does so
for nine. That existing content weakness is not fixed by accurate clocks.
The September 17 experiment also recorded a `1re` error with experimental
wording; this successful repeat does not establish that the error is gone.

## Four-client repeat and public-gateway latency

Four isolated clients start together, each issuing its current-prompt request
and then its experimental-prompt request. This is eight total requests with
at most four outstanding, not two synchronized waves or a backend-only test.
Each client preloads local files and uses the application adapter and normal
60-second deadline. No timeout is retried or erased.

| Natural sample | Current wording | Experimental wording |
|---|---|---|
| Gueye | 53.367 s, correct +4 and null period this time | 17.434 s, correct timing |
| Yamal | 10.694 s, exact timing, rejected | 21.680 s, exact timing, rejected |
| Maitland-Niles | 21.220 s, correct timing except omitted prime | 21.509 s, same punctuation difference |
| Fernández | 60.059 s, HTTP context deadline, no usable observation | 21.855 s, exact timing, null period |

The current Gueye reading changed from missing +4 to retaining it. This is
evidence of variability, not evidence that the concurrent load fixed OCR.
Model-owned sampling is nondeterministic. Both experimental Gueye/Fernández
reads retain the expected fields, but these small repeats are not broad
accuracy qualification.

The gateway classifies all eight requests as `cold_long`. Acquisitions increase
from 32 to 40, and cumulative acquired-request admission wait rises from
0.000118 to 145.398631 seconds. A during-run sample shows one backend slot in
use out of four, with the sole shared cold-long permit occupied. Afterward,
both return to zero. These metrics establish substantial gateway queueing;
they do not give request-by-request inference spans for the timed-out call.

Later requests from clients that finished early complete while other clients'
initial requests remain pending. Review queue fairness as well as permit
ownership before choosing an admission change; do not assume four advertised
backend slots yield four simultaneous application OCR calls. The user's
reported 25.25-second four-call median came from a different experiment and
does not establish this gateway path's tail latency.

## Disposition

- Keep the model restoration separate from prompt acceptance. The model now
  serves the existing real ID; this pass does not reconfigure or restart
  application workers and does not verify a natural production workflow.
- Investigate a focused period/allowance prompt change before adding dedicated
  OCR infrastructure. Existing experimental wording is promising; it still
  needs repeat and regression coverage, including previously tested label
  variants, before promotion. No clock tolerance or acceptance gate changed.
- Coordinate public-gateway admission with Control under FF-037. The exact
  application workload can time out with only four callers even though all
  thirty sequential calls succeed. Do not silently increase deadlines, bypass
  admission, or interpret failed calls as content rejections.
- FF-097 historical recovery remains separately scoped and unexecuted. Model
  restoration does not restart terminal failed/rejected candidate histories.
  The four Gueye candidate IDs still require an exact selector/dry run and
  explicit production mutation approval.

## Evidence and verification

Ignored artifacts are under `scratch-audit-2026-09-18/12b-restored/`.
The original natural manifest remains unchanged, SHA-256
`ec608431626aad5f8d66f2b34b5822bc0a61939a1cae4ee733cd437b5edac45d`.
Each result binds model, prompt, schema and image hashes, raw observations,
token usage, elapsed time, differences and evaluation. Explicit completion
markers and per-call errors determine completion, not process exit alone.

All six report completion markers are present (10 + 20 + four times 2 calls).
The compiler and cached scoped race suites passed for the benchmark, its
fixtures, the LLM adapter, vision domain and vision activity. Runtime prompt,
schema and adapter files have no changes from this pass. The selected cases do
not constitute a fresh natural-content corpus, a general error rate, or a
high-percentile latency estimate.

| Artifact | SHA-256 |
|---|---|
| `natural.ndjson` | `f576e92ae6857c4577a862fea2d82b7ec8b6e68300d61ebf0cff4be2897be08f` |
| `controls.ndjson` | `b7429bfa6966b06e3d243d0f15351100eae7a3d357b30b8fd702f2bc00a7ed56` |
| `concurrent-gueye_running_first_half_stoppage.ndjson` | `20bad0768f05579b7a1f2505d0287f4a43a7493fa244945d141cf53dd79228cd` |
| `concurrent-yamal_first_half_penalty_miss.ndjson` | `7699c4ed26f06aec631ad1979c6f89f40b8fdff6f85e91dd1f231d17fbae56ca` |
| `concurrent-maitland_running_plus_five.ndjson` | `b8bc4f5cf64088ac03f5b8534786838bcbb68f566bd4ea0d6237803d3fdfae5a` |
| `concurrent-fernandez_running_plus_one.ndjson` | `47be7f7b2530e42ed05693f4c88d44818553c73ab1fe7d3aa638d58b34ab43de` |
