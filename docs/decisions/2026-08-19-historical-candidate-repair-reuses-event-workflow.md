# Historical candidate repair reuses EventWorkflow

## Context

FF-056 and FF-057 corrected deterministic clock interpretation after the
Barcelona–Al Ahly fixture had completed. The original EventWorkflow
executions had already made all candidate rows terminal. Temporal replay does
not rerun completed business work, and a fresh discovery execution would
exclude every known tweet while the normal age window would reject historical
search results.

Directly changing candidate verdicts or inserting assets would bypass current
download, filtering, vision, deduplication, ranking, publication, and cleanup
contracts. Resetting every rejected candidate would also reconsider unrelated
content failures without a code change that justified doing so.

## Decision

Historical candidate repair uses a new deterministic EventWorkflow identity
per event. One Postgres transaction:

- inserts its own `event_downstream_workflows` checklist row;
- checkpoints `attempts_completed` at the configured maximum, so the workflow
  performs no Twitter search;
- selects only candidates with the exact terminal outcome and reject reason
  named by the repair; and
- moves that selection to `pending` while embedding the prior verdict, detail,
  repair kind, workflow identity, and queue time in `outcome_detail.replay`.

The normal EventWorkflow then restores current assets and every candidate,
re-drives only the selected pending rows, and uses the ordinary candidate
pipeline. Terminal UPSERT preserves the replay envelope beside the new verdict.
The runner waits for each event and verifies that its checklist closed, its
selected count is unchanged, and no selected row remains pending before it
starts the next event.

Candidate detail merge treats both SQL `NULL` and JSON `null` as an empty
object before attaching the replay envelope. A rerun normalizes only the exact
legacy `[null, {"replay": ...}]` shape owned by the same workflow identity;
this makes a stopped repair self-healing without broad JSON rewrites.

The checked-in runner is dry-run by default. Apply mode requires the expected
event count, enforces a per-event candidate ceiling, uses failed-only Temporal
Workflow ID reuse, and is idempotent across a process crash between the
Postgres transaction and workflow start. Running it against production is a
separate explicit production mutation from deploying corrected worker code.

## Consequences

- Repair benefits from the same validation, deduplication, persistence,
  ranking, publication, retry, and cleanup behavior as a new candidate.
- Existing active shares remain available while historical candidates run.
- No Firefox instance is provisioned and no historical Twitter search is
  attempted. The final release call is harmless when no event browser exists.
- Previous evidence remains queryable; repair does not rewrite history into
  appearing as an original verdict.
- An exact selector is part of the durable workflow identity. A later repair
  must use a new kind and workflow ID rather than broadening an existing run.
- The operation inherits EventWorkflow's existing effect contracts, including
  any separately tracked idempotency limits such as FF-011.

The as-built behavior is recorded in the
[event orchestration ledger](../orchestration/event.md) and the operator
procedure is in the [operations runbook](../operations.md#historical-candidate-replay).

## Production exercise

The Barcelona–Al Ahly repair selected 104 exact clock rejects across four
events. Its first process completed the 39-candidate Abdelkarim workflow, then
stopped when verification exposed 31 legacy `[null, {"replay": ...}]`
envelopes. It did not prepare the other three events.

After the null-safe merge and exact self-normalization shipped in `70fca8f`,
rerunning the same command normalized those 31 rows, verified the completed
identity, and processed the remaining `15/18/32` candidates sequentially. All
four checklists completed as `assets_surfaced`; the final database audit found
the exact `39/15/18/32` replay counts, zero pending rows, and zero malformed
arrays. The run left no Firefox fleet container and emitted no worker warning
or error. This exercises the decision's crash-resume and evidence-preservation
contracts against production state; natural live validation still governs
FF-057 closure.
