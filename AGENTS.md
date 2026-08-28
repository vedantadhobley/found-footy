# Found Footy — Agent Context

> **PROD IS LIVE. NEVER TAKE PROD DOWN OR MODIFY `found-footy-prod-*`
> WITHOUT EXPLICIT PER-ACTION USER APPROVAL.** Prod serves real users
> at vedanta.systems every match day. This rule OVERRIDES every other
> instruction in this file and every default agent behavior. It covers:
> `docker restart`, `docker stop`, `docker compose down`, `docker rm`,
> `docker compose up` (recreates containers), edits to prod compose
> files, edits to any prod-loaded `.env`, prod database mutations
> (dropping/altering tables, delete/update DML on prod
> Postgres), prod S3 (Garage) mutations, and any change to prod's Caddy
> config. Any action against a container, file, or endpoint whose name
> or path contains `-prod` (or, for shared infra whose
> data is prod's) is prod-touching and requires an explicit "yes, do X
> on prod" from the user before you take it. Ask; wait; then act. A
> single approval covers the single action described, not adjacent or
> follow-up prod actions. If in doubt whether an action touches prod,
> ask.

Real-time football goal video discovery and curation. Polls API-Football
every 30 s for live matches across the top-5 European leagues + MLS, plus
national-team fixtures during international windows, debounces detected
goals over 3 polls, then fires off a 15-attempt Twitter video search for
each one. Downloads candidate clips, AI-validates the broadcast clock
against the API's reported match minute (`gemma-4-12b` on `joi`),
perceptually deduplicates against the existing S3 corpus, and surfaces
the surviving clips through the `vedanta-systems` portal via SSE.

## Cross-cutting context

Workspace-wide rules, node topology, and cross-project decisions live in [`~/workspace/vedanta-dhobley/`](../../vedanta-dhobley/). Every agent session reads its global `AGENTS.md` automatically via symlinks (`~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.gemini/GEMINI.md`); this pointer exists so anyone browsing the repo sees the pattern.

- [`AGENTS.md`](../../vedanta-dhobley/AGENTS.md) — operating model, commit conventions, Docker-first policy, host-port scheme, `mem_limit` rules, tailnet FQDN rule, privacy preferences
- [`docs/topology.md`](../../vedanta-dhobley/docs/topology.md) — aerial view of nodes, services, routing, messaging, roadmap
- [`docs/decisions.md`](../../vedanta-dhobley/docs/decisions.md) — timestamped rationale for locked-in choices (top-of-file entries constrain this project: joi model swap to Gemma 4 12B, Prom-format /metrics standard, shared NATS Go client library)
- [`docs/plans/2026-08-15-cutover.md`](../../vedanta-dhobley/docs/plans/2026-08-15-cutover.md) — this project is the anchor deliverable of the Aug 15 cutover

**Where things belong:** if a decision in this project turns out to be cross-project, raise it in dhobley — do not duplicate it here.

This file is your front door. Which section applies depends on why you
landed here:

- **Working on the Go system** (what prod runs now) → read [§ Go system](#go-system-current-active-work)
  and [`docs/README.md`](./docs/README.md), then follow its authority map to the
  focused as-built ledger for the subsystem.
- **Consulting the legacy Python reference** (archived at cutover; the
  pre-cutover system-of-record) → skip to [§ Python legacy stack](#python-legacy-stack-archived).

**Current branch context**: `rebuild/go` is the active branch AND what
prod runs. The Python→Go cutover happened **2026-08-15** (see
[`docs/decisions/README.md`](./docs/decisions/)); prod containers build from
this branch's `Dockerfile`. `main` still holds the Python codebase as the
preserved rollback (its data volumes are kept, not wiped). A merge of
`rebuild/go` → `main` is deliberately held until the Go stack proves a
clean match-day.

## Go system (current active work)

Full ground-up rewrite from Python to Go 1.25, **in prod since the
2026-08-15 cutover**. Current authority is code, the focused as-built ledgers,
the decision log, and the issue register. The original
[`rebuild plan`](./docs/design/rebuild-plan.md) is historical target evidence;
large parts of its topology, schema, naming, deployment, and cutover design were
superseded before production.

The phased-delivery table that used to live here drifted badly (it was
frozen at 2026-07-07), so this file no longer tracks phase status. Use
[`docs/todo.md`](./docs/todo.md) for active bugs and deferred work. The
as-built ledgers below describe what actually shipped per package and
workflow. Point-in-time audits preserve evidence; they are not live task
lists.

**Where to look for Go rebuild work:**

- [`docs/todo.md`](./docs/todo.md) is the canonical issue register. The latest full audit is the independent [`docs/design/audits/audit-2026-08-17-codex.md`](./docs/design/audits/audit-2026-08-17-codex.md); earlier audits remain evidence snapshots. Validate an old finding against current code before scheduling it, then give accepted work a stable ID in `docs/todo.md`.
- [`docs/design/README.md`](./docs/design/README.md) classifies the rebuild plan,
  proposals, and audits by current disposition. They preserve intent and
  evidence; never implement one without verifying it against code, decisions,
  the relevant as-built ledger, and `docs/todo.md`.
- [`docs/design/rebuild-plan.md`](./docs/design/rebuild-plan.md) — historical
  target architecture (§1–§16). Consult it when original rationale matters;
  do not use it as the current implementation contract.
- [`docs/design/python-functional-spec/`](./docs/design/python-functional-spec/) — behavioral spec of the retired Python system — WHAT it did, not HOW. Use it and `archive/` as parity evidence, not as the current architecture.
- [`docs/design/README.md`](./docs/design/README.md) — routing index for per-topic rebuild docs.
- [`docs/architecture.md`](./docs/architecture.md) — **as-shipped ledger** of internal/ + cmd/ tree with per-package status.
- [`docs/orchestration/`](./docs/orchestration/) — **as-shipped ledger** of workflows + activities.
- [`docs/observability.md`](./docs/observability.md) — **as-shipped ledger** of vocabulary + logging + metrics substrate.
- [`docs/logging.md`](./docs/logging.md) — **emission reference** — how to call Emit + add new (Module, Action).
- [`docs/temporal.md`](./docs/temporal.md) — **as-shipped ledger** of Client/Worker adapter + registration flow.
- [`docs/testing.md`](./docs/testing.md) — **as-shipped ledger** of the test tiers (unit + integration + scenarios).
- [`docs/deployment.md`](./docs/deployment.md) — compose files + Caddy + first-time bootstrap steps.
- [`docs/decisions/README.md`](./docs/decisions/) — current architectural-decision index and frozen pre-normalization archive.
- [`internal/observability/vocabulary/vocabulary.go`](./internal/observability/vocabulary/vocabulary.go) — typed enum registry (Module, Action). Every log emission uses these. Adding a new Module or Action = one const declaration.
- [`internal/infra/pg/`](./internal/infra/pg/) — the **template** all future adapters follow: `Instruments` bundle + `RegisterMetrics` constructor + framework-native tracer + prometheus.Collector for scrape-time stats.

## Working discipline (mandatory, since 2026-07-07 retro)

Learned the hard way — early phases shipped without living-doc updates and the
plan, code, and ledgers diverged silently. The retro caught the damage; this
section prevents recurrence without treating the now-historical target plan as
current truth.

**Before writing code** for any workflow, activity, adapter, or
domain change:

1. **Read the current authority.** Start at `docs/README.md`, then read the
   accepted issue, relevant as-built ledger, decisions, and current code. Verify
   live state when the fact can drift.
2. **Consult history when it answers a real question.** Use the rebuild plan for
   original intent and `archive/` Python for prior behavior, never as templates
   or automatic requirements. Re-verify old audit claims before scheduling
   them.
3. **Surface material design changes before coding.** If the requested fix
   requires changing a current contract or recorded decision, propose the new
   invariant and its tradeoffs first. Do not create a silent convention from a
   historical plan or implementation accident.

**When shipping the change:**

4. **Update `docs/<topic>.md` in the SAME commit.** The
   as-built ledger docs (architecture, orchestration, observability,
   temporal, testing, etc.) get updated with what shipped in the same commit
   that ships the code. A code-only commit is treated as incomplete —
   same status as missing tests.
5. **Record durable decisions.** If a change alters a current contract or
   supersedes a recorded decision, add an individual record under
   `docs/decisions/` and route it from that directory's README.
   Reference historical plan material only when it materially explains the
   choice.
6. **Verify `git diff --stat --cached` matches the commit message
   before push.** Write/Edit tool failures produce silent no-ops.
   If the commit message says "filled observability.md" but the diff
   doesn't show observability.md, fix it before push. The retro's
   own commits made this exact mistake twice — verifying the diff
   catches it.

**Quality bar:**

7. **Enterprise-grade > Python-shape.** Python was expedient. The
   rewrite is what production should be. Concrete implications:
     - **Concurrency where safe + beneficial.** `workflow.Go` for
       per-fixture parallelism in ActivePollWorkflow. Goroutines with
       proper error aggregation for pipeline stages. Don't Python-port
       sequential loops when independent work is available.
     - **Typed everything.** Vocabulary enums for logs. Typed errors
       (per-adapter) that workflows can `errors.Is` against. No
       stringly-typed dispatch except where Temporal SDK requires it.
     - **Invariants enforced at the schema layer.** CHECK constraints,
       FOREIGN KEY RESTRICT/CASCADE, UNIQUE, NOT NULL. Domain code
       enforces the same invariants + generates friendlier errors, but
       the DB is the ultimate arbiter.
     - **Idempotency by design.** Ingest UPSERTs, Schedule creation
       swallows AlreadyRunning, event_monitor_workflows uses
       ON CONFLICT DO NOTHING. Every write op should be safe to retry.
     - **No half-finished implementations.** If you can't ship the
       full thing, don't ship the stub — it lies. See
       `internal/errors/` rip in decisions.md.

**When in doubt, ask.** The user would rather answer a question
mid-flow than review a commit that violated their intent.

See the [2026-07-07 doc retro closure](./docs/decisions.md#2026-07-07--doc-retro-closure) for the
history that produced these rules.



**`.env` / `.env.example` convention (load-bearing):** Docker Compose's
`env_file` parser does **not** strip inline `#` comments — everything
after `=` becomes the value, whitespace and all. Put descriptive
comments on their **own line above** the assignment:

```
# LLM_CHAT_MODEL empty = discover from /v1/models
LLM_CHAT_MODEL=
```

Never `KEY=value  # comment`. Same discipline for any dotenv file used
with `env_file`.

**Go rebuild conventions:**

- Go 1.25 (bumped from 1.23 for air compatibility). Toolchain via container — nothing installed on host.
- Everything runs in Docker. `make build`, `make test`, `make test-short`, and the check targets all spin pinned `golang:1.25.11-bookworm` throwaway containers with the source bind-mounted.
- Dev stack: `docker compose -f docker-compose.dev.yml up -d` — air hot-reload on the three Go binaries (worker/api/twitter).
- Prod stack: `docker compose -f docker-compose.prod.yml` — **runs the Go stack** (worker/api/twitter on Postgres + Garage) since the 2026-08-15 cutover. Files intentionally renamed from bare `docker-compose.yml` so a stray `docker compose <cmd>` from this directory refuses to resolve.
- Tests: unit + integration in the same package; integration tests spin real Postgres via testcontainers-go (`/var/run/docker.sock` mounted + `--network=host` in the `test` make target). Skip with `-short`.

**Things to check before doing X (Go rebuild):**

- **Replicating the pg adapter pattern for a new adapter (S3-S7)**: `internal/infra/pg/` is the template. `Instruments` struct + `RegisterMetrics(reg, log)` constructor + framework-native tracer (not method wrapping) + `prometheus.Collector` for scrape-time gauges + testcontainers-go integration test. Match the shape.
- **Adding a new Module or Action**: declare the const in [`vocabulary.go`](./internal/observability/vocabulary/vocabulary.go) (module) or the appropriate `actions_<family>.go` (action). Register via `registerActions(...)`. Runtime check in `slogEmitter.Emit` catches strays that slip through the compile-time enum.
- **Adding a new adapter with cleanup**: use `deps.RegisterCloser("name", closeFn)` at construction, not `defer`. Bootstrap drains closers in reverse-registration order (LIFO), which is what Temporal needs (worker drains before its downstream deps close).
- **Adding a new config surface**: put it on its own `config.<Adapter>Config` struct in `internal/config/<adapter>.go`, then reference it from the top-level `Config` in `config.go`. Envconfig via `caarlos0/env/v11`.
- **Touching the pg schema**: update the fresh-install snapshot at [`internal/infra/pg/schema.sql`](./internal/infra/pg/schema.sql) and add one immutable ordered SQL file under [`migrations/`](./migrations/). Its first line is `-- schema-hash: <sha256 of schema.sql>`. Migrations must be transaction-safe: no `BEGIN`/`COMMIT` and no concurrent indexes. Never edit a migration once a durable environment has recorded it in `schema_migrations`. Dev Compose runs the one-shot migration gate before worker/API; production uses separately approved `make migrate-prod` before `make deploy-prod`. Run `make test` for every schema change.
- **Ports**: metrics/healthz on `:8080` per binary. The api read surface is on `:8081` (routes under `/api/v1`). The twitter service exposes `:8888` (API) + `:6080` (VNC, opt-in via the `vnc` compose profile).

## Python legacy stack (archived)

**Until the 2026-08-15 cutover, prod ran this Python codebase.** The Go
cutover retired it — prod no longer runs Python. The code stays in the
tree (under `archive/`), and `main`'s data volumes are preserved, as the
**rollback path** if the Go stack has to be reverted.

Do NOT delete or refactor the Python code from this branch — it's the
rollback. Its docs below remain the reference for how the pre-cutover
system behaved (Grafana/Loki queries, edge cases, the RAG pipeline).

**Python stack summary (as it ran pre-cutover — historical):**

- **Workers**: Python 3.10 (`python:3.10-slim`), Temporal Python SDK, asyncio. 6 workflows, 42 activities. `docker-compose.prod.yml`.
- **Application database**: MongoDB 7, 5-collection design — `fixtures_staging` / `fixtures_live` / `fixtures_active` / `fixtures_completed` / `team_aliases`. The Go rebuild moves to Postgres per [`docs/design/rebuild-plan.md`](./docs/design/rebuild-plan.md) §3.
- **Object storage**: MinIO. The Go rebuild moves to Garage per rebuild-plan.md decisions.md 2026-07-01.
- **Twitter scraping**: Firefox + Selenium in `twitter/`. Cookies persisted; shared between dev + prod (same account).
- **External LLM**: Qwen3-VL-8B via llama.cpp on `joi` at `http://llama-small.joi`.
- **Auto-scaler**: Python sidecar watching Temporal queue depth + MongoDB active-goal count.

**Where to look for Python legacy context:**

- [`README.md`](./README.md) — public-facing project description (Python-oriented)
- [`archive/docs/architecture.md`](./archive/docs/architecture.md) — 5-collection MongoDB design + workflow hierarchy
- [`archive/docs/orchestration.md`](./archive/docs/orchestration.md) — event lifecycle state machine, debouncing
- [`archive/docs/temporal.md`](./archive/docs/temporal.md) — per-activity timeouts, retries, heartbeats
- [`archive/docs/logging.md`](./archive/docs/logging.md) — Python `footy_logging` reference (Grafana/Loki queries still apply)
- [`archive/docs/rag.md`](./archive/docs/rag.md) — Wikidata + LLM team-alias pipeline
- [`archive/docs/twitter-auth.md`](./archive/docs/twitter-auth.md) — browser automation, cookie lifecycle, VNC re-auth
- [`archive/docs/operations.md`](./archive/docs/operations.md) — Python-era runbook
- [`docs/decisions/README.md`](./docs/decisions/) — architectural decisions index; the pre-normalization log remains a frozen archive.
- [`archive/docs/todo.md`](./archive/docs/todo.md) — frozen Python-era work and bug history; re-verify any relevant behavior against the Go system before intake
- [`archive/docs/roadmap.md`](./archive/docs/roadmap.md), [`archive/docs/sprints.md`](./archive/docs/sprints.md) — historical Python-era delivery plans; no longer production status
- [`archive/docs/proposals/`](./archive/docs/proposals/) — Python-era feature designs
- [`archive/deploy/INFRA-NOTES.md`](./archive/deploy/INFRA-NOTES.md) — Caddyfile entries + cross-project network setup

## Documentation and docstrings (both eras)

Three layers of persistence, each for a different knowledge type:

1. **`docs/`** — project-wide knowledge in three layers: **`docs/`**
   itself is the **as-built** ledger for the Go system (what shipped);
   **[`docs/design/`](./docs/design/)** is the **target** design
   (rebuild-plan, proposals, audits); **[`archive/docs/`](./archive/docs/)**
   is the frozen **legacy Python** knowledge, next to the Python code in
   `archive/`. Start at [`docs/README.md`](./docs/README.md) — the routing
   index + authority map (which source of truth answers which question).
2. **Code-level docstrings + per-file header comments** — every file
   gets a top-of-file comment describing what it is; every
   function/method/class gets a docstring. Doc-heavy by design.
   Applies to Python, Go, and every hand-authored config/infra file
   (Dockerfiles, Makefiles, compose YAMLs, SQL, Caddyfiles, dotfiles,
   etc.). Explain WHY, not WHAT.

   - **Go**: every `.go` file (including `_test.go`) gets at least a
     one-line comment above `package X` describing what THAT file
     contributes to the package. Overrides Go's convention of "package
     doc lives on one file"; godoc concatenates per-file comments so
     nothing breaks. Longer multi-paragraph package context still lives
     on the primary entry-point file of each package.
   - **Config / infra files**: first-line comment (using whatever
     syntax the file supports — `#`, `--`, `//`, YAML `#`, etc.)
     describing purpose. Section-header comments deeper in the file
     don't count.
   - **Markdown docs**: the `# H1` title serves as the header — no
     separate top-of-file comment needed.
   - **Generated files** (go.sum, .terraform.lock.hcl, etc.) and
     rendered artifacts don't need headers.
   - **Never edit-forbidden files** (secrets, .env with real values):
     don't add headers to values-containing files that live outside
     the repo anyway.
3. **Per-agent auto-memory** — `~/.claude/projects/<project>/memory/`
   and analogous paths for other agents. Reserved for user preferences
   and collaboration tone. Project facts do NOT go here — they go in
   `docs/` (per global rule in `~/.claude/CLAUDE.md`).

**Cross-doc linking**: markdown `[text](./path.md)` syntax. No
`[[wiki-link]]` style.

## Reference material

- llama.cpp on joi: live model IDs via `curl http://joi.luv/v1/models` (current vision model: `gemma-4-12b`)
- API-Football: <https://www.api-football.com/documentation-v3> (key in `.env`)
- Temporal Python SDK: <https://docs.temporal.io/develop/python>
- Temporal Go SDK: <https://docs.temporal.io/develop/go>
- Wikidata SPARQL: <https://query.wikidata.org/>
- pgx v5 docs: <https://pkg.go.dev/github.com/jackc/pgx/v5>
- testcontainers-go: <https://golang.testcontainers.org/>

## Load-bearing operational rules

- **Prod is untouchable without explicit per-action user approval.** See the top-of-file callout. It is the single most important rule in this repo and overrides every other instruction.
- **Do NOT `ssh vedanta@joi` from this repo's tooling.** joi is a separate node. HTTP queries over the tailnet are fine; shell commands need explicit user approval.
- **LLM concurrency cap**: the vision model is `gemma-4-12b` on `joi.luv` (`LLM_CHAT_CONCURRENCY_CAP=4`); throughput drops sharply past the cap. Any code that fans out LLM calls needs a semaphore or gateway. (nexus is the planned future inference target — WIP, hardware-bound — not yet live.)

---

> **Auto-memory note** — per `~/.claude/CLAUDE.md`, project facts belong here or in `docs/`, **not** in `~/.claude/projects/<project>/memory/`. Keep that auto-memory directory essentially empty.
