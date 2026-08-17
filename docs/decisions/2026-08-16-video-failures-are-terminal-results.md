# Exhausted video activities return terminal candidate results

## Context

Two Huijsen candidates downloaded and staged the same 108 MB clip, then each
exhausted `HashVideo` retries on an ffmpeg extraction timeout. `VideoWorkflow`
returned an error. The EventWorkflow callback decremented its in-flight count
and discarded the failed future because no output was available. Both
persisted candidate rows remained `pending`, both staging objects remained,
and the parent completed normally.

The archived Python workflow isolated download and hash exceptions per video
and converted them into failed result records so one candidate could not fail
the batch. The Go child retained the activity error after its own retry policy
was exhausted, despite having no child-workflow retry policy or caller that
could use that error to recover the candidate.

## Decision

For new executions, `VideoWorkflow` converts an exhausted
`DownloadAndStage` or `HashVideo` activity into a successful child-workflow
completion carrying a typed terminal output:

- `outcome=failed`;
- `failure_reason=download_error` or `hash_error`;
- the original tweet URL; and
- the staging key when download succeeded before hashing failed.

Cancellation remains a workflow error. It belongs to event removal and must
not schedule candidate persistence or staging cleanup after the parent context
closes.

The EventWorkflow consumer stamps a typed failed output onto
`event_search_candidates` and deletes a non-empty staging key through the
existing retrying activities. The parent callback also captures the input
tweet URL. If the child fails before returning any output, the parent records
`video_workflow_error` against that URL. An invalid child output records
`video_workflow_invalid_outcome` and reclaims any staging key it did return.
Raw activity errors remain in Temporal history and structured logs; they are
not copied into the candidate table.

Both workflows guard the new command sequence with Temporal change ID
`ff-002-terminal-video-failures`, version 1. A history without the marker uses
the old error path during replay. A new execution records version 1 and uses
the terminal-result path.

## Consequences

- Exhausted download and hash failures no longer leave a candidate pending by
  construction.
- A hash failure cannot orphan its known Garage staging object on the normal
  failure path.
- One failed candidate remains isolated; the parent can drain its other
  children and complete discovery.
- The existing best-effort candidate-forensics and cleanup activity policy is
  unchanged: the activities retry, but an external persistence or deletion
  outage does not fail the whole event pipeline.
- Existing production rows and staging objects are not repaired by deployment;
  any repair is a separate explicitly approved production action.

## Superseded contract

This refines the old “decrement in-flight on child error and continue” branch
from the frozen
[`#164c-b decision`](./archive-through-2026-08-16.md#2026-08-04--164c-b-eventworkflow-producerconsumer-engine-the-v-phase-spine).
It preserves the best-effort write policy from the frozen
[`#181 candidate-outcome decision`](./archive-through-2026-08-16.md#2026-08-15--181-per-candidate-discovery-outcomes-persisted-surfacing-forensics)
while ensuring that child failure now reaches that write path. Current behavior
is recorded in the [`orchestration ledger`](../orchestration.md#eventworkflow)
and tracked as [`FF-002`](../todo.md#ff-002--failed-video-child-leaves-candidate-pending).
