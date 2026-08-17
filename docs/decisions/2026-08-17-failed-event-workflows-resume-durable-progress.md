# Failed EventWorkflow executions resume durable progress

## Context

Monitor registers one `event_downstream_workflows` row and starts one
deterministically named EventWorkflow for each confirmed event. The original
[Temporal-direct spawn decision](./archive-through-2026-08-16.md#2026-07-16--downstream-workflow-spawn-via-temporal-direct--register-on-flip-chain-not-nats)
used `RejectDuplicate`. The implementation also added a 30-minute workflow
execution timeout even though the original rebuild design and the archived
Python behavior gave the discovery cycle no outer timeout.

That combination made an abnormal close permanent. The open downstream row
continued to block fixture completion, but Monitor could not start the same
Workflow ID again. Simply permitting reuse was insufficient because completed
search attempts, candidate ownership, and the live dedup pool existed partly
in the prior execution's memory.

## Decision

EventWorkflow starts use Temporal's typed
`ALLOW_DUPLICATE_FAILED_ONLY` policy. Running and successfully completed
executions still reject duplicate starts; failed, timed-out, canceled, and
terminated executions may start a new run under the same deterministic ID.
The client start call retains its short RPC timeout, but EventWorkflow has no
outer execution timeout. Its configured finite search loop and the existing
activity and child-workflow timeouts bound actual work.

Postgres carries the minimum recovery state across runs:

- `event_downstream_workflows.metadata.attempts_completed` advances
  monotonically after each search attempt has scheduled every discovered
  candidate.
- `event_search_candidates` restores all owned URLs. Terminal rows seed the
  exclusion set; rows still marked `pending` are re-driven.
- Active `video_shares` and their non-superseded assets restore the live
  exact/perceptual dedup and ranking pool.

The replacement execution loads all three before it starts new work, resumes
at the first unfinished attempt, and leaves the downstream checklist open
until normal finalization. No fixture age can stand in for score consistency
or completed downstream work. A Temporal change marker keeps executions that
started before this decision on their original command sequence; new and
replacement histories record recovery version 1.

## Consequences

An unsuccessful closed run can recover without repeating completed searches,
forgetting prior candidates, or treating already surfaced assets as a fresh
dedup universe. Recovery reads are bounded by the candidates and shares for
one event; the share-to-asset lookup is deliberately simple at this scale.

This decision alone does not recover an execution Temporal still classifies as
`RUNNING`. That boundary is now closed by the
[FF-025 progress-proof decision](./2026-08-17-stale-event-recovery-requires-progress-proof.md),
which terminates and re-drives only an exact run with two conservatively spaced
unchanged Temporal snapshots. FF-011 continues to own retry-safe popularity
accounting; recovery does not broaden that existing soft-vote guarantee.

## Superseded contract

This decision replaces only the old spawn decision's `RejectDuplicate`
policy and the unrecorded 30-minute implementation timeout. It preserves
deterministic Workflow IDs, register-before-spawn ownership, duplicate-start
idempotency for active/successful runs, and the downstream completion
checklist.
