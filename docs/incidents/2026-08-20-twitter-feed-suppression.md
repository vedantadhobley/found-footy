# 2026-08-20 Twitter feed suppression

This report preserves the production evidence that confirmed FF-061. It is a
point-in-time incident record, not a second backlog. The fix and its natural
production validation are preserved in the
[2026-08-25 issue closures](../history/issue-closures-2026-08-25.md).

## Outcome

The Twitter service treats the absence of a tweet article after ten seconds as
successful `feed_timeout`. EventWorkflow then checkpoints the outer attempt.
An unavailable X feed can therefore consume the configured 15-search budget
without performing a usable search.

The 2026-08-19 MLS workload reproduced that failure across independent event
browsers. Retained host and container metrics rule out local resource
saturation. The synchronized loss, shared account and outbound IP, bounded
recovery, and request pressure make upstream account/IP enforcement or an X
search-backend circuit the leading cause. The exact X response remains
unproven because the browser service retained no page-state or internal-network
evidence.

## Implemented correction

FF-061 now classifies `rendered`, `explicit_empty`, `login`,
`upstream_error`, and `unknown_timeout` through one shared browser/worker
contract. Only the first two consume the 15-search budget. New workflow
histories issue one activity call per physical probe and maintain a separate
15-probe unavailable budget at the existing one-minute cadence.

The latest bounded final route/title, selector bits, SearchTimeline
status/failure, and rate-limit headers persist with both monotonic counters in
the downstream checklist metadata. No body, request header, cookie, or token is
retained. Exhausting the unavailable budget completes the checklist instead of
holding the fixture indefinitely.

See the [landed decision](../decisions/2026-08-20-twitter-search-attempts-require-usable-observations.md)
for retry, replay, and rollout semantics.

## Production evidence

- **Runtime:** production release
  `70fca8faef10007f4763e00a4766815367da313c`.
- **Sample:** 287 EventWorkflow search measurements over the retained 24-hour
  interval; 226 rendered normally and 61 returned `feed_timeout`.
- **Scope:** 20 event workflows ran; 12 experienced at least one timeout.
- **Classification:** all 61 timeout responses carried workflow outcome
  `passed`, zero articles, zero parsed tweets, and zero videos.
- **Recovery:** no browser restart, authentication action, or operator change
  preceded either recovery.

The failures formed two waves. Times are UTC; local EDT was four hours behind.

| Interval | Evidence |
|---|---|
| 2026-08-19 23:58:41–2026-08-20 00:01:03 | Six initial timeouts. Every completed search during 23:59 and 00:00 returned no feed; three of four rendered again during 00:01. |
| 2026-08-20 00:02:00–00:07:49 | Forty-seven consecutive searches rendered normally across unrelated queries. |
| 2026-08-20 00:07:55–00:16:03 | Fifty-five timeouts. Every completed search from 00:08 through 00:15 returned no feed across the active browsers. |
| 2026-08-20 00:16:28 onward | All active queries recovered; Messi returned 15 videos on the next attempt and the other browsers again rendered four to six initial articles. |

Representative attempt loss:

- Luighi: attempts 6–13 timed out; attempts 14–15 rendered and found no video.
- Gillier: attempts 7–13 timed out; attempts 14–15 rendered.
- Messi: attempts 2–8 timed out despite attempts 1 and 9 returning 16 and 15
  videos respectively.
- Cimermancic and Vassilev ended with attempts 13–15 and 14–15 timed out, so
  those workflows had no later recovery slot.

This rules out a query-specific empty result. It does not prove that a missing
clip existed during a lost slot, so candidate recall loss is possible rather
than measurable from the retained data.

## Post-release natural validation

The next match-day burst on 2026-08-24 produced 18 classified unavailable
probes between 20:16:49 and 20:35:15 UTC across Fulham, Chelsea, Schalke,
Malaga, and Roma queries. Every probe retained `upstream_error`, HTTP 429,
`x-rate-limit-limit=50`, `x-rate-limit-remaining=0`, and one of two reset
epochs: 20:19:48 or 20:35:11 UTC. This measures a shared account/IP timeline
bucket with a limit of 50 and a roughly 15-minute reset window; it is not a
query-specific empty feed.

The workflow boundary behaved as designed. All affected events still reached
15 usable searches, while the 18 unavailable probes were counted separately
and consumed no logical attempt. Loki reconciled exactly to 3,192 event-search
successes, five maintenance-canary successes, and 18 classified failures.
FF-061's production proof is complete. Any coordinated search-admission policy
now belongs to FF-038's fleet-controller boundary.

## Resource and lifecycle correlation

Prometheus retained 15-second node-exporter and cAdvisor samples across the
incident:

| Signal | Observed interval |
|---|---|
| luv CPU | 6.1% average; 12.8% maximum |
| luv load 1 | 1.63 average; 3.30 maximum on the 32-thread host |
| luv memory used | 18.2% average; 20.4% maximum |
| Dynamic Firefox CPU | 0.45 cores average; 0.91 cores maximum in aggregate |
| Dynamic Firefox memory | 4.63 GiB average; 6.46 GiB maximum in aggregate |
| Dynamic Firefox count | 6–11 containers |
| OOM, container network errors, host TCP retransmissions | zero |

The browsers were alive and receiving network traffic. Loki retained one
`service_starting` line per reaped Firefox container and no browser, auth, or
process failure. This excludes host CPU/memory exhaustion, OOM, container
network failure, and browser restart as credible explanations for the
synchronized window.

Application measurements undercount X traffic because each search navigation
can generate timeline pagination calls while scrolling. The last healthy minute
before the large wave contained nine application searches plus 16 recorded
scrolls—approximately 25 possible timeline fetches. All active browsers share
one `auth_token` identity and one outbound IP. The evidence therefore supports
shared upstream suppression, but does not distinguish a rate bucket,
anti-automation response, or ordinary X backend circuit.

The public [X API rate-limit contract](https://docs.x.com/x-api/fundamentals/rate-limits)
uses HTTP 429 plus remaining/reset headers. The browser UI uses undocumented
internal endpoints; the public API limits and response shape must not be
projected onto this incident without measuring the actual page requests.

## Pre-fix code path

`internal/twitter/search.go`:

1. Navigates to the Latest-search URL.
2. Treats a login/flow redirect as unauthenticated.
3. Accepts the page when no login redirect occurs, even when neither positive
   app-shell selector rendered.
4. Marks the service healthy before proving the tweet feed usable.
5. Returns HTTP 200 with `stop_reason=feed_timeout` when the first tweet locator
   misses its ten-second bound.

`internal/workflow/event.go` treats that response as a successful outer search
and records `attempts_completed`. A real `SearchTweets` error receives four
Temporal activity attempts at roughly 0/10/30/60 seconds, but exhaustion is
also logged and checkpointed instead of failing or preserving the logical
attempt. The intended 15-search contract is therefore not currently enforced.

## Implemented requirements

Treat this as one search-boundary fix rather than separate browser, metrics,
and workflow patches:

1. Classify rendered feed, explicit empty result, login redirect, known
   retry/error/interstitial, and unknown timeout as distinct states.
2. Capture bounded upstream evidence: final URL, title/state selector bits,
   internal timeline response status, response failure class, and rate-limit
   headers when present. Never record bodies, cookies, authorization headers,
   or tokens.
3. Advance `attempts_completed` only for a usable rendered or explicit-empty
   observation. An unavailable page or activity-retry exhaustion must not
   consume one of the 15 logical searches.
4. Bound unavailable retries by an explicit outage window or failure budget so
   a long X outage cannot hold fixture completion forever.
5. Add a bounded Prometheus result-state counter and retain the final search
   state durably enough to outlive fleet reaping.
6. Cover explicit empty, transient page failure, upstream status evidence,
   exhausted activity retries, recovery, and attempt checkpointing in tests.

Do not fix this by only increasing the ten-second locator wait. The observed
large wave lasted more than eight minutes. Do not immediately multiply traffic
by turning every timeout into the existing four-attempt activity retry; a real
empty result must first be distinguishable. Do not add a guessed global
rate-limit policy before the upstream response is measured.

## Related work

- FF-058's scheduled static canary independently verifies the session and live
  feed through the same classified browser boundary. It does not participate
  in an event workflow's attempt budget.
- FF-039 owns shared lifecycle/readiness semantics; FF-061 proves the concrete
  false-healthy search path.
- FF-038 owns the eventual atomic fleet controller. The later natural 429
  evidence makes the shared account/IP admission budget an input to that work.
- `AUD-TWITTER-COOKIE-WRITER` remains separate. Full credential expiry is ruled
  out here; semantic last-writer-wins cookie rotation was not measured.

## Original retrospective boundary

The reaped browser contexts and their network events no longer exist. Loki has
only service startup lines for those containers. A deliberate production load
test could recreate suppression but risks prolonging it or damaging the shared
account, while one low-rate probe would not test the incident condition. At
incident time, the maximum safe retrospective conclusion was shared upstream
feed suppression with an unknown exact response. The later
[post-release natural validation](#post-release-natural-validation) supplied
the missing response evidence without a deliberate load test.
