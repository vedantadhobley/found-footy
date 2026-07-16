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
| [`run-flow.md`](./run-flow.md) | ✓ **filled** (2026-07-09) | Narrative walk-throughs of Ingest + Monitor cycles, concurrency inventory, state-transition diagrams, latency profile, and known gaps. Cross-reference doc that connects the ledgers into a coherent "how does one run happen" story. |
| [`python-functional-spec.md`](./python-functional-spec.md) | ✓ **filled** (2026-07-10) | Behavioral spec of the Python system — WHAT it does, not HOW. Data schema, per-workflow contracts, cross-workflow coordination, failure modes, edge cases, config reference. Use as the authoritative "does Python do X?" reference during Go implementation; complements rebuild-plan.md which describes the TARGET architecture. |
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

## proposals/ — pre-commit design drafts + cross-cutting audits

`proposals/` is where **design-first drafts** and **cross-cutting
audits** live, distinct from the ledger docs above. When picking up
the rebuild, read here BEFORE proposing new designs — the next phase
may already have an open proposal awaiting review.

| Doc | Kind | Status | Purpose |
|---|---|---|---|
| [`proposals/workflow-audit-2026-07-09.md`](./proposals/workflow-audit-2026-07-09.md) | Audit | ✓ **THE CURRENT PUNCH LIST** | Cross-referenced audit of shipped Go IngestWorkflow + MonitorWorkflow against Python + rebuild-plan. Severity-ranked (P0/P1/P2). Has a "What to do next" section. Read this FIRST when picking up the rebuild — don't re-derive an audit that already exists. |
| [`proposals/api-football-audit-2026-07-09.md`](./proposals/api-football-audit-2026-07-09.md) | Audit | ✓ filled | Vendor doc audit — endpoints, rate limits, casing quirks, per-family enum values. Backs the frozen `docs/api-football/` reference. |
| [`proposals/monitor.md`](./proposals/monitor.md) | Phase proposal | ⚠ **SUPERSEDED** | O2 design proposal from 2026-07-07. Phase O2 shipped 2026-07-08 with deviations. Kept for historical context only — read `orchestration.md` for actual shipped shape. |
| [`proposals/discovery.md`](./proposals/discovery.md) | Phase proposal | ✓ **SIGNED OFF 2026-07-16** | O3 design — DiscoveryWorkflow + NATS composer. Trigger transport landed as Temporal-direct + register-on-flip (not NATS-triggered); Q1-Q4 resolved. O3/a unblocked. |
| [`proposals/completion-contract.md`](./proposals/completion-contract.md) | Design | ✓ shipped | Fixture completion contract via pluggable per-event workflow checklist. Landed 2026-07-11 in commit 65942ed. Reference for `event_downstream_workflows` schema + `FixtureReadyToComplete` semantics. |
| [`proposals/video-dedup.md`](./proposals/video-dedup.md) | Phase proposal | ⏳ **AWAITING REVIEW** | O4/O5 design — video dedup pipeline redesign (URL → content hash → perceptual hash, batch → S3 corpus, with interleave optimization). Departs from Python's URL-as-identity design. Has 7 open questions on hash algo, sampling, threshold, backfill scope. Blocks V/a which itself waits on T (Twitter port) shipping first. |
| [`proposals/test-corpus.md`](./proposals/test-corpus.md) | Design | ✓ filled | Scenario test-corpus design + YAML shape. |

**Lifecycle:** an approved proposal → the code lands → the ledger
doc (architecture / orchestration / etc.) is updated in the same
commit → the proposal is marked SUPERSEDED or moved to
`proposals/superseded/` (TBD). Cross-cutting audits stay where they
are since they don't map to a single ledger.

## Intake rules

Same as top-level `docs/README.md` — architectural decisions land in
[`../decisions.md`](../decisions.md), open work in
[`../todo.md`](../todo.md). Structural facts about the Go system land
here, in the relevant stub. New design proposals for un-shipped
phases land in `proposals/`.
