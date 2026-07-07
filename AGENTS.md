# Found Footy — Agent Context

Real-time football goal video discovery and curation. Polls API-Football
every 30 s for live matches across the top-5 European leagues + ~15 FIFA
national teams, debounces detected goals over 3 polls, then fires off a
10-attempt Twitter video search for each one. Downloads candidate clips,
AI-validates the broadcast clock against the API's reported match minute
(Qwen3-VL-8B on `joi`), perceptually deduplicates against the existing
S3 corpus, and surfaces the surviving clips through the `vedanta-systems`
portal via SSE.

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
| S3 | ⏳ next | NATS adapter (workspace NATS bus for events + SSE fan-out + JetStream webhook delivery) |
| S4-S7 | 📅 planned | Garage/S3, Temporal, LLM, external HTTP adapters |
| D | 📅 planned | Domain layer (fixture, event, video, alias, discovery, vision, session, textanalysis) |
| O, V, A, T, M, C | 📅 planned | Orchestration, video pipeline, API surface, testing, migration, cutover |

**Where to look for Go rebuild work:**

- [`docs/rebuild-plan.md`](./docs/rebuild-plan.md) — **the design bible**. §1-§16 covers architecture, schema, adapters, workflows, deployment, migration. Read the section relevant to what you're touching before starting.
- [`docs/rebuild/README.md`](./docs/rebuild/README.md) — routing index for per-topic rebuild docs (mostly stubs until each phase fills them in).
- [`docs/rebuild/architecture.md`](./docs/rebuild/architecture.md) — pending
- [`docs/rebuild/orchestration.md`](./docs/rebuild/orchestration.md) — pending
- [`docs/rebuild/observability.md`](./docs/rebuild/observability.md) — pending
- [`docs/rebuild/deployment.md`](./docs/rebuild/deployment.md) — pending
- [`docs/rebuild/testing.md`](./docs/rebuild/testing.md) — pending
- [`internal/observability/vocabulary/vocabulary.go`](./internal/observability/vocabulary/vocabulary.go) — typed enum registry (Module, Action). Every log emission uses these. Adding a new Module or Action = one const declaration.
- [`internal/infra/pg/`](./internal/infra/pg/) — the **template** all future adapters follow: `Instruments` bundle + `RegisterMetrics` constructor + framework-native tracer + prometheus.Collector for scrape-time stats.

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
- Commits: no `Co-Authored-By` trailer. Lowercase prefix + optional scope. Multi-paragraph via HEREDOC.
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

- **Do NOT `ssh vedanta@joi` from this repo's tooling.** joi is a separate node. HTTP queries over the tailnet are fine; shell commands need explicit user approval.
- **Do NOT touch prod containers** (`found-footy-prod-*`) without explicit user approval. Prod runs live at vedanta.systems; wrong actions have real user impact.
- **LLM concurrency cap**: joi enforces 2 concurrent calls; throughput drops sharply past that. Any code that fans out LLM calls needs a semaphore or gateway.

---

> **Auto-memory note** — per `~/.claude/CLAUDE.md`, project facts belong here or in `docs/`, **not** in `~/.claude/projects/<project>/memory/`. Keep that auto-memory directory essentially empty.
