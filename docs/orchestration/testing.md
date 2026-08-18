# Orchestration testing and references

Workflow test boundaries and supporting sources. See the
[orchestration index](./README.md) for the current workflow inventory.

## Testing shape

Two-layer testing pattern matches plan §12:

**Unit tests for activities** —
`internal/activity/ingest/activities_test.go`. In-memory fake
`fixture.Repo` + `alias.Repo` + `fixtureFetcher`. Tests state-transition
edge cases (fresh terminal,
existing preserves domain fields, empty input, mixed existing/new
aliases, prune threshold).

**Workflow-level tests** — `internal/workflow/ingest_test.go` using
`testsuite.WorkflowTestSuite`. Mocks activities by name via testify
`OnActivity`. Tests the workflow's control flow: activity call
order, conditional-skip branches (empty TeamRefs skips alias step,
zero RetentionThreshold skips prune step), error propagation +
retry policy behavior.

**Live end-to-end via scripts/trigger_ingest** — dev-only script
that dials Temporal, fires a real IngestWorkflow with a tight
window, waits for completion. Exercises the whole chain against
real api-sports.io + dev pg. Used for O1d verification.

## Cross-refs

- Plan §5 (orchestration + workflow inventory) —
  [rebuild-plan.md §5](../design/rebuild-plan.md#5-orchestration-layer--temporal-workflows-and-activities)
- Plan §5 W1 (IngestWorkflow spec — the intent) —
  [rebuild-plan.md §5 W1](../design/rebuild-plan.md#workflow-1-ingestworkflow)
- Divergences from plan for IngestWorkflow —
  [decisions.md](../decisions.md)
- Activity inventory (plan) —
  [rebuild-plan.md activity inventory](../design/rebuild-plan.md#activity-inventory-by-domain-package)
- Architecture ledger (what packages exist) —
  [architecture.md](../architecture.md)
- Temporal specifics (client + worker shape) — [temporal.md](../temporal.md)
