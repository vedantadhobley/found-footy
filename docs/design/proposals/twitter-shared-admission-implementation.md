# Shared Twitter admission — implementation and acceptance plan

**Status:** admission proposed; passive accounting deployed on 2026-10-16 as
`62af131`. Read the [design and measured evidence](./twitter-shared-admission.md)
first. The previous one-active-scan recommendation is withdrawn: preserve
healthy concurrency. Scarce-request scheduling remains a design gate, not a
settled implementation instruction. Only the passive telemetry slice is deployed.

## Scope and ownership

Preserve the existing query, fixed earliest-tweet boundary, fifteen usable
search target, candidate processing, browser ownership, and public API/NATS
contract. Add admission ahead of search execution and ahead of each physical
SearchTimeline send. Do not change OCR, deduplication, video quality selection,
or the fleet's Docker ownership in this slice.

| Owner | State and work |
|---|---|
| EventWorkflow | Search intent, attempt/outage counters, durable candidates, admission-wait budget, cancellation, pipeline completion. |
| Coordinator workflow | Shared bucket, fair eligible demand, bounded active leases, request debits/receipts, cooldown, recovery probe, timers. |
| Internal HTTP controller | Validated short RPCs to the coordinator; no independent in-memory authority, cookies, Docker socket, or SQL queue. |
| Event/static browser | Page lifetime, pre-send routing, per-request evidence, extraction, bounded cancellation. |

One configured login/egress scope has one coordinator, irrespective of
deployment name. Use an operator-assigned opaque scope, never a cookie hash,
account name, or token. `timeline` is the initial endpoint family. All callers
using this login must join the authority or remain disabled.

The first deployment covers production's static and dynamic browsers while
dev remains stopped. Workflow-to-workflow grant delivery requires a shared
Temporal cluster; namespaces can be explicit. Do not assume an independent
dev Temporal server can receive external signals from production. Enabling
such a dev stack needs a tested cross-cluster delivery adapter or a compatible
shared-cluster topology first. A second controller must not mint its own quota.

## Proposed internal contracts

Put transport-neutral types in `internal/contract/twitteradmission`. Names
below are the proposed implementation surface, not existing public APIs.

- `TurnTicket`: scope, endpoint family, ticket ID, event/workflow/run identity,
  reply namespace, logical search ordinal, not-before time, absolute queue
  deadline, and scan timeout. The controller selects allowed namespace/cluster
  destinations from config, never arbitrary caller-supplied network addresses.
- `TurnLease`: ticket ID, unique lease ID, grant time and expiry. At most one
  scan lease is active per event, not per scope. A permit is valid only within
  its active lease; a lease alone does not reserve arbitrary pagination quota.
- `RequestDecision`: lease ID, request ID, `granted | deferred | stale`, optional
  permit ID, expiry, and bounded reason. Only `granted` authorizes transmission.
- `QuotaObservation`: lease/request/permit IDs, status or transport outcome,
  parsed limit/remaining/reset, observed time, and actor-assigned send sequence.
  No raw URL, query string, response body, cookies or request headers.
- `TurnResult`: lease ID, completion/interruption reason and measured request
  totals. Repeated completion cannot release a different active lease.
- `AdmissionCheckpoint`: monotonic sequence, retry-stable checkpoint ID,
  current ticket, accumulated queue duration, queue-entry time, and final
  admission outcome. Persist in the existing downstream checklist metadata,
  not a new execution-queue table.

Ticket IDs are unique per workflow run and physical scan sequence, not just
the logical attempt: partial retries can share one logical attempt. Request
IDs are unique per intercepted physical request. Retry of the same controller
RPC reuses its request ID; a genuinely new request uses a new ID.

Add an optional admission lease to internal `twittersearch.SearchRequest`.
For enforced callers it is required. Extend `SearchResponse` with a typed scan
outcome (`complete | quota_deferred | upstream_failed | admission_failed`) and
bounded request totals. Keep `ResultState` for page/upstream evidence; a rendered
page is not proof that the scan completed. Validate impossible combinations.

## Signal and receipt protocol

The coordinator uses Signals for `enqueue`, `cancel`, `reserve`, `observe`, and
`finish`. Its receipt Query reads an already-processed decision. Initial start
uses Signal-With-Start through the controller/client adapter. Ordinary event
waits receive a grant signal, not an activity that holds a connection for the
reset interval. No Workflow Update or Temporal server change is required.

For each short browser RPC:

1. Validate scope, active lease, IDs, deadline and payload size.
2. Send the idempotent mutation; retrieve its named receipt.
3. If not yet processed, retry the read within a bounded RPC deadline. Never
   manufacture a grant from a Signal acknowledgment or an absent receipt.
4. If the caller times out, preserve the same request ID on retry. Its mutation
   may already have committed. An unknown result authorizes no network send.

Recommend a two-second end-to-end browser admission RPC bound, including
receipt lookup. An unresolved receipt produces `admission_failed`, not a send
or an invented cooldown. Enqueue/cancel activities use short bounded retries;
exhaustion fails the producer through its existing error/drain path. Long quota
waiting exists only in workflow state, not in those RPCs. Validate this proposed
RPC bound under load before enforcement; the local median is not its SLO.

Receipts are valid only for their active lease and physical request. Closed
leases reject old reserve messages even after detailed receipts are pruned.
Retain bounded lease tombstones until their ticket's absolute deadline; after
that deadline, late enqueue/reserve messages are invalid regardless of pruning.
Prune observations by count/age and carry only live state plus needed receipts
through Continue-As-New. Drain pending signals before rotating history.

The coordinator records a grant before signaling the event. Delivery failure
does not free and reissue that same lease; retry delivery or let it expire.
Signals address the exact workflow run. Events ignore expired, canceled,
superseded-ticket, or mismatched-run grants. Cancellation is idempotent.

## Bucket and fairness policy

Use pure transitions with an injected time in `internal/domain/twitteradmission`.
Temporal supplies time and timers; do not embed a wall-clock limiter in replay.

- `unknown`: one useful timeline probe may be in flight. Do not initialize an
  invented fifty credits. Concurrent requests cannot borrow future headers.
- `available`: reserve each send against valid observed remaining allowance,
  conservatively accounting for outstanding reservations. Never refund a send
  whose transmission is uncertain.
- `cooldown`: remaining zero or a 429 blocks all new sends in the scope.
  Already-sent requests can finish. A later 200 cannot immediately erase the
  429 or increase remaining allowance in that window.
- `probe`: when recovery is due, admit one useful queued request. A successful
  response with valid fresh headers can restore measured allowance. Timer
  expiry alone never restores an entire bucket.

Within an observed window, clamp remaining downward; correlate every response
with its reservation and send sequence. Stale or out-of-order headers cannot
mint credits. A reset change is evidence to validate, not permission to refill.
Missing/invalid headers never establish unlimited sending. A successful probe
without trustworthy quota evidence remains in conservative probe-only mode.

Proposed fallback parameters, all application policy rather than X guarantees:
wait sixty seconds after an unsuccessful/indeterminate probe; double repeated
probe backoff to a fifteen-minute cap; add five seconds to a plausible future
reset; reject reset times over one hour ahead and use fallback instead. Inject
these values in tests and record them explicitly if adopted. Do not infer
cookie expiry from cooldown, missing quota headers, or admission failure.

Preserve concurrent healthy scans. Atomic per-request reservations—not a
one-active-scan restriction—protect the last credits. Current search timeout
plus a short RPC cleanup allowance bounds lease ownership. Use passive request
cost evidence to settle allocation among pending initial and pagination
requests before implementing enforcement. No first-search priority, one-page
preemption, long-held route, or per-event independent backoff loop is approved.

## Browser and partial-scan behavior

Install routing and observers before navigation. Match actual known timeline
endpoints; test redirects and other X requests explicitly. Auth/home traffic
is not classified into this bucket without evidence. The coordinator's API
must not become a generic proxy or inspect authenticated payloads.

Every timeline route obtains a short request receipt before `Continue`.
Record every admitted request and response, not just the last response.
Authorization applies once to that paused request, not to a caller-created
replacement. Controller loss, stale permission and unknown RPC outcomes fail
closed. An expired scan aborts outstanding routes and closes its page.

When quota defers pagination, stop further scrolling, collect already-rendered
candidates, close the page and return `quota_deferred`. Do not keep the request
or page open through a fifteen-minute reset. Return `upstream_failed` for an
actual observed 429/503, even if earlier candidates rendered. Quota policy
prevents future sends; it does not rewrite an actual upstream failure as a
planned deferral. Accumulate failure evidence so a later 200 cannot hide it.

Do not restart the same scan immediately following deliberate deferral. The
next attempt waits in the coordinator. Preserve its fixed cutoff and disable
the seen-tweet shortcut after every interrupted scan. Restarting can repeat
the prefix; it is not equivalent to a durable provider cursor.

## EventWorkflow integration and recovery

Use a new `workflow.GetVersion` branch. Keep legacy histories' command order,
activity payloads, and accounting unchanged. New histories do the following:

1. Restore candidates, existing counters, fixed floor, admission checkpoint and
   cumulative waiting. Do not give replacement runs a fresh waiting budget.
2. Enqueue a retry-stable ticket with an absolute queue deadline. Wait on the
   matching grant/cancellation/deadline through a workflow selector. Waiting
   owns no SearchTweets activity slot or browser HTTP connection.
3. After a valid grant, execute SearchTweets under the existing execution
   timeout. Send lease completion from a cancellation-safe, bounded cleanup
   path; expiry also recovers lost completion. Cleanup failure cannot mask the
   original search result or grant an extra request.
4. Dispatch and persist returned candidates **before** branching on scan
   outcome. Preserve FF-034's parallel downloads/observation writes and its
   rule that failed observation persistence prevents checkpoint advancement.
5. `complete` advances usable searches. Deliberate quota deferral advances
   neither counter. Actual upstream failure advances the existing unavailable
   counter. Admission infrastructure failure has a separate bounded outcome,
   never an X outage or infinite quota wait. Mixed results retain both the
   found candidates and the actual failure classification.
6. Persist progress and wait before another turn. Existing downstream work
   continues while the producer waits; no batch barrier is added.
7. On admission-wait exhaustion, stop producing searches and drain candidates
   already owned. Report the incomplete search budget durably rather than
   claiming fifteen searches ran.

`RecordDiscoveryProgress` currently orders evidence by usable + unavailable
counts. A quota deferral changes neither. Add a separate monotonic sequence
for the **complete checkpoint**, including counters, evidence, fixed-window
eligibility and admission state. Use row-level compare-and-set, not a timestamp
or a newly invented execution registry:

- Read sequence `n` during recovery; the producer has at most one awaited
  checkpoint write in flight. Its next write carries expected sequence `n`,
  new sequence `n+1`, and checkpoint ID `<run_id>:<n+1>`.
- Commit the complete checkpoint only if the stored sequence still equals
  `n`. If that exact checkpoint ID and payload already committed, return
  idempotent success. Equal sequence with a different ID/payload is conflict,
  not permission to overwrite.
- A stale/conflicting writer stops through the error/drain path; replacement
  recovery reads the winning checkpoint. Do not silently merge queue duration
  from one observation with scan eligibility from another.
- Once a versioned checkpoint exists, legacy checkpoint inputs cannot replace
  its fields. Retain the old behavior for rows belonging only to old histories.

This protects a committed newer checkpoint from delayed activities without
claiming that SQL knows which Temporal run is currently active. Keep the fixed-
cutoff invariant. Test the failed-run overlap explicitly. No table migration
or separate owner-generation field is planned.

Use the proposed thirty-minute cumulative queue budget from the design. Store
queue-entry time before waiting and charge until grant or terminal deadline;
recovery includes elapsed time since the stored entry. A whole EventWorkflow
execution timeout would also kill video work and is explicitly excluded.

## File change map

| Files | Required modification |
|---|---|
| `internal/contract/twitteradmission/` (new) | Typed tickets, leases, decisions, observations, checkpoints and validation. |
| `internal/domain/twitteradmission/` (new) | Pure finite-budget, fair eligibility, expiry, probe and pruning transitions. |
| `internal/workflow/twitter_admission.go` (new) | Actor, durable timers, grant signals, receipt Queries, Continue-As-New. |
| `internal/infra/twitteradmission/` (new) | Temporal Signal/receipt client and browser-facing HTTP client; bounded retry and instrumentation. |
| `cmd/twitter-controller/` and focused HTTP handler package (new) | Small independent controller process with bootstrap/drain; no browser dependency or Docker socket. |
| `internal/contract/twittersearch/types.go` and tests | Lease, scan outcome and request totals, combination validation. |
| `internal/twitter/browser.go`, `browser_iface.go`, `search*.go` | Pre-send hook, per-request association, sticky failure, context cancellation, partial extraction and route cleanup. |
| `internal/infra/twitter/client.go` and tests | Validate the new scan contract; preserve partial candidates in classified results. |
| `internal/activity/discovery/activities.go`, `candidates.go`, `search_window.go` | Admission enqueue/cancel adapter, classified partial output, compare-and-set sequence checkpoint and recovery. |
| `internal/workflow/event.go` and focused helper/tests | Versioned producer, candidate-dispatch helper, durable admission wait, independent accounting and drain. |
| `internal/activity/twittermaintenance/`, `internal/workflow/twitter_maintenance.go` | Separate auth verification from canary search; canary joins the same queue. Deferred canary is not re-auth failure. |
| `internal/app/worker/` and worker registration | Register the coordinator and required admission activities; no new polling Schedule. |
| `internal/config/`, `.env.example`, `internal/infra/firefoxfleet/` | Controller binary profile, immutable scope/address/mode config and explicit dynamic-container propagation. |
| `Dockerfile`, both Compose models, release/config tests | Controller build, private network route, startup/readiness, memory cap/heap limit, exact image identity and no host port. Production edits require approval. |
| `docs/twitter-service.md`, orchestration, Temporal, testing, operations, decisions | Shipped contract and rollout gates in the code commit; FF-038 remains open for broader fleet work. |

## Observability and failure visibility

Expose queue depth, active lease, bucket state, granted/deferred/unknown request
counts, quota-wait duration, actual timeline status, partial scans and final
search-budget outcomes. Metrics use bounded state/reason/endpoint labels, never
event IDs, request IDs or account identity. Correlated logs/history can carry
event/ticket/request IDs without authenticated URL or header contents.

Persist bounded per-scan request totals/evidence alongside its progress. Keep
live lease/receipt detail in Temporal; do not retain every timeline body or add
an unbounded SQL request log. Controller health distinguishes process health
from a reachable coordinator and from an intentionally exhausted bucket.
Exhausted quota is not a failed browser or an expired-cookie alert.

## Verification and release sequence

1. **Passive baseline:** characterization/protocol probes passed; request
   accounting deployed with explicit approval as `62af131`. There is no routing,
   limiter, query change, or extra X search. Natural observations remain pending.
   Validate changed source/config before expensive admission test work.
2. **Offline implementation:** pure model tests; worker restart/history rotation;
   idempotent and concurrent mutations; lost acknowledgment; stale lease and
   cross-run grants; quota observations in every order; no/malformed headers;
   repeated 429 after reset; controller loss; browser cancellation; partial
   candidate durability; conflicting checkpoint/failed-run races; old-history replay.
3. **Fairness/load fixtures:** many events, sustained fresh arrivals, slow/lost
   browser, maintenance, shared static/dynamic callers. No eligible ticket is
   overtaken by a later eligible ticket; ownership expiry guarantees progress.
   Assert request conservation and bound state/history growth, not just latency.
4. **Coverage gate:** replay passive request/candidate sequences through the
   model. Measure requests per usable scan, admission overhead, queue delay,
   repeated prefix and oldest reached tweet. Replay cannot invent what X would
   return after deferral; bounded live enforcement is still required to prove
   that behavior. If the proposed allocation/restarts starve coverage,
   stop and revise rather than tuning assertions to pass.
5. **Coordinated quiet-window cutover:** approve Compose/config changes and
   deployment independently; require no active discovery histories on the old
   contract. All shared-login searchers enforce admission together. Start in
   passive/shadow mode, then explicitly approve enforcement after reviewing
   evidence. Rollback is coordinated, not one browser silently bypassing it.
6. **Natural acceptance:** verify sends never bypass receipts, deferred scans
   retain candidates, public first-video latency/coverage remain acceptable,
   quota waits do not consume outage budget, and canceled/removed events release
   demand. Compare actual request use rather than search/scroll counts.

Compatibility is proven; end-to-end admission, provider coverage and the
proposed policy parameters are not. This plan is not permission to implement
or deploy before review. It intentionally exposes those release gates instead
of treating an offline Firefox fixture as proof of X behavior.
