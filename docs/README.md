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
| Active bugs and deferred project work | [`todo.md`](./todo.md) |
| Closed issue and release evidence | [`history/README.md`](./history/) |
| **Why** a choice was made | [`decisions/README.md`](./decisions/) — current index plus frozen archive |
| Target designs and point-in-time audit evidence | [`design/`](./design/) |
| The legacy **Python** system (retired at the 2026-08-15 cutover) | [`../archive/docs/`](../archive/docs/) |
| External API-Football behavior | [`api-football/`](./api-football/) — frozen vendor reference |

Precedence when sources disagree: **code > ledger** (if a ledger is
wrong, fix the ledger); **decision record > design doc** (a landed
decision supersedes an older target design).

## The three layers

- **`docs/` (here) — as-built.** What has actually shipped in the Go
  rebuild. The ledgers below.
- **`docs/design/` — design history and evidence.** Target architecture,
  proposals, and point-in-time audits. Index: [`design/README.md`](./design/README.md).
- **`archive/docs/` — legacy.** Frozen Python-era docs, next to the
  Python code they describe (`archive/`). Use them as behavior and rollback
  evidence; production runs the Go stack.

## As-built ledgers (this directory)

- [`architecture.md`](./architecture.md) — `internal/` + `cmd/` tree, per-package status
- [`api.md`](./api.md) — REST DTOs, playback redirects, NATS subjects, and consumer rules
- [`orchestration.md`](./orchestration.md) — workflows + activities as shipped
- [`observability.md`](./observability.md) — vocabulary + logging + metrics substrate
- [`logging.md`](./logging.md) — Emit reference (how to add a Module/Action)
- [`temporal.md`](./temporal.md) — Client/Worker adapter + registration flow
- [`twitter-service.md`](./twitter-service.md) — Playwright-Go scraping service: HTTP contract, state machine, cookie fleet model
- [`testing.md`](./testing.md) — test tiers and git test gates
- [`deployment.md`](./deployment.md) — compose files + Caddy + first-time bootstrap
- [`operations.md`](./operations.md) — environment lifecycle, production diagnostics, recovery boundaries, cookie re-auth, and rollout gates
- [`todo.md`](./todo.md) — canonical active issue register and deferred work
- [`decisions/`](./decisions/) — **the "why" authority**; individual new records plus the frozen pre-normalization archive

## Historical evidence

- [`history/`](./history/) — completed project roadmaps and delivery snapshots.
- [`history/issue-register-2026-08-17.md`](./history/issue-register-2026-08-17.md)
  — closed issue narratives and the completed documentation-normalization
  register from the 2026-08-17 release.
- [`history/issue-closures-2026-08-17.md`](./history/issue-closures-2026-08-17.md)
  — post-release production validation and closure evidence for FF-034 and
  FF-051.
- [`design/`](./design/) — target designs, proposals, and point-in-time audit
  evidence, classified by [`design/README.md`](./design/README.md).

## After-session checklist

Before wrapping a session that changed code:

- Does any ledger here now describe something untrue? Update it in the
  same change.
- Did a decision land (→ [`decisions/`](./decisions/)) or work get
  deferred (→ [`todo.md`](./todo.md))?
- Did you move or rename a doc? Grep for inbound references and fix them
  (broken links rot quietly).
