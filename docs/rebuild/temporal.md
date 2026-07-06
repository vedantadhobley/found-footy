# temporal.md — Go rebuild

**Phase F stub.** Populated during Phase O as workflows + activities
land + retry policies become code.

Target content:

- Per-workflow: workflow ID convention, retry policy, timeout config,
  signal contracts, child-workflow spawn patterns
- Per-activity: input/output types, timeout, retry override,
  non-retryable error classes
- Workflow ID reuse policies (REJECT_DUPLICATE for direct spawns,
  ALLOW_DUPLICATE for SignalWithStart)
- Auto-generated from `cmd/worker/main.go` registration + AST walk of
  `internal/workflow/*.go` and `internal/activity/*.go` per
  [`../rebuild-plan.md`](../rebuild-plan.md) §15.3

Current design source of truth: [`../rebuild-plan.md`](../rebuild-plan.md)
§5 (workflow specs + activity inventory), §16.5 (phase O milestones).
