# Stale EventWorkflow recovery requires Temporal progress proof

## Context

FF-007 lets a closed unsuccessful EventWorkflow reuse its deterministic ID and
restore durable search, candidate, and asset state. It deliberately cannot
replace an execution Temporal still reports as `RUNNING`. A truly wedged run
therefore leaves `event_downstream_workflows.completed_at` null, blocks fixture
completion, and keeps the event's Firefox container live.

The 2026-08-15 audit proposed an active-fixture maximum-age force-complete
backstop. That remedy is incompatible with the later score-inventory and
downstream-settlement contracts: elapsed time cannot prove that every scoring
event was observed or that downstream work stopped writing.

## Decision

The Temporal spawner uses repeated server state, not fixture age, to prove a
running execution stale:

1. A failed-only duplicate start describes the current execution.
2. The spawner records its exact run ID, history length, state-transition
   count, and observation time.
3. Any run/status/counter change starts a new observation. A newer pending-
   activity heartbeat also advances the progress time.
4. The exact run may be terminated only when it remains `RUNNING`, both
   counters remain unchanged for the full quiet bound, no newer heartbeat
   exists, and total run age exceeds the same bound.
5. Successful exact-run termination is followed by the normal failed-only
   start. FF-007's replacement execution restores the Postgres checkpoint and
   alone owns normal checklist completion.

The quiet bound has a 30-minute floor. Worker bootstrap raises it to twice the
configured discovery attempt spacing or four configured query timeouts plus a
five-minute retry/scheduling allowance, whichever is largest. The floor
exceeds every fixed candidate-activity retry chain and the legacy ten-minute
VideoWorkflow child timeout.

Observations are process-local and synchronized. Losing one on worker restart
delays recovery until another full proof window; it cannot authorize an early
termination. Multiple worker replicas may reach the same proof, but exact run
ID termination and failed-only replacement make the race safe.

Malformed descriptions, Temporal RPC failures, and a run that closes or
changes between description and termination fail closed. They never trigger a
replacement from uncertain state. ReconcileFixture includes these failures in
its error output and retries the still-open checklist on a later poll.

## Consequences

- Slow but progressing workflows remain untouched regardless of total age.
- A run with no observable Temporal or heartbeat progress eventually closes
  as terminated and re-enters the existing durable FF-007 recovery path.
- The detector never updates the downstream checklist, releases Firefox, or
  changes fixture state. Those effects remain owned by normal workflow and
  monitor lifecycle paths.
- Recovery can be delayed by worker restarts or alternating replica
  observations. Safety is preferred over an eager distributed lease; a durable
  observation store can replace the process-local cache if measured recovery
  latency justifies its write and coordination cost.

## Superseded contract

This decision closes FF-007's stale-`RUNNING` boundary and rejects the audit's
fixture-age force-complete proposal. It does not reintroduce an outer workflow
execution timeout or weaken failed-only Workflow ID reuse.
