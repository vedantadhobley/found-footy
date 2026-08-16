# docs/design/ — the to-build layer

Target architecture, design proposals, and audits for work **not yet
shipped** in the Go rebuild. For **what's already built**, see the
as-built ledgers in [`../`](../) (start at [`../README.md`](../README.md),
the routing index + authority map). For the **legacy Python** system,
see [`../../archive/docs/`](../../archive/docs/).

## Contents

- [`rebuild-plan.md`](./rebuild-plan.md) — the design bible for the TARGET
  architecture (§1–§16: architecture, schema, adapters, workflows,
  deployment, migration). Large; consult the relevant § before touching
  code — don't read end-to-end.
- [`design-audit.md`](./design-audit.md) — the 17-section analysis that
  informed the rebuild plan (the "why" behind it).
- [`python-functional-spec.md`](./python-functional-spec.md) — behavioral
  spec of the CURRENT Python system (WHAT it does, not HOW). The build
  reference; retires to `archive/` at cutover.
- [`design-improvements-2026-07-23.md`](./design-improvements-2026-07-23.md)
  — improvement backlog from the Miami smoke test + audit.
- [`audit-2026-07-26.md`](./audit-2026-07-26.md) — point-in-time audit of
  shipped Go vs the plan.
- [`audit-2026-08-05.md`](./audit-2026-08-05.md) — as-built audit: shipped Go
  LOGIC vs Python LOGIC (code-to-code) + doc accuracy across all ten
  subsystems. 63 improvements / 30 gaps; 24 high-severity doc inaccuracies +
  a 4-phase doc-restructure plan (since executed).
- [`audit-2026-08-15.md`](./audit-2026-08-15.md) — **pre-MLS-reset audit** (cutover
  evening): 7 code dimensions + a doc-staleness review. The cookie write-back +
  LLM-cap fixes and the live follow-up list live here. The latest audit.
- [`api-contract.md`](./api-contract.md) — public API surface contract
  (Phase A, unbuilt).
- [`frontend-bridge-handoff.md`](./frontend-bridge-handoff.md) — **cross-project
  handoff** to the vedanta-systems frontend/BFF: how to consume the live feed —
  the 3 NATS subjects + envelope, the 2 REST data endpoints + video redirect,
  the DTO shapes, and the refetch-on-connect model. Self-contained; the producer
  side is shipped (N1–N8).
- [`proposals/`](./proposals/) — design-first drafts per phase. Signed off:
  `discovery.md`, `twitter-port.md`,
  [`twitter-scaling.md`](./proposals/twitter-scaling.md) (per-event Firefox
  instance model — one browser per event, event-driven; **supersedes**
  twitter-port.md's T/d–T/e pool + router + scaler), `video-dedup.md` (**historical** —
  topology + schema largely diverged from the as-built; see its banner +
  [`../orchestration.md`](../orchestration.md)), `team-aliases.md`,
  `completion-contract.md`, `vision.md` (multi-frame VL soccer/screen +
  period-aware clock verification; *model behaviour* pending a gemma-on-nexus
  bake-off). `monitor.md` is SUPERSEDED (historical only).
  `workflow-audit-2026-07-09.md` + `api-football-audit-2026-07-09.md` are
  cross-cutting audits, not phase proposals.

## Convention

A design doc describes **intent**. When the thing ships, the as-built
truth moves to a `../` ledger + an entry in [`../decisions.md`](../decisions.md);
the design doc stays as the historical rationale — mark superseded
sections rather than deleting them (see `video-dedup.md`'s banner for the
pattern).
