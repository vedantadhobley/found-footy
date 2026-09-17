# API-Football health assessment and reconciliation protection

**Status:** Durable collection and atomic protection deployed in shadow for
[`FF-075`](../../todo.md#ff-075--successful-provider-responses-can-destructively-regress-live-state).
The wire contract and non-enforcing semantic evaluator have shipped; the durable
collector and atomic mutation enforcement are committed as `89d1d7c`. Their
production schema was prepared and the application deployed as `abfea7a` on
September 17 at 02:35 UTC. The
[shadow-default follow-up](../../decisions/2026-09-17-provider-policy-starts-in-shadow.md)
retains transactional safeguards while making the new policy advisory for at
least one complete match window. The earlier live audits below validate the
stateless evaluator, not the new durable thresholds and recovery. The
[first production shadow audit](../audits/provider-integrity-shadow-2026-08-31.md)
found and locally repaired classification gaps. The
[second shadow window](../audits/pre-rollout-evidence-2026-09-08.md#ff-088-and-ff-075-postponed-polling-pollutes-circuit-evidence)
is now reviewed: stable deferred null-score observations repeatedly flagged
against retained zero scores and contaminated global recommendations. The
[second classification repair](../../decisions/2026-09-08-postponed-score-absence-is-not-play-regression.md)
deployed on 2026-09-09 as `a7c9f53`: only an unchanged empty PST scoreboard
is exempt from score clearing. Waiting for more unchanged polls is not the
remaining design gate. FF-088 owns the separate deferred
polling/reactivation policy. Current behavior is authoritative in the
[fixture-monitoring ledger](../../orchestration/monitor.md).

**September 16 design revision — deployed in shadow September 17:** The
[Barcelona stalled-feed incident](../../incidents/2026-09-16-api-football-stalled-fixture.md)
requires separating fixture/provider health from write permission. This revision
replaces the earlier single circuit-state table with independent evidence and
cause-specific recovery. Numerical thresholds below are proposed starting
values, not vendor guarantees or historically calibrated limits. The
[health ledger](../../provider-health.md) describes the four-table durable
contract, versioned collector, independent cause recovery, and guarded writers
now deployed in shadow. Search and frontend contracts are unchanged.
The [test ledger](../../testing.md) separates atomic protection proof
from the remaining search and operator work.

## Problem

Found Footy treats a successfully decoded `/fixtures?ids=` response as a fresh,
authoritative replacement for every mutable provider fact. This protects against
out-of-order responses through `fixtures.last_polled_at`, but it does not protect
against a newer response whose football state has regressed.

The 2026-08-29 production incident demonstrated that boundary. API-Football
continued returning nominally successful responses while event, score, and
status facts regressed across several unrelated fixtures. Three consecutive
bad observations satisfied the event-absence debounce. Ten confirmed events
were therefore marked removed, 26 public shares were revoked, and their Garage
objects were reclaimed. The provider restored the missing facts roughly 75
minutes later.

The existing three-poll debounce only answers whether an observation persisted.
It does not answer whether the observation is credible. The one-hour terminal
grace also cannot protect an active fixture from destructive event reconciliation
before completion.

On September 16, valid HTTP-200 responses held Barcelona at 65′, 4–2 while
other fixtures advanced. A frozen feed neither regresses known facts nor
removes an event we have never seen. The user reported a later goal, but the
provider had not supplied it. This requires temporal freshness evidence in
addition to the existing consistency checks.

## Health assessment before mutation policy

Use three independent assessments, not a combined healthy/unhealthy boolean:

| Assessment | Question | Evidence |
|---|---|---|
| Availability | Did the expected request return usable data? | Request outcome, typed contract failure, requested/returned IDs, observation time. |
| Freshness | Is a field that should progress still progressing? | Phase-aware clock history, phase changes, time since progress, observation coverage. |
| Consistency | Does this observation contradict established facts? | Identity, scores, event inventory, supported correction evidence, retained baseline. |

Keep unknown/not-assessable distinct from an observed failure and from recent
progress. No active fixtures means no live-feed freshness evidence, not a
healthy API. An advancing clock does not prove that all goals were reported.
`trusted` remains the current shadow policy name; it must not be presented as
an overall health result. Avoid a policy rename in the first implementation.

### Proposed first freshness rules

Evaluate wall-clock age, not a count of repeated polls. Temporal overlap-skip,
activity delays, and retries make poll counts an unreliable elapsed timer.

| Provider phase / evidence | Initial rule |
|---|---|
| `1H`, `2H`, or `ET`, numeric clock inside a playing period | Suspect staleness after **3 minutes** without credible clock progress. |
| Clock at a period boundary: 45, 90, 105, or 120, including stoppage display | Use **10 minutes** without clock/phase progress; do not treat normal capped clocks as the interior-period case. |
| `HT`, `BT`, `P`, `INT`, `SUSP`, `NS`, `PST`, `TBD`, terminal statuses | No running-clock timer. Report phase age and whether clock freshness is assessable; no automatic claim that the feed is current. |
| `LIVE`, unknown status, or no numeric clock | Clock freshness is unknown; availability and consistency checks still apply. |

Use the raw phase/elapsed/extra tuple, with the existing phase-aware reset
rules. Within a phase, only forward clock progress resets its age; metadata,
goals, repeated HTTP success, and a backward/forward oscillation below the
retained high-water mark do not. Track score/event changes separately. Entering
or leaving an accepted break starts the appropriate new phase expectation.

The timer needs distinct successful observations spanning the interval, with
no gap exceeding **90 seconds** in the active-poll evidence. A fetch failure
is availability loss. A local polling gap is observation loss, not proof the
provider froze; resume with unknown freshness until the window is established.
Persist progress across worker restarts; do not initialize it from kickoff or
retroactively label the whole unobserved interval as stale.

Boundary timers are lower-confidence warnings: long stoppages can be real.
They must not by themselves impose a destructive-write restriction. Interior
staleness temporarily restricts the affected fixture as specified below.
Long-break/shootout timers are not in the first implementation; phase-age
visibility makes this explicit rather than declaring those feeds healthy.

These signals detect a stalled feed, not its absolute lag or missing goals.
An unreported match interruption can resemble a frozen provider. One source
cannot prove completeness or that a resumed feed has caught up to real play.

## Research boundary

Conventional circuit breakers protect a caller and dependency from repeated
transport failures. They use closed, open, and half-open states; stop calls while
open; and close after bounded successful probes. The
[Microsoft pattern reference](https://learn.microsoft.com/en-us/azure/architecture/patterns/circuit-breaker),
[AWS pattern reference](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/circuit-breaker.html),
and [Resilience4j implementation](https://resilience4j.readme.io/docs/circuitbreaker)
also establish useful requirements: minimum evidence, consecutive recovery
successes, observable transitions, manual override, and resource-scoped state.

An off-the-shelf transport breaker is not the application mechanism here.
[Sony gobreaker](https://github.com/sony/gobreaker) is in-memory and rejects
wrapped calls while open. Found Footy must continue polling to observe recovery,
and the state must survive separate Temporal schedule executions and worker
restarts. HTTP success is also not the relevant success predicate.

Data-quality systems supply the missing half of the design: validate completeness,
identity, consistency, and changes from prior observations before accepting a
dataset. AWS Glue's
[data-quality rule model](https://docs.aws.amazon.com/glue/latest/dg/dqdl.html)
is representative. Found Footy needs a small deterministic domain version of
that model, not a new external service.

## Proposed invariant

Every provider observation receives one mutation policy before any fixture or
event write:

| Policy | Meaning |
|---|---|
| `trusted` | Apply ordinary reconciliation, including supported corrections. |
| `positive_only` | Accept additions and forward progress; suppress destructive or regressive mutations. |
| `rejected` | Apply no provider-derived mutation from this observation. |

| Condition | Required action |
|---|---|
| Fetch or wire-contract failure | Record unavailable/invalid evidence; apply no snapshot for affected IDs. Keep polling. |
| Interior running-clock staleness | Warn and use fixture-local `positive_only` until freshness recovery; do not freeze unrelated fixtures. |
| Boundary warning or unassessable clock alone | Warn/report uncertainty without changing write permission. |
| Unsupported regression or disappearance | Fixture-local `positive_only`; retain the conflicting baseline until resolved. |
| Conflicting identity | Reject the affected fixture, as the shadow evaluator already recommends. |
| Correlated destructive regression across fixtures | Provider-wide `positive_only`; fixture-specific rejection still wins. |
| Supported correction with no applicable restriction | Ordinary event debounce, not a second correction debounce. |

These are write permissions, not health states. `rejected` takes precedence
over `positive_only`, which takes precedence over `trusted`. The guard does
**not** stop API calls. A `positive_only` observation may:

- fetch every active fixture at the normal cadence;
- insert new events and cast presence votes;
- accept score, clock, and phase advancement;
- accept scorer attribution and other newly populated metadata;
- run the ordinary three-presence-vote discovery lifecycle.

It suppresses:

- event absence votes and unknown-placeholder disappearance deletes;
- score, clock, phase, winner, penalty, or populated-metadata regression;
- event removal, workflow cancellation, share revocation, and object deletion;
- terminal-observation advancement and fixture completion from suspect data.

This intentionally prefers bounded false-positive work over irreversible clip
loss. The ordinary event debounce still filters transient positive reports.
For example, a real VAR during an unreported long interruption can be delayed
until evidence supports the correction and freshness recovers. It must not be
silently discarded. Existing downstream work continues during provider trouble.

Missing never-observed goals are incomplete coverage, not evidence that retained
goals should be deleted. Preserve FF-063: score/event parity alone must not
block terminal retirement forever. An unresolved conflict with retained facts
does block destructive reconciliation and retirement; it requires evidence or
an audited operator decision, not automatic expiry. This distinction prevents
reintroducing the permanently eventless-fixture bug.

## State and scope

Use durable reason-bearing episodes, with per-cause recovery evidence. Derive
the current write policy from unresolved causes; do not add the old
`closed/open/recovering/forced_open` enum as a second source of truth. An audited
operator restriction is another explicit cause, never an erase-history switch.

Provider summaries include requested coverage, affected fixture IDs, shared
reasons, and examples of still-progressing fixtures. They describe the observed
feed scope, not the whole vendor. No extra `live=all` request is required on
every poll just to claim broader coverage.

Proposed correlation: **two distinct fixtures with new strong anomalies within
two minutes**, excluding supported corrections. Correlate freshness separately
from semantic regressions. Staleness across fixtures raises a provider warning
but restricts only the affected fixtures; correlated destructive regression
restricts destructive writes across the provider's monitored fixtures. Failed
request chunks retain their exact coverage; one bad chunk is not proof that
successful chunks are corrupt.

An old unresolved cause must not retrigger its onset every poll and combine
with a new isolated issue. Ten disappearing events in one fixture increase its
severity, not its scope: remove the current shadow shortcut that three missing
events alone imply a global problem. This changes the proposed enforcement
threshold, not the deployed shadow evaluator. Unresolved fixtures remain
restricted after provider-wide recovery.

Both scopes must be durable in Postgres. Each scheduled ActivePollWorkflow is a
separate Temporal execution, and a worker restart must not reset provider trust.

## Validation signals

### Reject the affected payload

- nonempty envelope `errors`;
- `results` does not equal the decoded response length;
- incomplete paging;
- duplicate or unrequested fixture IDs;
- requested fixture IDs absent from a nominally successful chunk;
- missing or `null` `events` on an ID-based query, distinct from explicit `[]`;
- zero or conflicting fixture/team identity, negative scores, or an event whose
  team is not a fixture participant.

The shipped typed adapter now rejects these envelope and coverage defects.
This was the pre-FF-075 transport gap; the remaining work is durable semantic
trust and mutation enforcement, not repeating the wire-contract implementation.

### Quarantine one fixture

- home, away, league, or fixture identity changes unexpectedly;
- score decreases without a supported correction signature;
- terminal status returns to live;
- phase or clock moves materially backward;
- a confirmed event disappears;
- several populated facts clear together.

Provider aggregation follows the scope rules above. Percentages are telemetry,
not an automatic whole-provider trip: one bad fixture out of two does not prove
a systemic failure. A failed chunk is rejected immediately without waiting for
correlation; a genuine multi-fixture semantic regression restricts the first bad
batch before any canonical writes, not after a debounce.

## Supported correction signatures

One isolated goal disappearance may continue through the existing three-poll
absence debounce when all available facts agree:

1. exactly one confirmed goal disappears;
2. the correct team's score decreases by exactly one;
3. the remaining inventory stays coherent;
4. status and clock do not regress; and
5. the correction is near the goal, unless explicit provider evidence overrides
   the age heuristic.

`Var / Goal cancelled`, when present, is strong corroboration. It cannot be
mandatory because captured API-Football behavior sometimes removes the goal
without adding the explicit VAR event.

Red cards and missed penalties have no score parity signal. A destructive
correction should require positive replacement evidence, such as a red card
becoming a yellow card for the same player and minute or a missed penalty
becoming a converted penalty, or an audited operator/secondary-source decision.

One provider cannot perfectly distinguish a persistent single-fixture outage
from a real correction. The fail-safe outcome is to retain uncertain state and
quarantine the fixture rather than destroy real assets.

## Recovery and closing signals

Every scheduled poll remains a probe. Resolve causes independently:

- **Availability:** one valid new observation restores current reachability for
  its scope. It does not clear freshness or integrity incidents. Record a failed
  attempt even if a retry succeeds; do not count retries as independent polls.
- **Freshness:** two distinct forward clock observations within three minutes,
  or a credible forward phase transition followed by two consistent scheduled
  observations. The second path permits recovery directly into halftime or
  full time. Repeated frozen HTTP-200 snapshots cannot recover freshness.
  Record "progress resumed", not "known current".
- **Fixture integrity:** missing facts return, or explicit supported correction
  evidence authorizes the ordinary event debounce, or an audited operator
  resolves the cause. Assess against the retained causal baseline, not an
  already-corrupted previous poll. Suppressed votes are never replayed later.
- **Provider-wide integrity restriction:** three complete valid scheduled
  observations with no new systemic anomaly and no newly affected fixture.
  Persistently unresolved causal fixtures remain individually restricted and
  do not hold unrelated matches indefinitely. Correlation remains active, so
  a new multi-fixture episode immediately reimposes the provider restriction.

Failed/missing observations reset applicable confirmation streaks; duplicate
activity results and late snapshots do not advance them. No recovery state may
lose its causal evidence on a restart. A successful HTTP response, timer expiry,
or one recovered dimension cannot clear a different unresolved cause.

## Implementation shape

### Boundaries found and repaired locally

- [Active reconciliation](../../../internal/activity/monitor/reconcile.go)
  previously persisted `RefreshActivePoll` before computing its shadow verdict.
  New workflow histories build the complete plan before any write and send it
  through `ProviderMutationRepo.ApplyActive`.
- [ActivePoll](../../../internal/workflow/active_poll.go) aggregates after
  reconciliation and formerly exited early on total fetch failure. It now
  records successful and failed coverage before fan-out, including the
  all-failed path, and fails closed when health collection fails.
- [Ingestion](../../../internal/activity/ingest/categorize.go) and
  [staging](../../../internal/activity/monitor/activation.go) also refresh
  provider facts. New histories use the same mutation boundary and current
  durable policy; date-list payloads without events are not empty event
  inventories or evidence of event-feed recovery.
- The [fact translation](../../../internal/activity/monitor/provider_observation.go)
  currently filters out untracked VAR/yellow-card evidence. Preserve relevant
  raw correction facts for assessment without changing which events we search.

Implemented local active path:

```text
FetchLiveFixtures
  -> validate envelope and requested-ID coverage
  -> AssessProviderObservations(successes + failures, durable baselines)
  -> persist health causes / restrictions / observation receipt
  -> ReconcileFixture(policy, observation, generation) concurrently
  -> post-commit discovery, cleanup, and existing NATS publication
```

Evaluation remains pure and typed. The collector and reconciler reuse the
existing event matcher rather than inventing a second identity scheme. The
activity owns persistence. All provider writers use the same observation
ordering and guarded repository boundary, including updates to already-completed
rows. Fresh historical terminal ingests retain their direct-complete path for
eligible observations; this work does not add historical event polling or
change FF-088 reactivation.

Within one fixture, permitted provider fields, event votes/mutable updates,
required audit rows, downstream checklist registration, and completion commit
in one transaction. Provider and fixture locks check the observation baseline,
health receipt, and restriction generation. Stale plans return a durable no-op
rather than applying. Different fixtures remain concurrent. No lock crosses
HTTP, Temporal, NATS, or Garage calls. Idempotent mutation receipts return
committed effects on retry so event removal cannot lose its cleanup.

`positive_only` requires an explicit reconciliation plan. Passing the raw API
fixture to the current full-snapshot writer and merely skipping absence votes
would still persist score, status, terminal, penalty, and winner regression.

The durable surfaces are:

- provider-level restriction causes, generation, request coverage, and recovery
  evidence;
- per-fixture observation/progress facts plus unresolved causes and the minimal
  score/phase/event baseline needed to resolve each cause;
- a bounded internal transition ledger and idempotent observation receipts.
  Preserve compact before/after evidence, not full JSON for every 30-second poll.
- a bounded per-observation/per-fixture mutation receipt containing the committed
  effect set, not a second canonical snapshot.

Do not create a second copy of the fixture/event database. Progress fields are
typed; incident-specific evidence can be bounded versioned JSON. Keep observation
time, last usable response, clock progress, and public `last_activity_at`
distinct. No health write may reorder fixtures or emit `fixture.update` merely
because an internal warning changed. Reuse the configured terminal grace and
existing downstream settlement gates; no new fixture-completion debounce.

The anomaly ledger must not reuse `event_log`: that table is also the public
SSE/NATS transition source. Internal provider-health evidence is not a public
fixture mutation.

Required telemetry includes active restrictions, transition count, affected
fixtures, progress age, verdict/reason counts, suppressed mutation counts,
coverage ratio, missing confirmed events, recovery evidence, and incident age.
Log cause transitions and bounded summaries rather than the same warning every
poll. Include fixture IDs in diagnostic records, not unbounded metric labels.
Local poll execution/observation gaps need their own signal; they must not be
reported as proven vendor outages. Operator actions require durable reasons.

## Delayed-goal search recovery

Keep ordinary search unchanged. For a new event first observed during a known
availability/freshness gap or its recovery, initialize its durable floor once:

```text
ordinary_floor = first_seen_at - configured_lookback
gap_floor = last_credible_pre_gap_observation - configured_lookback
recovery_floor = max(first_seen_at - 90 minutes,
                     min(ordinary_floor, gap_floor))
```

For a frozen clock, the baseline is its last credible advancement, not the
later warning timestamp or a recent identical HTTP-200 response. For a fetch
gap, use the last usable observation. If no baseline exists, retain the normal
floor and mark recovery coverage unknown rather than inventing one.

The **90-minute** recovery cap is proposed: it covers both the approximately
20-minute frozen-feed example and the historical 75-minute regression while
bounding older searches. Record a truncated/incomplete-coverage reason if the
gap exceeds the cap, the baseline is unavailable, or a scan hits existing
scroll/time/rate limits. No claim that every tweet in that interval was read.
Keep existing scan limits, usable-attempt budget, and Twitter query text; wider
lookback can cost more timeline requests, so FF-038 shared-budget work remains
relevant but is not bundled here. Do not put time operators into the X query.

Capture the gap reference when inserting the event, including events first
seen on the recovery poll. Do not rewrite `first_seen_at`, reinterpret match
minutes as wall time, retroactively expand initialized windows, or automatically
replay completed discoveries. Keep the existing durable floor and seen-stop
rules after initialization. This is an explicit proposed downstream contract
change that needs Temporal versioning and regression coverage, not a repair
already applied to today's goals.

## Rollout

1. **Shipped:** harden the API envelope and requested-ID coverage contract.
2. **Shipped:** add the pure evaluator in shadow mode. Record typed workflow
   verdicts and adapter contract metrics while preserving current mutations.
3. **Shipped September 9 (`a7c9f53`):** both shadow windows are reviewed.
   The September 8 regression repair excludes only empty unchanged PST
   scoreboards, including all retained/raw event presence as counterevidence.
   Enforcement remains off.
4. **Local September 16:** pure freshness, scope, recovery, and policy-composition
   helpers plus Monitor/SQL writer characterization.
5. **Local September 16:** durable observations, causal baselines, failed-attempt
   receipts and versioned active collection, including all-failed fetches. The
   migrations and shadow-default application were deployed with approval on
   September 17 UTC. This is not closure of FF-075.
6. **Local September 16:** implement the guarded transaction path across active,
   staging, and ingest writers. The August destruction replay, whole-plan
   rollback, ingest bypass, staging activation, response-loss retry, and stale
   collector-baseline tests pass. Audited operator restriction/resolution remains
   separate; never enable only a subset of provider writers.
7. The exact staged snapshot passes unchanged `make check` (see the
   [verification limits](../../provider-health.md#verification-checkpoint--september-16)).
   The two ordered migrations are applied. The September 17 local follow-up
   defaults `PROVIDER_INTEGRITY_MODE` to `shadow` and distinguishes recommended
   restrictions from applied effects in durable receipts and workflow counters.
   The compatible histories/binary deployed as `abfea7a`. Review at least one
   complete match window before separately enabling enforcement. Validate
   natural active, staging, ingest, anomaly, and recovery behavior before closure.
8. Version and test incident-linked search-window initialization separately.
   It can follow the protection release; it is not necessary to detect staleness
   or prevent destructive regressions. No ranking/quality policy changes here.

Policy tests now pin exact timing boundaries, evidence serialization/gaps,
distinct-fixture correlation, logical-observation deduplication, and recovery
directly into FT. They also reject recovery that later freezes again and use
the latest qualifying clock advances rather than a window stuck on the first.
Real-Postgres tests cover restart-independent collector state, concurrent
retries, stale polls, whole-plan rollback, retention, migration, ingestion and
staging guard use, event vote suppression, and collector/write races. Natural
production validation and operator/search follow-ups remain pending.

Do not add a frontend state or a new Temporal workflow. The current active-poll
schedule supplies probes, and the portal should continue showing the last
trusted state while provider trust is degraded.

## Required regression cases

- normal match progression and new-goal discovery;
- isolated coherent goal cancellation with and without explicit VAR evidence;
- red-to-yellow and missed-to-converted-penalty replacements;
- missing versus explicit-empty event arrays;
- nonempty envelope errors, results mismatch, paging mismatch, missing,
  duplicate, and unrequested fixture IDs;
- suspended, interrupted, postponed, extra-time, and shootout transitions;
- unchanged empty PST zero/null scoreboards, isolated versus global anomalies,
  and retained/raw event evidence that disqualifies the deferred exception;
- terminal-to-live and material clock rollback;
- frozen running-clock HTTP-200 observations while unrelated fixtures advance;
- repeated successful observations do not reset progress age or recover a
  freshness incident; ordinary pauses and terminal fixtures do not false-trip;
- correlated multi-fixture staleness versus one recurring unresolved warning;
- clock advancement with missing event coverage does not assert completeness;
- late first observation after a provider stall and bounded Twitter recovery;
- freshness recovery with an unresolved semantic regression, and vice versa;
- the 2026-08-29 multi-fixture regression: first bad batch restricts writes,
  three bad polls create no absence votes, and no share/object is destroyed;
- positive events continue to debounce and launch discovery while restricted;
- no terminal retirement from a restricted observation; incomplete coverage
  alone still permits the configured grace and downstream-settlement path;
- three qualifying probes release the provider restriction while unresolved
  fixtures remain restricted;
- activity retry, Temporal replay, concurrent manual invocation, worker restart,
  and stale circuit-generation behavior.

## Out of scope

- Replacing API-Football or building a scraped primary feed.
- Treating generic HTTP latency as football-state corruption.
- Machine-learned or automatically adapting thresholds before deterministic
  rules produce a measured false-positive corpus.
- Using the breaker as a substitute for the event debounce, terminal grace,
  or downstream workflow checklist.
