# Shared Twitter request admission — FF-038

**Status:** admission proposal; October 16 passive accounting is implemented
locally, not deployed. No scheduler, request limiter, search-count policy, or
production configuration changed.
The separate local FF-089 denial classifier is not part of this proposal.

## Problem and current evidence

Per-event Firefox gives searches independent browser state, not independent
upstream allowance. Browsers share a login and egress. Production has observed
SearchTimeline HTTP 429 with limit 50, remaining zero and roughly 15-minute
resets. This is an observation, not a documented universal X browser limit;
account-versus-IP scope and additional overlapping buckets remain unproven.

The [October investigation](../audits/cdn-and-discovery-gaps-2026-10-15.md)
includes workflows exhausting their unavailable budget before completing
fifteen usable searches. The [September MLS audit](../audits/mls-search-recovery-2026-09-10.md)
also found failures received after the reported reset. A reset timestamp alone
does not establish that allowance has recovered.

The October 15 baseline in the [browser ledger](../../twitter-service.md) and
code showed the following. The October 16 accounting slice addresses the first
bullet only; it does not fix classification, partial-candidate handling or
implement admission:

- `search_evidence.go` observes timeline responses and failures but overwrites
  the previous evidence; it does not count every request or preserve a
  per-request association between dispatch, headers and completion.
- `search_scroll.go` drives scrolling, which can cause pagination requests.
  One search or one scroll is not a fixed number of upstream requests.
- `search.go` classifies a missing initial feed, but its rendered-feed branch
  always returns `ResultRendered` after scrolling. A later observed 429 can
  therefore coexist with a usable result. Existing missing-feed classifier
  tests do not cover this rendered-then-failed path.
- `event.go` handles unavailable observations before dispatching returned
  candidates. Simply making partial scans unavailable would discard their
  returned candidates; candidate handling must change with classification.
- `Browser.Navigate` creates a new page, and `/search` closes it on return.
  Repeatedly yielding pagination would restart from the first page.
- `Browser.Navigate` currently ignores its context argument. New interception
  and cancellation paths must close or abort browser work, not assume HTTP
  cancellation stops Playwright automatically.
- Fleet provisioning passes only `TWITTER_HEADLESS=true` explicitly. New
  admission configuration must reach dynamic instances as well as static
  fallback and maintenance.

Production still runs `55c2afd`; read-only image inspection confirmed Temporal
`auto-setup:1.24`. The repository pins Go SDK `v1.45.0`, playwright-go
`v0.6100.0`, and driver/runtime `1.61.1`. Do not make this design depend on an
unverified newer server feature or a Temporal upgrade.

## Existing patterns and their fit

| Pattern | Fit here |
|---|---|
| Token bucket / leaky bucket | Useful for pacing. A local limiter does not coordinate replicas or learn an external finite window from response headers. |
| Temporal activity dispatch rate | Controls activity starts, not the several timeline requests made inside an activity. Insufficient as the authoritative X limiter. |
| Durable resource-coordinator workflow | One logical owner serializes shared policy and queue state. Signals, timers and Continue-As-New provide durable coordination without a new broker. Strong candidate for this system. |
| PostgreSQL-backed admission controller | A valid alternative using short row-locked transactions and idempotent receipts. Requires schema plus reliable notification/recovery between committed grants and waiting workflows. |

Primary references:

- [Go rate limiter](https://pkg.go.dev/golang.org/x/time/rate) describes the
  local token-bucket primitive. It is not a shared adaptive scheduler.
- [Temporal task-queue tuning](https://docs.temporal.io/develop/worker-performance/task-queues)
  describes activity dispatch rate controls; [fairness](https://docs.temporal.io/develop/task-queue-priority-fairness)
  describes scheduling across keys. Neither accounts for browser subrequests.
- [Temporal workflow messages](https://docs.temporal.io/develop/go/workflows/message-passing)
  and the [official resource-mutex sample](https://github.com/temporalio/samples-go/tree/main/mutex)
  provide reusable coordination patterns. Borrow the pattern, not sample-specific
  hacks or the assumption that a mutex alone enforces a rate budget.
- [PostgreSQL locking](https://www.postgresql.org/docs/16/sql-select.html#SQL-FOR-UPDATE-SHARE)
  supports a database-backed queue alternative. `SKIP LOCKED` is not a fairness
  guarantee and is unnecessary for a single serialized bucket row.
- [Discord's dynamic rate-limit guidance](https://docs.discord.com/developers/topics/rate-limits)
  illustrates learning and grouping limits from response headers. This is a
  pattern reference, not evidence that X has Discord's bucket contract.
- [X's public API header reference](https://docs.x.com/x-api/fundamentals/rate-limits)
  explains limit/remaining/reset semantics. Its public API tables do not
  establish browser SearchTimeline limits.

## Recommended direction

Use one durable admission owner per configured shared login/egress scope and
timeline endpoint family. Prefer a Temporal coordinator workflow behind a
small internal HTTP controller, subject to the compatibility spike below.
Existing EventWorkflows retain the discovery pipeline and wait durably for
their next search turn. The coordinator owns only admission, not video work.

This adds a coordinator workflow, not a polling Schedule and not a new workflow
per HTTP request. It does not require another message broker or a second job
runner. The actor variant also avoids adding an SQL execution queue.

Use a dedicated small Go HTTP process for the controller. Do not attach its
lifecycle to the static browser's critical child. Existing workers execute the
coordinator workflow; browsers need only its HTTP address, not Temporal access.
This is the first part of the FF-038 controller boundary, not an independent
limiter inside every worker or browser. It needs internal Compose routing and
an explicit memory budget before deployment. No service or budget is created
by this proposal.

Do not bundle moving Docker access out of workers, reworking browser capacity,
or cookie ownership into the first quota slice. Those remain broader FF-038
work, so shipping admission alone must not close that issue.

### Proposed flow

1. EventWorkflow requests its next search turn with an idempotent ticket and
   workflow/run identity. One pending turn per event prevents duplicate backlog.
2. The coordinator queues the ticket or grants a short-lived search lease.
   A queued workflow waits for a signal or durable deadline, not inside a
   running SearchTweets activity or an open browser HTTP request.
3. Once granted, the existing SearchTweets activity calls the event's browser.
4. A Playwright route hook asks the HTTP controller for permission before each
   timeline request, including initial navigation and pagination. Each request
   has a separate retry-stable request ID. The hook either continues promptly
   or ends the scan with an explicit deferral; it does not wait minutes.
5. Responses report only request identity, status, parsed quota values and
   observation times. The coordinator reconciles them with reserved/in-flight
   requests; late responses cannot refill an exhausted window or undo a 429.
6. Returned candidates are processed immediately even when the scan is partial.
   The remaining search intent returns to durable waiting. Hashing, OCR,
   placement and uploads continue independently.

All browsers sharing the configured login must use the same authority,
including future dev, static fallback and the maintenance search. Deployment
environment and cookie-file fingerprint are not quota identities: environments
can share a login, and cookie rotation must not mint a new allowance. No
credentials or actual account identifier belong in workflow IDs or metrics.

### Budget and recovery invariants

- Learn observed limit/remaining/reset values; do not hard-code 50 as a
  universal allowance or continuously refill at `50 / 900` without evidence.
- At cold start or after an uncertain reset, allow one useful queued search
  request as a recovery probe. Do not issue a throwaway search just to measure.
- An observed 429 blocks new requests in that shared scope. Already-sent
  requests may finish; the controller cannot retract them.
- At the bounded recovery time, allow one probe, not a synchronized fleet
  release. Missing, expired, implausible and repeated reset headers need an
  explicit bounded fallback policy, not immediate retries by every event.
- Reserve before dispatch. Retrying the same admission RPC must not debit
  twice. If permission was issued but actual transmission is uncertain,
  conservatively retain the debit; do not promise exactly-once X requests.
- Controller/Temporal unavailability must not silently become unlimited
  sending. Distinguish admission failure from deliberate quota deferral.
- Lease expiry, event removal, abandoned workflow runs and request cancellation
  must remove stale demand and prevent late grants from starting old searches.
- Intentional quota waiting consumes neither a usable search nor an unavailable
  probe. Actual upstream failures remain recorded. Waiting still needs a
  bounded search policy; existing EventWorkflows have no execution timeout.
- Preserve the stored FF-091 earliest-tweet boundary and disable seen-stop
  eligibility after a partial or interrupted scan. It is an eligibility floor,
  not a pagination cursor or proof of complete search coverage.

### First-release policy recommendation

Preserve fifteen usable searches and the existing spacing after a completed
search. Do not also introduce a fifteen-minute discovery horizon. The user
delegated this initial policy choice rather than selecting a new product
contract. Under quota pressure, fifteen usable searches remain a target, not
an unconditional guarantee.

Recommend a thirty-minute **cumulative admission-wait budget** per discovery
execution chain, persisted across failed-run recovery. This is a proposed
application safety bound, motivated by the observed approximately fifteen-
minute reset windows; it is not an X contract. Queue residence, including a
worker outage during residence, consumes it. Finishing a search or restarting
a worker does not reset it. On exhaustion, stop new searches, retain a durable
`admission_wait_exhausted` outcome, and drain owned video work. Do not impose a
whole-workflow timeout or count waiting as a failed upstream observation.

**Scheduling correction — October 16:** the earlier one-active-scan
recommendation is withdrawn. Independent searches can already start in arrival
order, and atomic reservations can protect quota while scans run concurrently.
Preserve healthy concurrency; do not add blanket serialization or delays to
scrolling. Each physical timeline request still needs its own debit under
future enforcement. A search lease is not unlimited permission to paginate.
Fair allocation when allowance becomes scarce remains a design gate after
passive request-cost measurements; do not implement the earlier FIFO whole-
scan scheduler as though it were approved.

Do not preempt healthy scans after one page, retain X pages across long quota
waits, or add continuation tokens in this slice. Return partial candidates when
allowance runs out, close the page, and wait durably before restarting from the
fixed floor. This can repeat the newest prefix after a reset. The coverage gate
must measure that cost before enforcement; fixed timestamps do not make it
lossless. If prefix repetition prevents progress, revise this plan for explicit
continuation rather than repeatedly restarting and calling that success.

Measure request cost, queue delay and useful coverage before choosing scarce-
request scheduling or pacing. Request accounting alone does not spread a
finite window's allowance evenly across fifteen minutes.

## Implementation surfaces

| Surface | Proposed responsibility |
|---|---|
| `internal/domain/` focused admission package | Pure bucket state, reservations, cooldown/probe transitions and fair turn selection with a simulated clock. |
| `internal/contract/` focused admission types | Tickets, leases, request receipts and bounded quota observations; no Playwright or Temporal dependencies. |
| `internal/workflow/` coordinator | Durable pending turns, signals, timers, lease expiry and bounded history rotation. |
| Internal HTTP controller adapter | Short idempotent admission/report RPCs; no cookies or raw timeline payloads. |
| `internal/twitter/browser.go`, `search_evidence.go`, `search_scroll.go`, `search.go` | Pre-send gating, request accounting, cancellation and honest partial results. |
| `internal/contract/twittersearch`, `internal/infra/twitter`, discovery activity | A compatible internal deferred/partial-result contract that preserves candidates. |
| `internal/workflow/event.go` | Candidate dispatch before deferral handling, durable waits, separate accounting and Temporal replay versioning. |
| Maintenance, config, fleet provisioning and Compose | One shared authority and scope; propagate configuration to dynamic containers. |

The [implementation plan](./twitter-shared-admission-implementation.md) specifies
the proposed contracts, file changes, retry ownership, recovery, and release
gates. These are reviewable design choices, not shipped interfaces. Queries
stay read-only: never "acquire" a permit by querying and changing local state.
Use Signals for mutations and named Query receipts for short HTTP responses.
Do not require Workflow Updates, Update-With-Start, or native fairness.

If the coordinator's synchronous request/receipt path has unacceptable
complexity or latency, prefer the PostgreSQL controller alternative rather
than layering an unreliable acknowledgment protocol onto Temporal.

## Offline feasibility results — October 15

The retained [research harness](../../../scripts/probe_twitter_admission/README.md)
uses the repository's Go SDK and a private disposable Temporal stack with the
same `auto-setup:1.24` image family as production. The server reported 1.24.3.
Signal-plus-receipt requests survived duplicate mutation delivery, a replaced
caller, worker replacement, and Continue-As-New without double debits. One
hundred local requests measured a median of 49.697 ms, p95 of 52.419 ms, and
maximum of 56.660 ms. This is a local feasibility measurement, not a production
latency promise or a full scheduler test.

Workflow Updates returned `PermissionDenied` because they were disabled in
the disposable server's default namespace configuration. Production's namespace
setting was not changed or assumed. The verified Signal/receipt path needs no
server feature change. A receipt must explicitly exist and authorize the
request; a successful Signal acknowledgment alone is not a grant.

Characterization through the real search handler confirmed both rendered-
then-429 and rendered-then-503 return a candidate **and** `rendered`/usable today.
The tests deliberately preserve the current defect until the runtime fix; they
do not endorse counting these scans as complete.

A Go/real-Firefox fixture proved that a locally paused route does not block
other driver calls, can resume the same synthetic page cursor, and can be
aborted before transmission. It does not establish that an X fetch can wait
for minutes without application timeout. The first release does not rely on
that behavior.

A disposable container used production's immutable Twitter image at
`55c2afd`, with `--network none`, a 1 GiB memory cap, two CPUs and no shared
volumes or cookies. A Node harness used the image-owned Playwright driver and
real Firefox against a local HTTP fixture. The route admitted two timeline
fetches and aborted the third. The fake upstream received exactly two requests;
both response headers were observed. The process exited successfully.

This validates the browser/driver interception seam, not a finished Go
controller, production latency, fairness, service-worker coverage or live X
behavior. The temporary harness is `/tmp/ff038-routing-probe-20261015.cjs`; that
file is disposable, not an operational dependency.

[Playwright routing](https://playwright.dev/docs/api/class-browsercontext#browser-context-route)
also documents two constraints: enabling routing disables HTTP cache, and
service-worker-handled traffic may bypass routing. Measure the first and prove
coverage in our Firefox configuration; do not extrapolate the fixture into a
guarantee that all browser traffic is intercepted.

## Delivery and acceptance

1. Characterization, the production-version Temporal receipt spike, and local
   passive per-request accounting are complete. The passive slice has no
   routing, controller call, new wait or search-policy change. Next obtain
   approval to capture normal traffic, and build the pure policy model with
   target regressions. Do not issue additional X searches for telemetry.
2. Prove the complete admission/deferral path offline before enforcement:
   shared cooldown, one recovery probe, idempotent reservations, stale headers,
   missing reset, cancellation, lease expiry, controller loss, worker restart,
   Continue-As-New and fair service under sustained arrivals. Check initial
   versus pagination calls and partial candidate durability separately.
3. Settle concurrent scarce-request allocation and the bounded wait against
   request cost, queue delay and repeated-prefix behavior. If the coverage gate fails,
   revisit continuation before enforcement. Keep fifteen usable searches
   initially unless an explicit product decision changes it.
4. Deploy the internal contract in a coordinated quiet window with no active
   discoveries. Observe normal-load request counts, 429s, waiting time, first
   candidate latency, scan completion and public clip coverage. No increase in
   X traffic, auth changes, alternate accounts or quota evasion is in scope.

An eventual fifteen-minute search horizon is a separate decision. Under a
finite shared allowance, neither fifteen successful scans nor an exact final
scan at minute fifteen is always possible. Reserve capacity or allow a bounded
catch-up grace only through an explicit policy; do not silently promise both.
