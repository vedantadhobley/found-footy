# docs/design/ — design history and audit evidence

Target architecture, design proposals, and point-in-time audits from the Go
rebuild. This directory is not the active backlog; use
[`../todo.md`](../todo.md) for current issues and deferred work. For
**what's already built**, see the
as-built ledgers in [`../`](../) (start at [`../README.md`](../README.md),
the routing index + authority map). For the **legacy Python** system,
see [the frozen Python docs](https://github.com/vedantadhobley/found-footy/tree/bfcbc4247f6c95ab22e75027091b84310ec25438/archive/docs).

Historical `archive/` paths in these documents refer to the
[pre-removal source snapshot](https://github.com/vedantadhobley/found-footy/tree/bfcbc4247f6c95ab22e75027091b84310ec25438/archive)
at commit `bfcbc4247f6c95ab22e75027091b84310ec25438`, not the current working
tree. See the [Python working-tree retirement decision](../decisions/2026-10-21-retire-python-working-tree.md).

## Foundational history

- [`rebuild-plan.md`](./rebuild-plan.md) — original 12,000-line target design.
  It contains substantial superseded topology, naming, schema, deployment, and
  cutover material. Use it as historical intent, never as current behavior.
  Project instructions and the docs authority map now classify it as historical
  evidence rather than mandatory implementation authority.
- [`python-functional-spec/`](./python-functional-spec/) — behavioral spec
  of the retired Python system. Use it with `archive/` as parity evidence, not
  as the Go architecture.
- [`v-phase-orchestration.md`](./v-phase-orchestration.md) — rationale for the
  shipped `EventWorkflow`/`VideoWorkflow` shape. Current workflow behavior lives
  in the [`orchestration` ledger](../orchestration/).

## Point-in-time audits

[`audits/`](./audits/) contains the routed archive from the pre-rebuild analysis
through the 2026-08-15 cutover audit. Audits preserve evidence; they are not
backlogs. Surviving findings resolve through [`../todo.md`](../todo.md).
The [Raphinha/Cancelo media inventory](./audits/event-media-identity-inventory-2026-09-23.md)
records FF-102's event-scoped production evidence and early-reclamation boundary.

## Proposal disposition

[`proposals/README.md`](./proposals/README.md) classifies every proposal as an
active design, shipped rationale, superseded topology, or a retired subsystem.

## Convention

A design doc describes intent. When the thing ships, truth moves to a focused
as-built ledger and the decision log. Keep historical rationale only while it
remains unique and clearly labeled. Move or delete a historical document only
after checking its unique content and inbound links.
