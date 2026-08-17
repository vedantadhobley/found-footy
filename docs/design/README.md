# docs/design/ — design history and audit evidence

Target architecture, design proposals, and point-in-time audits from the Go
rebuild. This directory is not the active backlog; use
[`../todo.md`](../todo.md) for current issues and deferred work. For
**what's already built**, see the
as-built ledgers in [`../`](../) (start at [`../README.md`](../README.md),
the routing index + authority map). For the **legacy Python** system,
see [`../../archive/docs/`](../../archive/docs/).

## Foundational history

- [`rebuild-plan.md`](./rebuild-plan.md) — original 12,000-line target design.
  It contains substantial superseded topology, naming, schema, deployment, and
  cutover material. Use it as historical intent, never as current behavior.
  DOC-006 tracks removing the remaining project instruction that treats it as
  mandatory implementation authority.
- [`python-functional-spec.md`](./python-functional-spec.md) — behavioral spec
  of the retired Python system. Use it with `archive/` as parity evidence, not
  as the Go architecture.
- [`v-phase-orchestration.md`](./v-phase-orchestration.md) — rationale for the
  shipped `EventWorkflow`/`VideoWorkflow` shape. Current workflow behavior lives
  in [`../orchestration.md`](../orchestration.md).

## Point-in-time audits

[`audits/`](./audits/) contains the routed archive from the pre-rebuild analysis
through the 2026-08-15 cutover audit. Audits preserve evidence; they are not
backlogs. Surviving findings resolve through [`../todo.md`](../todo.md).

## Proposal disposition

[`proposals/README.md`](./proposals/README.md) classifies every proposal as
shipped rationale, superseded topology, or a retired subsystem.

## Convention

A design doc describes intent. When the thing ships, truth moves to a focused
as-built ledger and the decision log. Keep historical rationale only while it
remains unique and clearly labeled. DOC-007 will move or delete documents only
after their unique content and inbound links have been checked.
