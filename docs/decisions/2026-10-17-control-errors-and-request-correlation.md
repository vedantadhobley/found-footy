# Control errors and request correlation stay at the HTTP boundary

Status: implemented; production rollout pending.

## Context

Control admits two 12B requests, independently of Found Footy's four outstanding
HTTP requests across two workers. The application must not infer GPU slots or
implement another model scheduler. The existing LLM adapter still interpreted
every HTTP 503 as the former direct llama.cpp concurrency-cap response.
It also discarded Control's request ID and retry guidance.

The October 17 observation found 937 successful OCR calls and no failures.
This change repairs the contract and makes future contention diagnosable; it
does not claim to fix a failure reproduced in that successful sample.

## Decision

- HTTP 429 plus `capacity_exceeded` means shared admission capacity. Unknown
  429 codes remain generic limits. HTTP 503 means unavailable, never proof of
  saturation. Preserve bounded error codes rather than guessing from messages.
- Read `X-Request-ID` and `Retry-After` from the pinned SDK's response headers,
  which remain accessible on error and decode-failure paths. Do not add an HTTP
  transport, body copy, second request, or SDK dependency for metadata access.
- `ResponseInfo` retains only an opaque ASCII request ID (128 bytes), error
  code (64 bytes), SDK-reported error status, and parsed retry duration. Invalid
  identifiers are omitted. Missing data remains unknown. An error with response
  metadata wraps the original typed cause without breaking `errors.Is/As`.
- Valid delay-seconds or future HTTP-date retry guidance on retryable capacity,
  generic-limit, or unavailable results becomes Temporal `NextRetryDelay`.
  The adapter never sleeps or retries. Invalid, expired, negative or overflowing
  guidance leaves the existing backoff intact. A valid positive hint overrides
  the ordinary next interval, not the maximum attempt count. Permanent and
  timeout/cancellation failures do not acquire a retry delay from metadata.
- Success/error adapter logs retain `model_request_id`, `gateway_error_code`,
  `http_status` and `retry_after_ms`. Success calls log at INFO so the join is
  available in ordinary production logs. These identifiers are not metric labels.
- Activity results and terminal failure details retain the bounded model-response
  metadata. Vision-phase workflow logs link the request ID to the existing
  event/candidate identity. Old results without this optional field remain valid.

## Timing ownership

Found Footy's request timer includes gateway processing, admission, backend work,
network and response decoding. It is not inference duration.

Control already logs `admission granted` with `request_id` and `waited_ms`, and
`dispatched` with the same ID, `waited_ms` and `ms`. The former is admission wait;
the latter `ms` is backend HTTP/response-forwarding duration. It can still include
model-runtime queueing and transport: it is not pure GPU execution or separate
prefill/decode time. Correlate logs by ID, not by aggregate-average subtraction.

The current public response has no gateway timing breakdown. No synthetic queue
or execution measurement is added to Found Footy. Direct per-call phase metrics
need a separately agreed Control response-timing contract; runtime execution
timings need runtime-owned measurements. Requests that end before headers arrive
may have no observable gateway ID. A final Temporal-owned timeout cannot prove
the response metadata of the interrupted attempt.

No model request, prompt, acceptance threshold, concurrency cap, deadline,
production configuration, schema, public API, NATS contract or Control deployment
changes in this slice. Tests use synthetic HTTP responses and Temporal fixtures.

## Verification

Both modules passed `make check-short`. Focused race tests passed for the LLM
adapter, vision activities and workflows. A read-only production check confirmed
Temporal server 1.24.3 supports the retry-delay field. Existing Control text logs
also supplied a completed request with `waited_ms=5234` and backend `ms=10866`
under one request ID. No inference was triggered to obtain that evidence.

These checks validate local source and the existing producer evidence, not a
deployed end-to-end Found Footy correlation. Production remains unchanged.

The [operations guide](../operations.md#ocr-request-correlation) owns the read-only
diagnostic procedure. [FF-087](../todo.md#ff-087--vision-failure-exhaustion-discards-the-actionable-cause)
retains the validation and Control-handoff boundary.
