# found-footy docs — routing index + authority map

**This is the entry point.** Use it to reach the right doc in ≤1 hop
instead of grepping. If you're an agent about to state how something
works: read the authoritative source first (see the map below), don't
reconstruct from memory.

## Authority map — where truth lives

| Question | Source of truth |
|---|---|
| Data model — tables, columns, constraints | [`../internal/infra/pg/schema.sql`](../internal/infra/pg/schema.sql) |
| How the system behaves **as built today** | the as-built ledgers in this directory (below) |
| **Why** a choice was made | [`decisions.md`](./decisions.md) — append-only, newest first |
| **Target** design / what's planned but unbuilt | [`design/`](./design/) |
| The legacy **Python** system (still in prod until cutover) | [`../archive/docs/`](../archive/docs/) |
| External API-Football behavior | [`api-football/`](./api-football/) — frozen vendor reference |

Precedence when sources disagree: **code > ledger** (if a ledger is
wrong, fix the ledger); **`decisions.md` > design doc** (a landed
decision supersedes an older target design).

## The three layers

- **`docs/` (here) — as-built.** What has actually shipped in the Go
  rebuild. The ledgers below.
- **`docs/design/` — to-build.** Target architecture, proposals, and
  audits for work not yet shipped. Index: [`design/README.md`](./design/README.md).
- **`archive/docs/` — legacy.** Frozen Python-era docs, next to the
  Python code they describe (`archive/`). Still the system-of-record
  for prod until the cutover.

## As-built ledgers (this directory)

- [`architecture.md`](./architecture.md) — `internal/` + `cmd/` tree, per-package status
- [`orchestration.md`](./orchestration.md) — workflows + activities as shipped
- [`observability.md`](./observability.md) — vocabulary + logging + metrics substrate
- [`logging.md`](./logging.md) — Emit reference (how to add a Module/Action)
- [`temporal.md`](./temporal.md) — Client/Worker adapter + registration flow
- [`testing.md`](./testing.md) — test tiers (~200 tests + 16 scenarios) + the git test gates
- [`run-flow.md`](./run-flow.md) — narrative walkthrough of shipped Ingest/Monitor cycles
- [`deployment.md`](./deployment.md) — compose files + Caddy + first-time bootstrap
- [`roadmap.md`](./roadmap.md) — roadmap to La Liga (2026-08-15)
- [`decisions.md`](./decisions.md) — **the "why" authority**, append-only (both eras)

## After-session checklist

Before wrapping a session that changed code:
- Does any ledger here now describe something untrue? Update it in the
  same change.
- Did a decision land (→ [`decisions.md`](./decisions.md)) or work get
  deferred (→ [`design/`](./design/))?
- Did you move or rename a doc? Grep for inbound references and fix them
  (broken links rot quietly).
