# Candidate terminal state is a workflow invariant

## Context

EventWorkflow previously persisted observation and outcome as two unrelated
best-effort writes. `StoreCandidate` ran before clip launch, but an exhausted
insert error was logged and ignored. `RecordCandidateOutcome` later used an
`UPDATE` whose zero-row result counted as success, and the workflow discarded
its error. The parent could therefore complete while a surfaced candidate had
no forensic row or remained `pending`.

Temporal preserves workflow commands and retries activities. It cannot make
two application-level database calls atomic, prove that an ignored call wrote
a row, or infer that a zero-row update violated the candidate contract.

## Decision

New EventWorkflow executions own one immutable `CandidateEvidence` value from
Twitter observation through terminal processing. It contains the event and
fixture IDs, search query and attempt, tweet URL and text, video-page URL,
author, duration, and observed age.

Candidate processing is dispatched before the producer awaits the concurrent
observation inserts. Observation persistence therefore does not add a launch
barrier. A failed observation insert prevents that search attempt from being
checkpointed, but already-launched candidates continue processing.

Every terminal path calls one idempotent UPSERT with the complete evidence and
terminal result. It can create the missing observation row or update an
existing pending row. A candidate becomes terminal in workflow memory only
after that activity succeeds. EventWorkflow cannot finalize its downstream
checklist while a candidate is non-terminal or a terminal write has failed.

Recovery loads complete evidence and explicit ownership state. Durable pending
rows return as observed and are re-driven; the new execution marks them
in-flight locally. Terminal rows only seed the Twitter exclusion set.

Temporal change ID `ff-034-candidate-durability`, version 1, selects this
command sequence. Older histories retain their original store-before-launch
and best-effort `RecordCandidateOutcome` commands.

## Consequences

- Parent completion now proves that every workflow-owned candidate has durable
  evidence and one terminal outcome.
- A terminal retry is safe whether or not the observation insert landed.
- Postgres latency does not delay the first download, although observation
  durability still gates search-attempt checkpointing.
- The serialized consumer still waits for terminal persistence. Moving
  ancillary I/O out of that selector is separate FF-046 work and must preserve
  this invariant.
