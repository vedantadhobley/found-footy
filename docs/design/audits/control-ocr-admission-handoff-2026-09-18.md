# Control handoff: Found Footy OCR admission

## Request

Review public-gateway admission for `gemma-4-12b` on Joi. Found Footy can
produce four simultaneous three-image validation requests. A backend-only
four-call benchmark does not qualify that public path: the
[application trial](./gemma-12b-restored-validation-2026-09-18.md#four-client-repeat-and-public-gateway-latency)
timed out one of eight calls under the normal 60-second HTTP deadline.
This is FF-037; it is separate from prompt extraction and FF-097 recovery.

Control owns any shared scheduler, classifier or deployment change. This
handoff requests review and a proposed fix, not an automatic settings change.
Cross-project ownership is routed through Found Footy's
[agent context](../../../AGENTS.md#cross-cutting-context) and dhobley's topology.

## Measured application workload

- Public endpoint `http://control-joi.luv/v1/chat/completions`, exact real model
  ID `gemma-4-12b`; one message with three JPEGs, strict `frame_validation`
  JSON schema, `reasoning_effort: none`, sampling and output budget omitted.
- Thirty prior sequential requests completed without request errors. Natural
  three-frame requests took 10.562–13.519 seconds; diagrams took 8.870–10.084.
- Four isolated clients then started together. Each sent the baseline prompt
  and, after its result, the experimental prompt: eight calls, at most four
  outstanding. No retries or unrelated application mutations.
- One baseline call ended at 60.059 seconds with `context deadline exceeded`.
  Another baseline call needed 53.367 seconds. All six report completion
  markers and every result remain retained, including the failed call.
- All eight requests were classified `cold_long`. During the run, metrics
  showed one backend slot in use out of four and the one cold-long permit
  occupied. Acquisitions rose 32 → 40; cumulative acquired admission wait rose
  0.000118 → 145.398631 seconds. Both in-use counters returned to zero.

The metrics establish gateway queueing, not a complete request-specific trace
of the failed call. Later requests from early finishers completed before some
initial requests. Do not extrapolate a population timeout rate from eight calls.

Found Footy's HTTP timeout starts **after** its local semaphore acquisition.
The HTTP budget therefore includes gateway classification, gateway queueing,
and inference. Its surrounding vision activity also has a three-minute
start-to-close budget, so enlarging HTTP deadlines alone is not a safe fix.
The local two-worker concurrency allocation and all deadlines are unchanged.

## Source review

Read-only review of Control commit `a2311f35b20ad038cbeb48ef9f64524b1312bb57`;
the reviewed files had no local changes. This identifies the source snapshot,
not independent verification of the deployed image's revision.

1. `shared/inference/gateway/classification.go`: `promptClassifier.classify`
   obtains the formatted prompt, tokenizes it and compares its length to a
   threshold. It does not observe a backend KV-cache miss. Thus `cold_long`
   is a conservative length-based class, not proof that this request is cold.
2. `shared/inference/gateway/main.go`: `Registry.acquire` loops on
   `tryAcquire`; `release` closes a shared change channel and wakes all
   waiters. There is no FIFO waiter queue or age preference. Mutex acquisition
   order, not arrival order, selects the next eligible request. Backend
   round-robin selection does not provide fairness between callers.
3. The proxy defers `reg.release(lease)` until its handler returns. The node's
   cold-long permit is retained through upstream decoding and response-body
   transfer, not released when prompt processing ends.
4. `docs/admission.md` documents the 128-token mirror threshold inherited
   from Nexus measurements and a 500-ms interactive decode-gap objective.
   That is a shared policy constraint, not permission for Found Footy to
   disable the limiter or claim cached requests are exempt.

These source mechanisms explain why four configured slots can remain mostly
unused for this workload. The source also supports the observed reordering;
it does not prove every millisecond of the timed-out request's lifecycle.

## Recommended next slice

1. Add deterministic fairness tests: hold the only cold permit, enqueue an
   older eligible waiter, then submit fresh work after release. Bound overtaking
   while preserving cancellation, readiness changes and heterogeneous capacity.
   A single strict global FIFO must not strand unrelated eligible models
   behind an unavailable or heavier request.
2. Select fair, bounded admission with explicit overload/cancellation outcomes.
   Preserve memory and mixed-workload protections. Do not expose request IDs as
   metric labels or log prompts/images; retain bounded phase timings and
   cancellation counts, including requests that never acquire a permit.
3. Re-measure the exact three-image workload through the **public gateway**,
   including repeated four-client traffic and a mixed-model load fixture.
   Record queue wait separately from backend time and interactive decode gaps.
4. Decide permit policy from those results. Fairness can prevent repeated
   overtaking; it cannot make a serialized service run four calls concurrently.
   Changing thresholds, slot counts or full-request permit ownership requires
   independent evidence and an explicit Control decision/deployment.

Do not make Found Footy sleep/retry more aggressively, bypass the gateway,
globally extend its timeout, or add another OCR service to hide admission delay.
No retry-count, event workflow, clock gate or historical repair is in scope.

## Completion

Control deployed eligibility-aware bounded-overtaking fairness to Joi as
release `2026-09-18.3` (image digest prefix `41d9eabd`). Model services, permit
counts, thresholds and Nexus remained unchanged. Its public acceptance run
completed all 24 frozen OCR calls without errors or 60-second timeouts;
latency was 10.577–43.847 seconds and measured gateway wait peaked at 32.983
seconds. Mixed OCR, 31B, GPT-OSS, embedding and Whisper traffic passed. The
queued cancellation never reached a backend, all waiters/counters drained,
and there were no container restarts or OOM events. Final Control validation
passed.

Six evaluator rejections are retained extraction/content findings, not
admission failures. Control owns its source/evidence in
`docs/admission-fairness.md` and the `gateway-fairness-joi-20260918-c`
artifacts. The Control handoff reported that no source commit was created;
source provenance and any required follow-up commit remain owned by Control.
Found Footy resumed its paused caller and separately qualified the literal-
label V5 contract; no Found Footy deadline or concurrency change was needed.

## Reproduction inputs

The linked audit binds each report by SHA-256. Local source/media are under
`scratch-audit-2026-09-18/12b-restored/` and the unchanged manifests referenced
there. Each concurrent manifest contains exactly one natural sample. The
benchmark's frozen `--comparison period-labels` reproduces the original two
prompts, independent of the new runtime prompt; `--comparison current` tests
the current default once per sample. Both require explicit `--run-model`.

Use pinned, memory-capped containers, local images mounted read-only and a new
report path. Do not load production environment files. Coordinate new inference
load separately; this review has not rerun the failed concurrency experiment.
