# docs/rebuild/ — Go rebuild knowledge base

This directory is the **incoming knowledge layer for the Go rebuild** —
architecture, operations, and reference docs describing the system as it
will exist post-cutover. Populated incrementally during Phase S onward
(§16.3 in [`../rebuild-plan.md`](../rebuild-plan.md)) as each adapter,
domain, and workflow lands.

## Parallel-truth phase (per rebuild-plan §15.10)

During the rebuild build window, two doc trees coexist:

| Path | Describes | Status |
|------|-----------|--------|
| [`../*.md`](..) (top-level `docs/`) | The Python system currently in prod | Frozen. Do not edit. |
| `./` (this directory, `docs/rebuild/`) | The incoming Go system | Grows during Phase S through C |

Both are current, for different audiences. When someone asks
"how does this work today," point at `docs/`. When someone asks "how
will this work after cutover," point here.

At cutover completion (§15.10 Phase B), `docs/rebuild/*.md` moves to
top-level `docs/*.md` and the current top-level docs move to
`docs/legacy/`.

## What lives here (targets)

- [`architecture.md`](./architecture.md) — domain model + adapter tree + workflow hierarchy
- [`orchestration.md`](./orchestration.md) — event lifecycle state machine, debouncing, VAR
- [`temporal.md`](./temporal.md) — per-workflow retry / timeout / heartbeat policies
- [`observability.md`](./observability.md) — log + metric + trace catalog
- [`api-contract.md`](./api-contract.md) — HTTP + SSE + webhook contract (auto-derived from Huma tags)
- [`logging.md`](./logging.md) — structured JSON emission convention + Loki query cookbook
- [`operations.md`](./operations.md) — runbook: bring-up, scaling, common issues
- [`deployment.md`](./deployment.md) — docker-compose reference + Caddy routes
- [`testing.md`](./testing.md) — three-tier pyramid + factory conventions

Every file exists as a stub during Phase F. They get filled in as the
corresponding code lands.

## Where the design lives right now

Until each stub gets populated, the canonical description of the Go
rebuild's target design lives in
[`../rebuild-plan.md`](../rebuild-plan.md). Section pointers per file:

| Stub | Design lives at (rebuild-plan.md sections) |
|------|-------------------------------------------|
| `architecture.md` | §2, §4, §9 |
| `orchestration.md` | §5, §6 |
| `temporal.md` | §5 (retry/timeout tables), §16.5 |
| `observability.md` | §11 |
| `api-contract.md` | §8 |
| `logging.md` | §11 (vocabulary + Loki query cookbook) |
| `operations.md` | §10, §14 |
| `deployment.md` | §10 |
| `testing.md` | §12 |

Once a stub gets populated, its rebuild-plan section becomes "historical
context" per §15.7 — the stub is the source of truth going forward.

## Intake rules

Same as top-level `docs/README.md` — architectural decisions land in
[`../decisions.md`](../decisions.md), open work in
[`../todo.md`](../todo.md). Structural facts about the Go system land
here, in the relevant stub.
