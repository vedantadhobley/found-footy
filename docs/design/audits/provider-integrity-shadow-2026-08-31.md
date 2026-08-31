# Provider-integrity shadow audit — 2026-08-31

## Scope

This audit classifies every FF-075 semantic warning emitted after the shadow
evaluator reached production on 2026-08-30. Evidence came from read-only worker
logs, canonical Postgres rows, and the exact `ReconcileFixture` inputs retained
in Temporal history. No production state changed during the audit.

The audit covers the wire contract, fixture evaluator, batch aggregation, and
the activity/workflow failure boundary. It does not approve durable circuit
enforcement.

## Production result

The evaluator emitted 15 warnings across 12 distinct episodes.

| Observation | Warning records | Classification |
|---|---:|---|
| Four unrelated fixtures simultaneously returned empty event arrays, omitting ten confirmed events | 1 | true systemic regression |
| Augsburg score changed from 1–0 to 0–0 while the goal event remained present | 1 | true fixture inconsistency |
| St. Louis changed from `1H`, 1–1 to `NS`, null–null, then recovered | 1 | true fixture regression |
| Rennes changed from `2H 90+15` to `2H 46`, then `FT` | 1 | true clock regression |
| Six fixtures changed from `HT 45+N` to `2H 46` | 6 | normal period transition; false trip |
| Deportivo replaced J. Guerra with C. Tárrega for the same 13-minute own goal | 3 | supported identity refinement; false trip |
| Columbus cancelled a 74-minute goal and reduced 1–3 to 1–2 | 2 | supported correction continuation; false trip after the first vote |

The global thresholds worked as intended. At 14:29 UTC, four unrelated
fixtures returned unchanged score and clock data but explicit empty event
arrays. The evaluator recommended provider-wide `positive_only` on the first
bad batch because ten confirmed events disappeared. All event arrays returned
on the next poll. The existing event debounce prevented damage during this
one-poll incident; the 2026-08-29 75-minute incident would not have been safe
without enforcement.

No wire-contract rejection occurred in this window. One ordinary connection
reset was transport failure, not semantic corruption.

## Findings

### Period progress cannot add stoppage time across boundaries

The evaluator compared `elapsed + extra` without considering phase. A normal
`HT 45+4` to `2H 46` transition therefore looked like `49 → 46`. Clock
rollback must compare within a period and treat forward phase transitions as
forward progress. `ET ↔ BT` also needs explicit handling because API-Football
uses one `ET` code for both extra-time periods.

### Correction authority must survive the absence debounce

The first Columbus cancellation poll was trusted because the score dropped by
one and the remaining goal inventory matched it. Reconciliation then stored the
lower score and cast the first absence vote. On the next poll, the score no
longer decreased relative to storage, so the stateless evaluator forgot that
the same correction was already in progress.

The supported signature must therefore include continuation through the
existing event debounce. A missing goal with a partial absence count remains
trusted when the score is stable, the inventory still matches, and the
correction remains inside the age window. This reuses the durable event counter
instead of adding a second debounce.

### Exact identity replacement is positive correction evidence

Deportivo retained the same score, team, type, detail, and minute while changing
only the scorer identity. Treating the old key as an unsupported disappearance
would quarantine a routine attribution correction. A replacement is supported
only when exactly one confirmed event disappears, exactly one unmatched event
appears, both describe the same event within one minute, both players are
known and different, and no score or other fixture regression occurs.

### Fetch failures need typed workflow evidence

The adapter classified contract failures internally but returned only
`failedIDs` to Monitor. That erased the distinction between downtime and a
malformed successful response before the future circuit could act on it.
Chunk results now retain `transport` versus `contract` plus the bounded
contract reason. Total failure still crosses Temporal as a retryable activity
error, with the typed result attached as error details, so existing retries and
the error contract remain intact.

### Rejection is fixture-scoped

The evaluator declared `rejected` but never emitted it. Its aggregator would
also have promoted one rejected fixture to a rejected global batch. An identity
conflict cannot safely accept positive events, so it now rejects that fixture.
The batch remains globally trusted unless the ordinary systemic thresholds are
met.

## Local repair and verification

The branch now contains non-enforcing repairs for every false-trip class above,
typed fetch-failure propagation, and an exact August 30 regression corpus. The
complete engineering gate passes, including Postgres-backed integration tests.
The evaluator remains advisory: it still changes no fixture, event, share,
object, completion, or circuit state.

## Enforcement gate

Before FF-075 can enforce policies:

1. Deploy the repaired evaluator in shadow mode.
2. Classify at least one additional live match window.
3. Add the durable circuit, quarantine, anomaly ledger, generation checks, and
   operator controls.
4. Move batch assessment ahead of every provider-derived write and enforce the
   resulting fixture and global policies.

The production shadow evidence validates the circuit design. It does not
validate the original classifier unchanged.
