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
| The retired **Python** system | [Historical source and retrieval](./decisions/2026-10-21-retire-python-working-tree.md) and [functional specification](./design/python-functional-spec/) |
| External API-Football behavior | [`api-football/`](./api-football/) — frozen vendor reference |

Precedence when sources disagree: **code > ledger** (if a ledger is
wrong, fix the ledger); **decision record > design doc** (a landed
decision supersedes an older target design).

## Current and historical documentation

- **`docs/` (here) — as-built.** What has actually shipped in the Go
  rebuild. The ledgers below.
- **`docs/design/` — design history and evidence.** Target architecture,
  proposals, and point-in-time audits. Index: [`design/README.md`](./design/README.md).
- **Retired Python source and docs — Git history.** The old `archive/` tree
  is preserved at revision `bfcbc42`; use the retrieval guide above. The frozen
  behavioral specification remains under `docs/design/`. Neither is a supported
  operational rollback; production runs the Go stack.

## As-built ledgers (this directory)

- [`architecture.md`](./architecture.md) — `internal/` + `cmd/` tree, per-package status
- [`api.md`](./api.md) — REST DTOs, playback redirects, NATS subjects, and consumer rules
- [`orchestration/`](./orchestration/) — workflows + activities as shipped,
  routed by workflow family
- [`observability.md`](./observability.md) — vocabulary + logging + metrics substrate
- [Provider health and guarded writes](./provider-health.md) — durable polling evidence, cause-specific recovery, atomic provider mutation policy, and retry receipts
- [`logging.md`](./logging.md) — Emit reference (how to add a Module/Action)
- [`temporal.md`](./temporal.md) — Client/Worker adapter + registration flow
- [`twitter-service.md`](./twitter-service.md) — Playwright search, raw-Firefox login, maintenance, and shared-cookie fleet contract
- [`testing.md`](./testing.md) — test tiers and git test gates
- [`deployment.md`](./deployment.md) — compose files + Caddy + first-time bootstrap
- [Operator video-evidence UI](../operator-ui/README.md) — removable, read-only
  companion module for retained MD5 review; not part of the public application
- [`operations.md`](./operations.md) — environment lifecycle, production diagnostics, recovery boundaries, cookie re-auth, and rollout gates
- [Garage recovery](./operations/garage-recovery.md) — metadata diagnosis,
  isolated reconstruction, stopped capture, and storage cutover safety
- [`todo.md`](./todo.md) — canonical active issue register and deferred work
- [`decisions/`](./decisions/) — **the "why" authority**; individual new records plus the frozen pre-normalization archive

## Historical evidence

- [Attribution rewrite](./decisions/2026-10-21-remove-ai-coauthor-trailers.md)
  — the approved metadata correction, revision map and private-backup recovery
  boundary; historical production release IDs remain unchanged.
- [`history/`](./history/) — completed project roadmaps and delivery snapshots.
- [October restart recovery](./history/restart-recovery-2026-10-05.md) — closed
  FF-103 inference routing and FF-104 Garage recovery with exact validation.
- [API-Football stalled fixture — 2026-09-16](./incidents/2026-09-16-api-football-stalled-fixture.md)
  — Barcelona's frozen HTTP-200 feed while other fixtures progressed; FF-075
  freshness, scope, recovery, and mutation-protection design gaps.
- [`incidents/2026-08-20-twitter-feed-suppression.md`](./incidents/2026-08-20-twitter-feed-suppression.md)
  — live MLS evidence for FF-061's synchronized Twitter feed loss, retained
  resource correlation, and post-release 429 validation.
- [`history/issue-register-2026-08-17.md`](./history/issue-register-2026-08-17.md)
  — closed issue narratives and the completed documentation-normalization
  register from the 2026-08-17 release.
- [`history/issue-closures-2026-08-17.md`](./history/issue-closures-2026-08-17.md)
  — post-release validation and closure evidence for completed issues.
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
