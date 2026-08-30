# Provider fixture responses require a contract and shadow trust assessment

## Context

API-Football can return HTTP 200 while its envelope is erroneous, incomplete,
or semantically inconsistent with earlier fixture state. The 2026-08-29
incident persisted long enough to pass the event absence debounce: confirmed
events were removed and public assets were destroyed before the omitted facts
returned. `last_polled_at` only orders observations; it cannot prove that a
newer observation is credible.

The previous adapter decoded only `response` and ignored envelope errors,
results, paging, exact requested-ID coverage, and whether an ID-based fixture
actually carried an event array. Reconciliation then treated every decoded
fixture as authoritative state.

## Decision

Every `/fixtures` response must pass a typed wire contract before reaching an
application caller:

- `errors` is present and empty;
- `results` equals the decoded response length;
- paging is present and complete in one page;
- fixture and team identities are valid and unique, scores are nonnegative,
  and every event team belongs to the fixture;
- an `ids=` chunk returns every requested fixture exactly once and no other
  fixture; and
- each by-ID fixture sends `events` as an array. Missing and `null` are invalid;
  explicit `[]` is a valid empty inventory.

A rejected chunk follows the existing partial-failure contract: its IDs enter
`failedIDs` and the caller retries on its normal cadence. If every chunk fails,
the fetch fails. Contract reasons are typed, logged without the raw payload,
and counted under one bounded Prometheus label.

Validated active-fixture observations also receive a non-enforcing semantic
assessment. Monitor translates the stored pre-write fixture plus confirmed
event history and the new response into provider-independent facts. It reuses
the same canonical event sequence allocation as reconciliation. The pure
evaluator emits per-fixture and batch `trusted`, `positive_only`, or `rejected`
recommendations with typed reasons.

The shadow batch recommends provider-wide `positive_only` after two anomalous
fixtures or three missing confirmed events. One fixture remains isolated. A
single disappearing goal remains trusted only when it is recent, the correct
score side decreases by one, the other side is unchanged, and the remaining
goal inventory exactly matches the new score.

Shadow recommendations do not alter fixture writes, event votes, completion,
or cleanup. Enforcement requires the separate durable circuit/quarantine phase
designed in the
[provider-integrity proposal](../design/proposals/provider-integrity-circuit-breaker.md).

## Consequences

- Structurally invalid or incomplete fixture payloads cannot mutate local
  state; one missed poll is preferred to accepting an ambiguous snapshot.
- Semantic thresholds can be calibrated from live evidence before they gain
  destructive authority.
- Event identity has one implementation. The assessor cannot drift from the
  sequence matcher used by reconciliation.
- This phase adds no schema, service, dependency, queue, or Temporal workflow.
- Until enforcement ships, a structurally valid semantic regression can still
  execute the current destructive path. FF-075 remains open.
