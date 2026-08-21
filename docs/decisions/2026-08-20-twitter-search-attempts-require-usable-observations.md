# Twitter search attempts require usable observations

## Context

EventWorkflow promises 15 Twitter searches per event. The browser service
returned HTTP 200 when no tweet article appeared within ten seconds, and the
workflow also checkpointed a logical attempt after all activity retries failed.
The 2026-08-19 MLS burst therefore consumed 61 search slots while X returned no
usable feed.

A retry must not reduce recall, multiply browser traffic, or hold fixture
completion forever. The browser result must also remain diagnosable after its
short-lived Firefox container is reaped.

## Decision

The browser service and worker share one typed search contract in
`internal/contract/twittersearch`. Every page result has one bounded state:

- `rendered` and `explicit_empty` are usable observations;
- `login`, `upstream_error`, and `unknown_timeout` are unavailable.

The service records only bounded, secret-free evidence: final route, page title,
DOM state bits, SearchTimeline status/failure, and X rate-limit headers when
present. It never records page or response bodies, request headers, cookies, or
tokens.

For new Temporal histories, SearchTweets has one activity attempt. EventWorkflow
owns the retry cadence and maintains two monotonic counters:

1. usable searches, bounded by `DISCOVERY_MAX_ATTEMPTS`;
2. unavailable probes, bounded by
   `DISCOVERY_MAX_UNAVAILABLE_ATTEMPTS`.

Both default to 15. An unavailable probe waits the normal one-minute spacing,
advances only the unavailable counter, and preserves the current logical search
number. The workflow checkpoints both counters plus the latest state/evidence
in `event_downstream_workflows.metadata`. Recovery restores all of them.

When the unavailable budget is exhausted, the workflow drains candidate work,
completes its checklist, and reports `twitter_unavailable` if no candidate was
processed. It does not block fixture completion indefinitely.

## Replay and rollout

The `ff-061-search-availability` Temporal version marker preserves the previous
three/four-attempt activity policy and checkpoint behavior for histories that
started before this change. A classified non-2xx response remains a retryable
Temporal application error whose details carry the bounded state/evidence.
Old histories therefore retain their activity retries; new histories decode
the details after their single activity call. During a rolling release, an
older browser response without `result_state` remains usable except for the
historical `stop_reason=feed_timeout` shape, which is unavailable.

This supersedes the nested SearchTweets retry shape in
[rebuild-plan §5 W3](../design/rebuild-plan.md#workflow-3-discoveryworkflow) for
new histories. Temporal remains the durable retry owner; the retry unit moves
from an opaque activity chain to an explicit workflow-level classified probe.

## Consequences

- Fifteen means 15 usable X observations, not 15 HTTP responses.
- New histories remain bounded at 30 SearchTweets activity executions with
  default configuration. A failed per-event transport can add one static-service
  HTTP request inside an execution.
- Explicit empty and known error selectors can return before the ten-second
  article timeout.
- `found_footy_twitter_calls_total{op="search",outcome=...}` uses the bounded
  result state when available.
- The next natural suppression window can distinguish upstream status,
  rate-header, DOM-interstitial, login, and unexplained-timeout evidence.
