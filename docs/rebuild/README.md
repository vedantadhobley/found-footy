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

## What lives here + current status (2026-07-07)

| Doc | Status | Covers |
|---|---|---|
| [`architecture.md`](./architecture.md) | ✓ **filled** | Repo tree + domain + adapter + workflow inventory per phase. §2/3/4/9. |
| [`orchestration.md`](./orchestration.md) | ✓ **filled** (IngestWorkflow) | Workflow inventory + IngestWorkflow ledger (input/output/activities/reconcile/wire-up). §5. |
| [`temporal.md`](./temporal.md) | ✓ **filled** | Client + Worker adapter shape, registration flow, workflow conventions. §5, §9. |
| [`observability.md`](./observability.md) | ✓ **filled** | Four pillars status, vocabulary + Emitter + metrics + tracing stub. §11. |
| [`logging.md`](./logging.md) | ✓ **filled** | Emission reference — Emit call site, Field helpers, TestEmitter. §11. |
| [`deployment.md`](./deployment.md) | ✓ **filled** | Compose files + Garage bootstrap + Caddy + workflow scheduling state. §10. |
| [`testing.md`](./testing.md) | ✓ **filled** | Test tier ledger (~175 tests) + make targets. §12. |
| [`api-contract.md`](./api-contract.md) | ⊘ Phase F stub | Populated during Phase A. §8. |
| [`operations.md`](./operations.md) | ⊘ Phase F stub | Populated during Phase M/C bring-up + failure procedures. §10, §14. |

**Ledger discipline (since 2026-07-07 — MANDATORY):** every code
change that touches a topic updates its ledger doc in the same
commit. Divergences from `../rebuild-plan.md` land in
[`../decisions.md`](../decisions.md). Full working discipline —
including "read the plan §", "reference archive/ but improve, don't
port", "verify diff before push" — lives in
[`../../AGENTS.md § Working discipline`](../../AGENTS.md#working-discipline-mandatory-since-2026-07-07-retro).
Non-negotiable.

## Where the design lives right now

Filled ledgers are the source of truth for what shipped. For topics
without a shipped ledger yet (api-contract.md, operations.md), the
canonical design lives in [`../rebuild-plan.md`](../rebuild-plan.md):

| Stub | Design lives at (rebuild-plan.md sections) |
|------|-------------------------------------------|
| `api-contract.md` | §8 |
| `operations.md` | §10, §14 |

Once a stub gets populated, its rebuild-plan section becomes "historical
context" per §15.7 — the ledger is the source of truth going forward.

## Intake rules

Same as top-level `docs/README.md` — architectural decisions land in
[`../decisions.md`](../decisions.md), open work in
[`../todo.md`](../todo.md). Structural facts about the Go system land
here, in the relevant stub.
