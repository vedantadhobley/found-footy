# Vision failures retain stage and timeout kind

## Context

The [September 4 production burst](../design/audits/pre-rollout-evidence-2026-09-08.md#ff-087-vision-failure-burst-and-missing-durable-cause)
left 167 failed candidates with no durable cause. Logs distinguished request
timeouts and late activity completions, but completed Temporal histories expire
after one day. Exact followers magnified candidate counts without representing
independent validation attempts. FF-060 already solved the equivalent durable
diagnostic gap for downloads.

## Decision

`ValidateClip` carries a bounded `FailureDetail` through Temporal:

- Stage: `scratch`, `staging_fetch`, `probe`, `frame_extract`,
  `model_admission`, `model_request`, `response_parse`, or fallback `activity`.
- Class: an allowlisted typed cause, such as `timeout`, `rate_limited`,
  `capacity`, `storage`, `invalid_json`, or `unknown`. The complete registry is
  [the activity contract](../../internal/activity/vision/failure.go).
- Optional `timeout_type`: `start_to_close`, `schedule_to_start`,
  `schedule_to_close`, `heartbeat`, or `unknown`, only for Temporal timeouts.

Retryable errors use application type `vision_failure`. Permanent model errors
retain the existing `vision_llm_permanent` type and non-retryable flag. No
classification changes retry counts, admission limits, prompts, acceptance
rules, cleanup, or publication.

The final Temporal timeout takes precedence over any previous retry's
application-error cause. It is `activity/timeout`; we cannot infer the expired
attempt's stage from a prior attempt. Untyped, malformed, or unregistered detail
falls back to `activity/unknown`, without parsing raw messages.

New EventWorkflow histories use `ff-087-vision-failure-detail`. After exhaustion
they retain `failed/vision_error` and persist the detail under
`outcome_detail.failure`. Exact followers receive the same final evidence,
without another validation budget. Older histories retain their original null
terminal payload. No schema migration or historical data rewrite is required.

The LLM adapter marks interruption before HTTP with `ErrLocalAdmission`, while
preserving the context error. It measures local semaphore waiting separately
from HTTP duration, including calls canceled before HTTP. Waiting calls and
admitted calls have separate gauges; the existing concurrent-call gauge now
measures its documented admitted-call meaning. Existing call-duration and
`elapsed_ms` semantics remain HTTP request time.

## Boundaries

Raw errors remain available in activity history and adapter logs, not in the
candidate failure detail. Prompts, response bodies, and signed URLs are not
added to durable diagnostics or metric labels. Logs contain bounded stage,
class, timeout type, and timing fields; metrics add only a two-value admission
outcome label.

This is diagnostic work, not the saturation fix. Local admission plus remote
waiting and inference still share the existing three-minute activity deadline;
the request remains capped at 60 seconds. FF-037 and Control own the subsequent
measured admission/deadline decision. Neither that policy nor FF-075 circuit
enforcement changes here.
