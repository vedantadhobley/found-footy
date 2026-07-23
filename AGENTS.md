# Found Footy — Agent Context

> **PROD IS LIVE. NEVER TAKE PROD DOWN OR MODIFY `found-footy-prod-*`
> WITHOUT EXPLICIT PER-ACTION USER APPROVAL.** Prod serves real users
> at vedanta.systems every match day. This rule OVERRIDES every other
> instruction in this file and every default agent behavior. It covers:
> `docker restart`, `docker stop`, `docker compose down`, `docker rm`,
> `docker compose up` (recreates containers), edits to prod compose
> files, edits to any prod-loaded `.env`, prod database mutations
> (dropping/altering tables, delete/update DML on prod Mongo or
> Postgres), prod S3 (MinIO) mutations, and any change to prod's Caddy
> config. Any action against a container, file, or endpoint whose name
> or path contains `-prod` (or, for shared infra like `mongo`, whose
> data is prod's) is prod-touching and requires an explicit "yes, do X
> on prod" from the user before you take it. Ask; wait; then act. A
> single approval covers the single action described, not adjacent or
> follow-up prod actions. If in doubt whether an action touches prod,
> ask.

Real-time football goal video discovery and curation. Polls API-Football
every 30 s for live matches across the top-5 European leagues + ~15 FIFA
national teams, debounces detected goals over 3 polls, then fires off a
10-attempt Twitter video search for each one. Downloads candidate clips,
AI-validates the broadcast clock against the API's reported match minute
(Qwen3-VL-8B on `joi`), perceptually deduplicates against the existing
S3 corpus, and surfaces the surviving clips through the `vedanta-systems`
portal via SSE.

## Cross-cutting context

Workspace-wide rules, node topology, and cross-project decisions live in [`~/workspace/vedanta-dhobley/`](../../vedanta-dhobley/). Every agent session reads its global `AGENTS.md` automatically via symlinks (`~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.gemini/GEMINI.md`); this pointer exists so anyone browsing the repo sees the pattern.

- [`AGENTS.md`](../../vedanta-dhobley/AGENTS.md) — operating model, commit conventions, Docker-first policy, host-port scheme, `mem_limit` rules, tailnet FQDN rule, privacy preferences
- [`docs/topology.md`](../../vedanta-dhobley/docs/topology.md) — aerial view of nodes, services, routing, messaging, roadmap
- [`docs/decisions.md`](../../vedanta-dhobley/docs/decisions.md) — timestamped rationale for locked-in choices (top-of-file entries constrain this project: joi model swap to Gemma 4 12B, Prom-format /metrics standard, shared NATS Go client library)
- [`docs/plans/2026-08-15-cutover.md`](../../vedanta-dhobley/docs/plans/2026-08-15-cutover.md) — this project is the anchor deliverable of the Aug 15 cutover

**Where things belong:** if a decision in this project turns out to be cross-project, raise it in dhobley — do not duplicate it here.

This file is your front door. Which section applies depends on why you
landed here:

- **Working on the Go rebuild** (current active development on
  `rebuild/go` branch) → read [§ Go rebuild](#go-rebuild-current-active-work) first,
  then [`docs/rebuild-plan.md`](./docs/rebuild-plan.md) is the design bible.
- **Working on legacy Python prod** (still live at vedanta.systems,
  every match day) → skip to [§ Python legacy stack](#python-legacy-stack-still-running-in-prod).

**Current branch context**: `rebuild/go` is the active work branch. `main`
still runs the Python codebase; prod containers are built from that.
The two coexist — no cutover has happened yet.

## Go rebuild (current active work)

Full ground-up rewrite from Python to Go 1.25, tracked in
[`docs/rebuild-plan.md`](./docs/rebuild-plan.md) as the design bible.
Phased delivery — see §16 for the phase map. Where we are (2026-07-07):

| Phase | Status | What it covers |
|---|---|---|
| F | ✅ shipped | Scaffold: `cmd/`, `internal/`, docker-compose, air, Caddy stubs, Makefile |
| S1 | ✅ shipped | Observability substrate: `config`, `logging` (slog + typed Field), `vocabulary` (typed Module/Action enums), `metrics` (Prometheus), `bootstrap` (shared binary startup with Closer registry) |
| S2 | ✅ shipped | Postgres adapter: `pool.go` (pgxpool wrapper), `instruments.go` (query tracer + pool-stats collector), full §3 schema in `schema.sql`, wired into `cmd/worker` + `cmd/api` |
| S3 | ✅ shipped | NATS adapter (workspace NATS bus for events + SSE fan-out + JetStream webhook delivery) |
| S4 | ✅ shipped | Garage / S3 adapter (aws-sdk-go-v2 client wrapper + tests) |
| S5 | ✅ shipped | Temporal adapter (Client + Worker wrappers with Instruments) |
| S6 | ✅ shipped | LLM adapter (OpenAI-compatible client + typed errors + Chat) |
| S7 | ✅ shipped | External HTTP adapters (apifootball, twitter, syndication, wikidata) |
| D | ✅ shipped | Domain layer — 4 of 8 packages complete (fixture, event, video, alias); discovery/vision/session/textanalysis stubbed for build-when-needed |
| O1 | ✅ shipped | IngestWorkflow + 4 activities + wire-up + live e2e verification + daily 00:05 UTC Temporal Schedule (O1e complete) |
| O2 | ⏳ next | MonitorWorkflow — 30s cycle, staging-poll 15-min amortization, event debounce (see decisions.md 2026-07-07 staging-poll entry) |
| O3–O5 | 📅 planned | Discovery, VideoValidation, AssetPersistence workflows |
| V, A, T, M, C | 📅 planned | Video pipeline, API surface, testing (synthetic e2e), migration, cutover |

**Where to look for Go rebuild work:**

- [`docs/rebuild/proposals/workflow-audit-2026-07-09.md`](./docs/rebuild/proposals/workflow-audit-2026-07-09.md) — **THE CURRENT PUNCH LIST.** Cross-referenced audit of shipped Go vs Python + rebuild-plan, with severity buckets (P0/P1/P2), "What to do next" section, aggregate counts. Read this FIRST when picking up the rebuild. Don't re-derive an audit that already exists.
- [`docs/rebuild/proposals/`](./docs/rebuild/proposals/) — **design-first drafts for phases before they're committed.** Look here before proposing designs — all current phase proposals SIGNED OFF: `discovery.md` (O3/a unblocked, 2026-07-16), `twitter-port.md` (T/a unblocked after O3/a-c ship, 2026-07-16), `video-dedup.md` (V/a unblocked after T ships, 2026-07-16), `team-aliases.md` (blocks team-alias domain package, 2026-07-19). `monitor.md` SUPERSEDED — historical only. `workflow-audit-2026-07-09.md` and `api-football-audit-2026-07-09.md` are cross-cutting audits, not phase proposals.
- [`docs/rebuild-plan.md`](./docs/rebuild-plan.md) — **the design bible for the TARGET architecture**. §1-§16 covers architecture, schema, adapters, workflows, deployment, migration. Read the section relevant to what you're touching before starting.
- [`docs/rebuild/python-functional-spec.md`](./docs/rebuild/python-functional-spec.md) — **behavioral spec of the CURRENT Python system** — WHAT it does, not HOW. Data schema, per-workflow contracts, cross-workflow coordination, failure modes, edge cases, config reference. Authoritative "does Python do X?" reference during Go implementation.
- [`docs/rebuild/run-flow.md`](./docs/rebuild/run-flow.md) — narrative walkthrough of shipped Ingest + Monitor cycles with inline `[GAP]` markers. Reads as connective tissue between the ledgers.
- [`docs/rebuild/README.md`](./docs/rebuild/README.md) — routing index for per-topic rebuild docs.
- [`docs/rebuild/architecture.md`](./docs/rebuild/architecture.md) — **as-shipped ledger** of internal/ + cmd/ tree with per-package status.
- [`docs/rebuild/orchestration.md`](./docs/rebuild/orchestration.md) — **as-shipped ledger** of workflows + activities (IngestWorkflow complete).
- [`docs/rebuild/observability.md`](./docs/rebuild/observability.md) — **as-shipped ledger** of vocabulary + logging + metrics substrate.
- [`docs/rebuild/logging.md`](./docs/rebuild/logging.md) — **emission reference** — how to call Emit + add new (Module, Action).
- [`docs/rebuild/temporal.md`](./docs/rebuild/temporal.md) — **as-shipped ledger** of Client/Worker adapter + registration flow.
- [`docs/rebuild/testing.md`](./docs/rebuild/testing.md) — **as-shipped ledger** of ~175 tests across the tiers.
- [`docs/rebuild/deployment.md`](./docs/rebuild/deployment.md) — compose files + Caddy + first-time bootstrap steps.
- [`docs/decisions.md`](./docs/decisions.md) — append-only architectural decisions, including divergences from `rebuild-plan.md`.
- [`internal/observability/vocabulary/vocabulary.go`](./internal/observability/vocabulary/vocabulary.go) — typed enum registry (Module, Action). Every log emission uses these. Adding a new Module or Action = one const declaration.
- [`internal/infra/pg/`](./internal/infra/pg/) — the **template** all future adapters follow: `Instruments` bundle + `RegisterMetrics` constructor + framework-native tracer + prometheus.Collector for scrape-time stats.

## Working discipline (mandatory, since 2026-07-07 retro)

Learned the hard way — Phases S1–O1d shipped without living-doc
updates, and IngestWorkflow drifted from `docs/rebuild-plan.md` §5 W1
in six places before anyone noticed. The retro caught the damage;
this section prevents recurrence.

**Before writing code** for any workflow, activity, adapter, or
domain change:

1. **Read the plan §.** `docs/rebuild-plan.md` is the design bible.
   Find the section relevant to what you're about to touch (§2 tree,
   §3 schema, §4 domain, §5 orchestration, §9 adapters, §11 obs,
   etc.). Read it before touching code. If you don't know which §,
   see `docs/rebuild/README.md` for the mapping.
2. **Read the archive/ Python** — as INPUT, not template. The
   Python code is the reference implementation for BEHAVIOR ("what
   was Python doing when X happened?"). It is NOT the template for
   HOW to write the Go version. The rewrite exists to raise the
   quality bar: enterprise-grade code, thoughtful concurrency where
   safe, clean domain boundaries. Copy the behavior, redesign the
   shape.
3. **Surface deviations BEFORE coding.** If the plan or Python
   behavior implies design X and you want to do Y, propose Y to the
   user with reasoning first. Silent design decisions are the
   specific failure mode this discipline exists to prevent.

**When shipping the change:**

4. **Update `docs/rebuild/<topic>.md` in the SAME commit.** The
   ledger docs (architecture, orchestration, observability, temporal,
   testing, etc.) get updated with what shipped in the same commit
   that ships the code. A code-only commit is treated as incomplete —
   same status as missing tests.
5. **Log divergences in `docs/decisions.md`.** If what shipped
   differs from `rebuild-plan.md`, add an append-only entry with
   date + rationale. Reference the diverged plan § so the doc trail
   is auditable.
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
       per-fixture parallelism in MonitorWorkflow. Goroutines with
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

See the [2026-07-07 doc retro closure](./docs/decisions.md) for the
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
- Everything runs in Docker. `make build`, `make test`, `make test-short` all spin `golang:1.25-bookworm` throwaway containers with the source bind-mounted.
- Dev stack: `docker compose -f docker-compose.dev.yml up -d` — air hot-reload on all four Go binaries.
- Prod stack: `docker compose -f docker-compose.prod.yml` — **still runs the Python codebase**. Files intentionally renamed from bare `docker-compose.yml` so a stray `docker compose <cmd>` from this directory refuses to resolve.
- Tests: unit + integration in the same package; integration tests spin real Postgres via testcontainers-go (`/var/run/docker.sock` mounted + `--network=host` in the `test` make target). Skip with `-short`.

**Things to check before doing X (Go rebuild):**

- **Replicating the pg adapter pattern for a new adapter (S3-S7)**: `internal/infra/pg/` is the template. `Instruments` struct + `RegisterMetrics(reg, log)` constructor + framework-native tracer (not method wrapping) + `prometheus.Collector` for scrape-time gauges + testcontainers-go integration test. Match the shape.
- **Adding a new Module or Action**: declare the const in [`vocabulary.go`](./internal/observability/vocabulary/vocabulary.go) (module) or the appropriate `actions_<family>.go` (action). Register via `registerActions(...)`. Runtime check in `slogEmitter.Emit` catches strays that slip through the compile-time enum.
- **Adding a new adapter with cleanup**: use `deps.RegisterCloser("name", closeFn)` at construction, not `defer`. Bootstrap drains closers in reverse-registration order (LIFO), which is what Temporal needs (worker drains before its downstream deps close).
- **Adding a new config surface**: put it on its own `config.<Adapter>Config` struct in `internal/config/<adapter>.go`, then reference it from the top-level `Config` in `config.go`. Envconfig via `caarlos0/env/v11`.
- **Touching the pg schema**: authoritative source is [`internal/infra/pg/schema.sql`](./internal/infra/pg/schema.sql). Mounted into dev postgres via `/docker-entrypoint-initdb.d/` (fresh volume only) AND applied in testcontainers via `WithInitScripts`. If you edit it, wipe the dev postgres volume (`docker volume rm found-footy-dev_postgres-data`) to re-provision, and re-run `make test` to catch tests that hit the change.
- **Ports**: metrics/healthz on `:8080` per binary. Public API surface (api) will move to a different port in Phase A. Twitter's :6080 VNC + :8888 API surfaces are Python-era; the Go rewrite lands in Phase S7.

## Python legacy stack (still running in prod)

**Prod at vedanta.systems runs the Python codebase, unchanged.** Every
match day, the Python worker + twitter pools + scaler are what actually
process live matches. The Go rebuild will replace this incrementally
per §13-§14 migration plan; until then, the Python code is the
system-of-record and its docs describe the running system.

Do NOT delete or refactor the Python code from this branch. The
`rebuild/go` branch adds new Go code alongside; it doesn't remove
Python.

**Python stack summary:**

- **Workers**: Python 3.10 (`python:3.10-slim`), Temporal Python SDK, asyncio. 6 workflows, 42 activities. `docker-compose.prod.yml`.
- **Application database**: MongoDB 7, 5-collection design — `fixtures_staging` / `fixtures_live` / `fixtures_active` / `fixtures_completed` / `team_aliases`. The Go rebuild moves to Postgres per [`docs/rebuild-plan.md`](./docs/rebuild-plan.md) §3.
- **Object storage**: MinIO. The Go rebuild moves to Garage per rebuild-plan.md decisions.md 2026-07-01.
- **Twitter scraping**: Firefox + Selenium in `twitter/`. Cookies persisted; shared between dev + prod (same account).
- **External LLM**: Qwen3-VL-8B via llama.cpp on `joi` at `http://llama-small.joi`.
- **Auto-scaler**: Python sidecar watching Temporal queue depth + MongoDB active-goal count.

**Where to look for Python legacy context:**

- [`README.md`](./README.md) — public-facing project description (Python-oriented)
- [`docs/architecture.md`](./docs/architecture.md) — 5-collection MongoDB design + workflow hierarchy
- [`docs/orchestration.md`](./docs/orchestration.md) — event lifecycle state machine, debouncing
- [`docs/temporal.md`](./docs/temporal.md) — per-activity timeouts, retries, heartbeats
- [`docs/logging.md`](./docs/logging.md) — Python `footy_logging` reference (Grafana/Loki queries still apply)
- [`docs/rag.md`](./docs/rag.md) — Wikidata + LLM team-alias pipeline
- [`docs/twitter-auth.md`](./docs/twitter-auth.md) — browser automation, cookie lifecycle, VNC re-auth
- [`docs/operations.md`](./docs/operations.md) — Python-era runbook
- [`docs/decisions.md`](./docs/decisions.md) — append-only architectural decisions log (both eras)
- [`docs/todo.md`](./docs/todo.md) — Python-era active work + open bugs (largely superseded by rebuild but bugs still real in prod)
- [`docs/roadmap.md`](./docs/roadmap.md), [`docs/sprints.md`](./docs/sprints.md) — Python-era rewrite plan (partially superseded — Phases 1-4 shipped and are in prod)
- [`docs/proposals/`](./docs/proposals/) — Python-era feature designs
- [`deploy/INFRA-NOTES.md`](./deploy/INFRA-NOTES.md) — Caddyfile entries + cross-project network setup

## Documentation and docstrings (both eras)

Three layers of persistence, each for a different knowledge type:

1. **`docs/`** — frozen, project-wide knowledge (architecture, decisions,
   roadmap, operational runbook, design proposals). See
   [`docs/README.md`](./docs/README.md) for the Python-era routing
   index. Go-rebuild-specific per-topic docs live under
   [`docs/rebuild/`](./docs/rebuild/).
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

- llama.cpp on joi: live model IDs via `curl http://llama-small.joi/v1/models`
- API-Football: <https://www.api-football.com/documentation-v3> (key in `.env`)
- Temporal Python SDK: <https://docs.temporal.io/develop/python>
- Temporal Go SDK: <https://docs.temporal.io/develop/go>
- Wikidata SPARQL: <https://query.wikidata.org/>
- pgx v5 docs: <https://pkg.go.dev/github.com/jackc/pgx/v5>
- testcontainers-go: <https://golang.testcontainers.org/>

## Load-bearing operational rules

- **Prod is untouchable without explicit per-action user approval.** See the top-of-file callout. It is the single most important rule in this repo and overrides every other instruction.
- **Do NOT `ssh vedanta@joi` from this repo's tooling.** joi is a separate node. HTTP queries over the tailnet are fine; shell commands need explicit user approval.
- **LLM concurrency cap**: joi enforces 2 concurrent calls; throughput drops sharply past that. Any code that fans out LLM calls needs a semaphore or gateway.

---

> **Auto-memory note** — per `~/.claude/CLAUDE.md`, project facts belong here or in `docs/`, **not** in `~/.claude/projects/<project>/memory/`. Keep that auto-memory directory essentially empty.
