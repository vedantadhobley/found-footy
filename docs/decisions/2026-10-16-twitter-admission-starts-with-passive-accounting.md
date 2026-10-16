# Twitter admission starts with passive request accounting

**Date:** 2026-10-16. **Status:** implemented locally; not deployed.
**Issue:** FF-038. Design: [shared admission](../design/proposals/twitter-shared-admission.md).

## Decision

Collect actual browser-observed timeline request events before implementing
shared admission. Search counts and scroll counts are not upstream-request
counts. The last response alone hides mid-scan failures and header ordering.

Add optional `SearchEvidence.timeline` to the existing internal contract.
Each scan has a random identity unrelated to the login/query, start/snapshot
times, request/response/finish/failure totals, 429/5xx totals, and at most 64
request records. Join events by the SDK request object, not URL. Records have
scan-local ordinals, monotonic event-processing offsets, status, transport
failure flag and parsed non-negative quota values. Missing/invalid quota values
stay unknown; an actual remaining zero stays zero.

Beyond the record cap, aggregate counts continue, dropped records are explicit,
and response/terminal events without retained dispatch identity are counted as
unmatched. Unfinished records remain unfinished at snapshot. These are browser
observations, not proof that X charged quota or received every dispatched request.

Emit one bounded `twitter_timeline_measured` summary through the existing
browser diagnostic sink before normal page cleanup, even if the caller has
disconnected. Return accounting in success/error evidence and preserve it in
client logs, activity history, latest progress metadata, and workflow search
measurements where existing paths carry evidence. Copies share `scan_id` and
must not be summed as independent requests. Navigation failures can occur
after Browser.Navigate has already closed its failed page; no new wait/drain
is added to reinterpret those events.

## Boundaries

- No routing hook, controller round trip, extra X request, body download,
  per-request driver RPC, new sleep, or database schema. Playwright listener
  registration uses its ordinary driver subscription messages.
- No change to healthy search concurrency, scrolling, cutoff, retry policy,
  result-state classification, usable-search accounting, or public API/NATS.
- No raw request URL, cursor, query, body, credentials or arbitrary failure
  text in the new accounting records. Existing bounded page evidence remains.
- No new metric labels or admission dependency. Bounded bookkeeping, response
  encoding and diagnostic output still have a small local cost; this is not
  a claim of zero overhead.
- Whole-search serialization is not required by quota safety. Its earlier
  recommendation is withdrawn pending evidence; scarce-request scheduling
  needs review before implementation, not silent adoption of that restriction.

## Acceptance

Synthetic tests cover shared URLs, reordered responses, body failure after
headers, unrelated endpoints, absent dispatch, unfinished requests, record
truncation, concurrent callbacks/snapshots, navigation failure with a canceled
caller, old wire shapes, and unchanged rendered-then-429/503 behavior. Fixtures
panic on unexpected routing/body-fetch calls. A Go-only benchmark measures
bookkeeping separately from browser/network/log-output latency.

October 16 local validation: `make check-short` passed across both Go modules;
focused race checks passed for contract, browser, HTTP client and research
characterization. On luv's Ryzen AI MAX+ 395, the Go-only eight-request benchmark
measured 2,616 ns/scan, 3,168 bytes/scan and 59 allocations/scan using the pinned
Go 1.25.11 container (`-benchtime=300ms`). This excludes listener setup, browser,
response encoding and diagnostic stdout; it is not an end-to-end latency claim.

Natural validation after an explicitly approved telemetry release must inspect
requests per scan, quota windows, response ordering, missing/unmatched evidence,
and first-candidate latency. Admission and enforcement remain separate work.
