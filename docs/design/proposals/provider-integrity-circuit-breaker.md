# API-Football provider-integrity circuit breaker

**Status:** Proposed design for
[`FF-075`](../../todo.md#ff-075--successful-provider-responses-can-destructively-regress-live-state).
No contract in this document has shipped. Current behavior remains authoritative
in the [fixture-monitoring ledger](../../orchestration/monitor.md).

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

The breaker protects local state from untrusted data. It does **not** stop API
calls. While provider trust is degraded, Found Footy continues to:

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

## State and scope

Provider-wide state uses four values:

| State | Mutation policy |
|---|---|
| `closed` | Per-fixture assessment controls the policy. |
| `open` | Every returned fixture is at most `positive_only`; polls are recovery probes. |
| `recovering` | Clean-probe streak is advancing; destructive writes remain suppressed. |
| `forced_open` | Operator kill switch; automatic probes cannot close it. |

An isolated semantic anomaly quarantines one fixture instead of opening the
provider globally. A provider-wide open protects all fixtures only when the
batch demonstrates shared failure. After global recovery, unresolved causal
fixtures remain quarantined so one ambiguous match cannot freeze unrelated
matches.

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

The current adapter decodes `errors` but ignores it and does not model
`results` or `paging`. It also marks a successful HTTP chunk complete without
checking that every requested ID appeared.

### Quarantine one fixture

- home, away, league, or fixture identity changes unexpectedly;
- score decreases without a supported correction signature;
- terminal status returns to live;
- phase or clock moves materially backward;
- a confirmed event disappears;
- several populated facts clear together.

### Open the provider circuit

- two unrelated fixtures show strong semantic regression in one batch;
- at least three confirmed events disappear in one batch;
- a response-envelope or coverage defect affects a complete request chunk;
- event, score, and status regression co-occur across fixtures.

Percentages remain telemetry rather than the primary threshold. Live batches
are often small; one legitimate correction in a two-fixture batch must not be
treated as a 50% provider outage.

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

Every open-state poll is a probe. A timer alone never closes the circuit.

1. Every requested fixture returns exactly once.
2. Envelope, paging, identity, and event-presence validation passes.
3. No new semantic regression appears.
4. The systemic behavior that opened the provider circuit has stopped.
5. The first clean batch moves `open` to `recovering`.
6. Three consecutive clean batches close the provider circuit.
7. Any anomaly resets the streak and returns to `open`.

The streak counts observations, not elapsed wall time. ActivePoll uses Temporal
overlap-skip, so a slow cycle can stretch the nominal 30-second interval.

Global recovery does not silently resolve affected fixtures. A fixture leaves
quarantine only when its missing facts return, a supported correction persists
for three clean observations, or an audited operator or future secondary source
adjudicates it.

## Implementation shape

Add one batch assessment between `FetchLiveFixtures` and concurrent
`ReconcileFixture` calls:

```text
FetchLiveFixtures
  -> validate envelope and requested-ID coverage
  -> AssessFixtureBatch against durable fixture/event state
  -> persist circuit transition and anomaly evidence
  -> ReconcileFixture(policy) concurrently
```

The evaluator belongs in a pure domain package and returns typed reason codes.
The activity owns database reads and durable circuit transitions. Reconcile
receives the policy, observation time, and circuit generation; a repository
write rejects a stale generation rather than applying an assessment made before
a concurrent state transition.

`positive_only` requires an explicit reconciliation plan. Passing the raw API
fixture to the current full-snapshot writer and merely skipping absence votes
would still persist score, status, terminal, penalty, and winner regression.

The minimum durable surfaces are:

- one provider-circuit row with state, generation, timestamps, cause summary,
  and clean-probe streak;
- active per-fixture quarantine records with causal baseline and resolution;
- a bounded internal anomaly ledger containing compact verdict evidence and
  raw provider JSON only for anomalies/state transitions.

The anomaly ledger must not reuse `event_log`: that table is also the public
SSE/NATS transition source. Internal provider-health evidence is not a public
fixture mutation.

Required telemetry includes circuit state, transition count, quarantined
fixtures, verdict/reason counts, suppressed mutation counts, coverage ratio,
confirmed-event disappearance count, clean-probe streak, and open duration.
State transitions and operator actions require durable reason evidence.

## Rollout

1. Harden the API envelope and requested-ID coverage contract.
2. Add the pure evaluator in metrics-only mode. Record verdicts but preserve
   current mutations.
3. Run the regression corpus and at least one live match window; classify every
   false trip before enforcement.
4. Apply the additive provider-state/quarantine migration.
5. Enforce `rejected` and `positive_only` policies.
6. Add audited force-open and fixture-resolution operations. Force-close must
   require a reason and must not erase incident evidence.

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
- terminal-to-live and material clock rollback;
- the 2026-08-29 multi-fixture regression: first bad batch opens the circuit,
  three bad polls create no absence votes, and no share/object is destroyed;
- positive events continue to debounce and launch discovery while open;
- terminal fixtures cannot complete from an open/quarantined observation;
- three clean probes close the provider globally while unresolved fixtures
  remain quarantined;
- activity retry, Temporal replay, concurrent manual invocation, worker restart,
  and stale circuit-generation behavior.

## Out of scope

- Replacing API-Football or building a scraped primary feed.
- Treating generic HTTP latency as football-state corruption.
- Machine-learned or automatically adapting thresholds before deterministic
  rules produce a measured false-positive corpus.
- Using the breaker as a substitute for the event debounce, terminal grace,
  or downstream workflow checklist.
