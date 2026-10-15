# Vision capacity waits in Temporal

Status: deployed on 2026-10-15 as `55c2afd`; natural-load validation remains.
The [rollout record](../history/vision-queue-rollout-2026-10-15.md) preserves
the exact gates, queue-poller proof, and unchanged stateful-service identities.

## Context

The [October 14 recovery record](../history/cucho-recovery-2026-10-14.md)
separates three failure paths: local OCR admission, shared X search allowance,
and selective CDN denial. Validation attempts spent 150–178 seconds waiting
inside Found Footy's chat semaphore after their three-minute execution budget
had already started. Retries repeated the wait and could terminalize a valid
candidate without giving inference a useful execution window.

Control subsequently admitted two concurrent 12B requests while preserving
four physical contexts and its heavy-model guard. That increases available
throughput; it does not fix local queueing or reserve capacity for Found Footy.
This decision owns only the Found Footy execution boundary, not Control policy.

## Decision

- Derive `<base-task-queue>-vision` from the workflow's recorded task queue and
  the worker's existing `TEMPORAL_TASK_QUEUE`. One shared pure naming function
  prevents bootstrap and workflow routing from drifting. Add no environment
  selector or queue configuration.
- Each existing worker process hosts a second, activity-only Temporal worker.
  Register only vision activities there. Its execution-slot cap equals the
  process's existing `LLM_CHAT_CONCURRENCY_CAP`; do not raise that cap. The
  general worker retains its independent workflow/activity limits.
- New EventWorkflow histories route `ValidateClip` through that queue and
  explicitly disable eager execution. A task with no available execution slot
  waits in Temporal, not in an already-running activity or a process-local
  scheduler. Exact/media followers still share one claimant's validation.
- Keep the three-minute StartToClose timeout, one-minute heartbeat, three
  attempts, and existing 60-second model-request timeout. Set no queue-wait or
  total-activity timeout. Parent cancellation still cancels queued tasks.
  Prompts, acceptance, failure classification, public messages, and storage
  contracts remain unchanged.
- Gate routing with `ff-037-vision-task-queue`. Histories without that marker
  retain their original activity destination and eager attributes. Keep
  `ValidateClip` registered on the general worker for those histories. The
  legacy path can still wait on the shared semaphore; it is compatibility,
  not a claim that old histories acquired the new admission contract.
- Start vision polling before general polling. Unwind successful starts on
  partial startup failure. Drain both workers concurrently in one final
  bootstrap closer, before their client, database, fleet, and model adapters
  close. Both SDK workers share the existing shutdown grace period.

## Evidence and boundaries

Unit/workflow regressions pin old/new queue routing, unchanged execution and
retry budgets, disabled eager execution, cap derivation, startup failure, actual
queue lifecycle logging, and overlapping drain. An isolated, capped Temporal
CLI server tests real slot saturation, general-lane progress, cancellation
before activity execution, waiting beyond the execution timeout, and recovery
after vision-poller replacement. No test uses production, X, or an LLM.

This prevents local backlog from exhausting execution deadlines. It cannot
increase model throughput, avoid all Control admission waits, or guarantee a
bounded time to display under sustained overload. Waiting at Control still
consumes the HTTP/activity budgets. ffmpeg admission, shared X-budget
coordination (FF-038), and CDN access (FF-089) remain separate work.

No schema migration, schedule, service, queue database, or frontend cutover is
needed. Roll out after active discovery drains so the retained general-queue
path does not contend with the new lane. Before rolling back to a release
without vision polling, drain new discovery and its vision queue; an old image
cannot execute tasks whose recorded destination is the new queue.
