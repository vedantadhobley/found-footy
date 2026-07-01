# Rebuild Plan — 2026-07-01

Prescriptive from-scratch build of found-footy, informed by the lived
knowledge in the existing prod system and by [`design-audit.md`](./design-audit.md)'s
17-section analysis. This document says **what to build**; the audit says
*why* and gives full context. Read this to know what ships; consult the
audit for the reasoning behind each choice.

## How this differs from the audit

| Aspect | [`design-audit.md`](./design-audit.md) | This document |
| ------ | ---------------------------------- | ------------- |
| Framing | Incremental refactor of existing substrate | Fresh build with legacy running in parallel until cutover |
| Language | Python (Python-idiomatic refactor) | **Go, full-stack** — worker, API, scaler, Twitter service |
| Storage layer | Keep Mongo; add discipline via Pydantic + JSON Schema validators + UUID | **Postgres from day one; Mongo not used** |
| Blob storage | MinIO stays (audit didn't call it out) | **Garage; MinIO not used** (community image licensing concerns) |
| Data-layer code | Refactor `mongo_store.py` incrementally into per-domain mixins | Fresh `internal/domains/` Go packages built against `pgx` |
| Orchestration | Temporal kept; workflow IDs unified, activities shrink incrementally | **Temporal kept**; workflows freely re-decomposed to fit domain services (not preserved 1:1) |
| Frontend boundary | FastAPI in front of existing Mongo, incrementally | **Chi + Huma from day one**; vedanta-systems cuts over endpoint-by-endpoint |
| Twitter engine | Selenium + Python | **Playwright-Go**, same operational shape |
| LLM integration | Track 1 gateway proposal (deferred pending nexus timing) | Endpoint fully abstracted; joi today, nexus when ready, config-swap only |
| Tests | Backfill alongside domain extractions | Three-tier pyramid from day one; no legacy code without tests to backfill |
| Documentation | Rich audit + roadmap already exist | Rebuild is the moment to apply everything learned about doc-driven dev |

## How this relates to the roadmap and audit

- [`roadmap.md`](./roadmap.md) F-0..F-6 phases described *incremental*
  work. This rebuild plan supersedes those phases for the *build side*
  — the fresh-build subsumes what the incremental phases would have
  done and lets us skip the "refactor in place" tax.
- [`design-audit.md`](./design-audit.md) sections are still referenced
  throughout this document for the *why* on each decision. When this
  plan says "share-id indirection for URL stability," it's pointing
  at audit §4 for the full rationale, not repeating it.
- [`decisions.md`](./decisions.md) gets dated entries for each
  foundational reversal (Postgres-not-Mongo, Garage-not-MinIO,
  fresh-build-not-incremental) so future-reader isn't left thinking
  the audit and rebuild plan contradict.

---

## 0. Goals and non-goals

### Goals

**Functional (unchanged from today):**
- Real-time discovery, validation, dedup, and public delivery of
  goal-clip videos for tracked-league fixtures.
- 30 s API-Football polling cycle, 3-poll debounce, 10-attempt Twitter
  discovery per goal, AI-validated broadcast-clock timestamps,
  scoped perceptual-hash dedup, S3-compatible blob storage, live SSE
  feed to vedanta-systems + OpenGraph card generation via og-server.
- Same tracked-league + FIFA national-team scope as today.

**Structural (new bar):**
- **Enterprise-grade quality**: typed end-to-end (no `dict[str, Any]`
  crossing service boundaries), schema-enforced at the database, tests
  as a required deliverable of every domain service, deploy automation
  that makes "prod runs main" a mechanical guarantee.
- **Documentation-driven development**: every module has a header
  docstring, every public function/class has a docstring explaining
  purpose and non-obvious invariants, every docs cross-reference is a
  live markdown link, `docs/README.md` is the routing glossary that
  anyone (or any agent) starting fresh consults first.
- **Isolation**: per-project docker-compose owns its full data plane
  (Postgres, Garage, Temporal, workers). Shared workspace-level
  infrastructure is only Caddy proxy + monitor stack + Tailscale.
- **Config-driven external endpoints**: the LLM endpoint is one
  `.env` variable. Switch from joi to nexus is a config change, not
  a code change.
- **Observable**: every activity boundary emits structured errors
  with typed classes; per-match SLO summaries; Grafana dashboards
  for coverage rate, error-class breakdown, deploy freshness; alerts
  that page before matches go dark, not after.
- **URL stability as an architectural invariant**: public share
  URLs never break (per audit §4). Dedup, VAR-removal, asset
  supersession all preserve consumer-facing URLs.

### Non-goals

- **Not rebuilding Temporal orchestration.** Workflow patterns
  (signal-with-start, ABANDON parent close, scoped dedup via
  serialized upload) took real experimentation to get right. Kept
  intent-for-intent.
- **Not rebuilding Twitter's browser layer.** Firefox + Selenium +
  cookie-persisted profiles is committed. The fleet *methodology*
  changes per audit §8; the browser engine doesn't.
- **Not changing the docker-compose deployment model.** Per-project
  isolation is settled. Caddy routing is settled. Tailscale
  networking is settled.
- **Not multi-node.** Single-node luv is fine for the workload;
  audit §15 defers multi-host until observed post-fix utilization
  warrants it.
- **Not migrating historical `fixtures_completed` documents to
  Postgres.** Legacy Mongo data stays in a frozen read-only legacy
  service; new data lives in Postgres. Read-side compatibility is
  a separate small service that projects legacy Mongo docs into the
  new API's response shape when a historical event is queried.
- **Not switching Twitter discovery to the official API v2.**
  Audit §8 keeps this as a separate future decision; unchanged
  here.
- **Not building a workspace-level LLM gateway.** Given nexus's
  eventual role, and given found-footy's LLM concurrency needs are
  bounded (2-cap on joi today, nexus handles its own scheduling),
  a gateway is only worth building if nexus surfaces come out
  narrower than expected. Deferred.

### Success criteria at cutover

Cutover is complete when:

1. All new goal events land in Postgres, not Mongo.
2. All new video blobs land in Garage, not MinIO.
3. vedanta-systems reads from `found-footy.<base-domain>/api/v1/...`,
   not directly from Mongo.
4. og-server generates OG cards via share-id endpoint, not direct
   Mongo reads.
5. Test coverage ≥ 50% by line, with the synthetic harness scenarios
   passing in CI on every push.
6. Deploy gate is live: main pushes trigger rebuild + restart; a
   Grafana panel shows "prod running commit X, main HEAD is
   commit Y" and alerts if drift > 24h.
7. All 15 audit §16 phases either landed or are explicitly deferred
   with a `decisions.md` entry.

---

## 1. Substrate

**Language: Go, full-stack.** Single language across worker, API,
scaler, and Twitter service. Per-project docker-compose owns its full
data plane. The audit's structural invariants
(idempotent workflow tracking, per-event dedup serialization, scoped
comparison by verification status, ABANDON parent-close, share-id
indirection for URL stability, atomic completion marking) *persist*.
Everything else — how workflows decompose, how many activities exist,
which domain owns what, how data flows between them — is up for
redesign. The existing Python code is *lived evidence* of what the
domain requires; it is not a spec to preserve line-for-line.

### Container inventory

| Container | Role | Language | Replaces |
|---|---|---|---|
| `postgres` | Structured data (fixtures, events, videos, aliases, telemetry, event_log for LISTEN/NOTIFY SSE fan-out) | — | Mongo |
| `garage` | S3-compatible blob storage for video files | — | MinIO |
| `temporal` + `temporal-postgres` | Workflow orchestration + its metadata store | — | *(kept)* |
| `temporal-ui` | Workflow observability | — | *(kept)* |
| `worker` | Temporal activity workers (scaled 2–8 by scaler) | **Go** | Python workers |
| `twitter` | Firefox fleet via Playwright-Go (scaled 2–8 by scaler) | **Go** | Python + Selenium |
| `scaler` | Auto-scale controller for `worker` and `twitter` pools | **Go** | Python scaler |
| `api` | HTTP + SSE + webhooks via Chi + Huma | **Go** | Node Express in vedanta-systems reaching into Mongo |

Shared workspace-level infrastructure (not in found-footy's compose):
- `~/workspace/proxy/` — Caddy fronts all HTTP hostnames
- `~/workspace/monitor/` — Prometheus + Grafana + Loki + Promtail

External endpoints:
- **LLM inference**: `LLM_ENDPOINT_URL` in `.env`. Today
  `http://llama-small.joi`. When nexus lands, one `.env` edit and
  container restart; application code unchanged (per the
  [`decisions.md`](./decisions.md) 2026-07-01 entry).
- **API-Football**: unchanged.

### Why Go

- **Simpler as a language than Python.** ~25 keywords, no magic, no
  hidden control flow. Aligns with the readability + maintainability
  goals in [§0](#0-goals-and-non-goals).
- **Massive runtime efficiency.** ~10-100× faster for CPU work,
  ~2-5× for I/O work, ~5-10× less memory per process. Scaler runs
  fewer worker replicas for the same throughput; luv's 125 GB budget
  gets breathing room.
- **Best-supported Temporal SDK.** Temporal's own team builds Go
  first; the SDK is the flagship. Features land in Go before other
  languages.
- **Single-binary deployment.** `go build` produces one file.
  Containers become ~20 MB base + binary instead of Python's
  100+ MB. Restarts take milliseconds. The deploy story from audit
  [§1](./design-audit.md#1-the-builddeploy-gap-prod--main) becomes
  "copy-and-restart" instead of "rebuild-image-and-pray."
- **Native concurrency via goroutines.** No async/await coloring, no
  GIL. Workers use goroutines directly for I/O parallelism; frame
  hashing uses worker pools with real parallelism (per audit
  [§5](./design-audit.md#5-parallelism-and-concurrency)).
- **Compile-time errors replace runtime NameErrors.** The May 2026
  `download.py:819` NameError and `ingest.py:342` NameError would
  have been build failures in Go. For enterprise-grade, this matters.
- **Dependency management: two files.** `go.mod` + `go.sum`.
  `go mod tidy` cleans them. No virtualenv, no `requirements.txt`
  vs `pyproject.toml` vs `poetry.lock` vs `uv.lock` chaos.

Where Go is weaker for this workload — and how the rebuild works
around it:

- **Perceptual hashing / video CV.** No native Go equivalent to
  Python's `opencv-python` or `imagehash`. Rebuild strategy:
  `ffmpeg` CLI subprocess for frame extraction (same pattern
  Python uses today), native Go for the actual dHash computation
  (~30 lines, faster than Python's C-extension-in-Python overhead).
  See §7 for the pipeline shape.
- **AI vision integration.** HTTP call to the LLM endpoint. Trivial
  in any language.
- **Boilerplate on error handling.** `if err != nil { return nil, err }`
  is verbose. Go 1.22+ improvements help; wrapping errors with
  `fmt.Errorf("...%w", err)` gives typed-error semantics comparable
  to the audit's Phase 1 taxonomy from [§7](./design-audit.md#7-error-taxonomy-and-recovery).

### Why Postgres for structured data

Rationale in [`decisions.md`](./decisions.md) 2026-07-01 entry
("Postgres over Mongo"). Go client: **[pgx](https://github.com/jackc/pgx)**.
Native driver, connection pooling, LISTEN/NOTIFY for SSE fan-out
(see §8), type-safe query results via `sqlc` code generation or raw
`Scan`. `database/sql` + `lib/pq` remains a lower-dependency
alternative if we ever want to shrink further; `pgx` wins on
features and ecosystem momentum.

Extensions to enable at day one:
- `pgcrypto` — for share-id UUIDs (see §3 + audit [§4](./design-audit.md#4-dedup-strategy-end-to-end))
- `pg_trgm` — fuzzy team-name matching in RAG
- `pgvector` — embedding-based dedup (audit [§4](./design-audit.md#4-dedup-strategy-end-to-end) Track 3) AND semantic-intent embedding storage (see extensibility hooks below)

TimescaleDB and `pg_partman` deferred until telemetry volume warrants.

### Why Garage for blob storage

Rationale in [`decisions.md`](./decisions.md) 2026-07-01 entry
("Garage over MinIO"). Go S3 client: **`aws-sdk-go-v2/service/s3`**.
Fully S3-compatible, works against any endpoint. Presigned URL
generation for the share-id endpoint (audit
[§4](./design-audit.md#4-dedup-strategy-end-to-end)) is a two-line
call.

Per-project isolation: found-footy runs its own Garage container.
vedanta-systems runs its own. Blob accidents don't cross project
boundaries. Data on disk at `~/workspace/data/found-footy/garage/`
per the CLAUDE.md `data/<project>/` convention; backup is `rsync`
of that directory.

### Why Temporal stays

The load-bearing patterns from audit [§0](./design-audit.md#0-whats-already-working--dont-touch-yet)
persist: signal-with-start for serialized per-event operations,
ABANDON parent-close for outliving parents, idempotent counters via
Postgres unique-index + `INSERT ... ON CONFLICT DO NOTHING`
(replacing Mongo's `$addToSet`), heartbeat-based timeouts for
long-running activities.

**But**: workflow *decomposition* is completely up for redesign.
The existing Python system has 6 workflows and 42 activities. The
Go rebuild might have 5 and 30, or 8 and 60, or something else
entirely — whatever fits the new domain-driven service structure.
Workflows are cheap in Temporal; the question is what makes each
one *coherent*, not "how do we mirror the Python decomposition."

Temporal Go SDK gives idiomatic workflow code (`workflow.Context`,
`workflow.NewChannel`, `workflow.Signal`, etc.). Activities are
plain Go functions registered with the worker. Temporal's own
Postgres (metadata) stays as its own container, independent of the
application Postgres.

### Why Chi + Huma for the API

The FastAPI-equivalent stack in Go:
- **[Chi](https://github.com/go-chi/chi)** — minimalist router built on
  stdlib `net/http`, composable middleware, active community, no
  framework lock-in.
- **[Huma](https://github.com/danielgtaylor/huma)** — OpenAPI spec
  auto-generated from Go struct tags and handler signatures.
  Type-safe request/response models, validation for free, matches
  FastAPI's "define types, get spec + validation" ergonomics.

Handler shape:
```go
type GetEventInput struct {
    ID string `path:"id" doc:"Event share-id"`
}
type GetEventOutput struct {
    Body struct {
        EventID    string       `json:"event_id"`
        FixtureID  int          `json:"fixture_id"`
        Type       string       `json:"type" enum:"Goal,Card,subst,Var"`
        PlayerName *string      `json:"player_name,omitempty"`
        Videos     []VideoLink  `json:"videos"`
    }
}
huma.Register(api, huma.Operation{
    OperationID: "get-event",
    Method:      http.MethodGet,
    Path:        "/api/v1/events/{id}",
}, GetEvent)
```

OpenAPI JSON served at `/api/v1/openapi.json` for vedanta-systems'
TS type generation (per audit [§11](./design-audit.md#11-cross-project-boundary--vedanta-systems-api)).
Middleware for Caddy-forwarded auth, request logging, tracing. SSE
via stdlib `http.Flusher` — no library needed, ~40 lines per stream
endpoint.

Alternative considered and rejected: **Fiber** (Express-like, uses
`fasthttp`). Rejected because `fasthttp` trades stdlib ecosystem
compatibility for raw throughput we don't need.

### Why Playwright-Go for the Twitter service

Firefox + persistent cookies + audit
[§8](./design-audit.md#8-twitter-fleet-management)'s fleet
methodology (Postgres-backed session state, rich `/health` protocol,
graceful drain) all stay. The engine underneath swaps from
Python + Selenium to **[Playwright-Go](https://github.com/playwright-community/playwright-go)**:

- **Officially maintained** (Microsoft-backed). First-class Firefox
  support alongside Chromium and WebKit.
- **Modern API**. Auto-waiting selectors, better DOM inspection,
  network interception (useful for capturing syndication-API
  responses to investigate the snowflake-truncation bug), context
  isolation for multiple concurrent tabs per browser.
- **Same operational model as Selenium.** Browser process managed
  by the library, cookie state in a Firefox profile directory. The
  audit §8 mechanisms (cookies-in-Postgres, hot-swap on version
  bump, rich `/health`, SIGTERM drain) transfer directly.

Fallback if Playwright-Go misses a specific capability:
`github.com/tebeka/selenium` remains as an in-project alternative.
Not expected to be needed.

### Video and vision pipeline (Go-side details)

- **Frame extraction**: `ffmpeg` CLI via `os/exec`. Same subprocess
  pattern as Python today. No CGo, no bindings — the CLI boundary
  is stable and language-agnostic.
- **Perceptual hash (dHash)**: native Go, ~30 lines. Decode
  extracted frame → resize to 9×8 → grayscale → row-adjacent
  differences → 64-bit hash. Faster than Python's `imagehash`
  because the whole hash pipeline runs in native code without
  Python's interpreter overhead per frame.
- **AI vision (soccer/screen/clock)**: HTTP POST to
  `$LLM_ENDPOINT_URL/v1/chat/completions` with multi-image input.
  Via [`github.com/openai/openai-go`](https://github.com/openai/openai-go)
  or plain `net/http` — both work against llama.cpp today and
  nexus tomorrow.

Hash generation and vision inference are activity-scoped: Temporal
calls the activity, activity does its work, returns typed result.
The activities are stateless; Temporal owns the state machine.

### Extensibility hooks — designed-in from day one

The rebuild takes seriously that new capabilities will land beyond
today's feature set. The domain-driven code layout (see
audit [§13](./design-audit.md#13-code-organization--domain-driven-structure-post-phase-3),
adapted for Go packages in §2) means adding a new capability = adding
a new package + wiring it to the workflows that need it, not
rewriting cross-cutting code.

Concrete example: **semantic intent extraction from tweet text**
(user-flagged 2026-07-01). Today's Twitter search returns candidate
videos with tweet text attached; we throw the text away. In the
rebuild:

- Twitter search response includes `tweet_text` alongside video
  URL and duration.
- New activity `AnalyzeTweetIntent` (in `domains/text_analysis`)
  calls the LLM endpoint with the tweet text plus a structured
  prompt: classify source type (broadcaster, media outlet, verified
  fan, random user), event type mentioned (goal / red-card /
  subst / celebration / replay), confidence signals, urgency, plus
  a semantic embedding vector via `pgvector` for similarity-based
  clustering later.
- Classification lands in a `tweet_intent` table joined to the
  video record via `video_id` FK.
- Downstream consumers, all pre-wired but disabled until the domain
  ships:
  - Audit [§10](./design-audit.md#10-filter-pushdown-and-pipeline-ordering)
    source-quality filter scores broadcaster clips higher.
  - Audit [§9](./design-audit.md#9-observability-alerting-deploy-visibility)
    surfaces "% of ingested videos from verified accounts" as a
    coverage-quality metric alongside coverage-rate.
  - Audit [§11](./design-audit.md#11-cross-project-boundary--vedanta-systems-api)
    API exposes intent as optional metadata on event/video responses;
    vedanta-systems can render "from @BBCSport" badges.
  - `pgvector` similarity queries surface "other tweets talking
    about the same moment" for tie-breaking near-duplicate video
    clusters.

Adding this domain touches:
- One new Postgres table (`tweet_intent`) with FK to `videos`
- One new Go package (`domains/text_analysis`)
- One new activity registered on the worker
- One optional field on the API response schema
- Zero workflow rewrites, zero cross-cutting refactor

That is the "designed for extension" bar the rebuild targets. Every
subsequent section (§2 onward) names at least one extensibility hook
its structure enables. The plan is not to build every possible
feature now; it is to build a shape that doesn't fight adding them.

---

## 2. Repository structure

Go module layout follows the community `golang-standards/project-layout`
convention with intentional deviations where they serve clarity. The
top-level layout:

```
found-footy/
├── cmd/                       # one binary per service
│   ├── api/main.go
│   ├── scaler/main.go
│   ├── twitter/main.go
│   └── worker/main.go
├── internal/                  # private application code (not importable externally)
│   ├── domain/                # per-domain bundles — see audit §13
│   │   ├── fixture/
│   │   ├── event/
│   │   ├── video/             # video_asset + video_share, see audit §4
│   │   ├── alias/             # team_aliases + RAG + top-flight cache
│   │   ├── discovery/         # Twitter search + URL extraction + source scoring
│   │   ├── vision/            # frame extraction + dHash + AI vision
│   │   ├── session/           # Twitter fleet management, see audit §8
│   │   └── textanalysis/      # semantic intent extraction (extensibility hook)
│   ├── workflow/              # Temporal workflow definitions
│   ├── activity/              # Temporal activities — thin orchestrators calling domain services
│   ├── api/                   # HTTP handlers, middleware, SSE, webhook delivery
│   ├── scaler/                # Docker API + auto-scale logic
│   ├── infra/                 # infrastructure adapters
│   │   ├── pg/                # Postgres pool + migrations + LISTEN/NOTIFY plumbing
│   │   ├── s3/                # Garage / aws-sdk-go-v2 client wrapper
│   │   ├── llm/               # LLM endpoint client (config-swappable joi → nexus)
│   │   └── temporal/          # Temporal client setup + shared config
│   ├── config/                # settings loading via envconfig
│   ├── logging/               # structured JSON logging (module/action/level)
│   ├── errors/                # typed error taxonomy
│   └── testutil/              # factories, fakes, harness helpers
├── migrations/                # SQL migrations (golang-migrate format)
│   ├── 0001_initial.up.sql
│   ├── 0001_initial.down.sql
│   └── ...
├── deploy/
│   ├── docker-compose.yml     # prod stack
│   ├── docker-compose.dev.yml # dev stack
│   ├── Dockerfile.worker
│   ├── Dockerfile.api
│   ├── Dockerfile.scaler
│   ├── Dockerfile.twitter     # includes Firefox + geckodriver install
│   └── caddy/found-footy.caddy
├── scripts/                   # bin/deploy, capture_scenario, dev helpers
├── docs/                      # existing docs/ structure — routing index + audit + rebuild-plan + decisions + operations + …
├── .claude/                   # skills, memories (skills author'd only when useful)
├── go.mod
├── go.sum
├── AGENTS.md
├── CLAUDE.md → AGENTS.md      # symlink
├── README.md
├── Makefile                   # common tasks (build, test, migrate, deploy)
└── .env.example
```

### Why `cmd/` with one binary per service

Standard Go convention: `cmd/<name>/main.go` produces a binary named
`<name>`. Four binaries = four `main.go` files under `cmd/`, each
importing whatever it needs from `internal/`.

Alternatives considered and rejected:
- **Single binary with subcommands** (`found-footy worker`,
  `found-footy api`). Simpler build, more complex service-boundary
  reasoning. Each Docker container ends up running the same fat
  binary with a different arg. Rejected because service boundaries
  in Go are cleanest at the binary level — dependencies are visible
  from `go list -m all` per binary.
- **One repo per binary.** Overkill for a project this size; the
  binaries share a lot of infrastructure code
  (`internal/infra/pg`, `internal/logging`, `internal/errors`).

### Why `internal/` (not `pkg/`)

`internal/` is Go's built-in mechanism for "not importable from outside
this module." Everything in `internal/` is private to found-footy.
`pkg/` is for public library code intended for import by other Go
projects; found-footy doesn't publish libraries, so `pkg/` would be
noise.

If we ever spin off a Go library shared across projects (e.g., a
common `logging` or `errors` package for the vedanta ecosystem), that
becomes its own module with its own repo, and we import it as a
dependency — not `pkg/` in this repo.

### Domain packages own model + store + service + lifecycle

Each `internal/domain/<name>/` is a bundle:

```
internal/domain/event/
├── doc.go            # package documentation (module-level docstring per policy)
├── model.go          # Go structs — the domain types
├── store.go          # Postgres CRUD via pgx — takes/returns models
├── service.go        # business logic — takes/returns models, calls store
├── lifecycle.go      # state machine (event: detected → tracking → stable → complete/removed)
├── errors.go         # domain-specific typed errors
├── service_test.go   # unit tests with mocked store
├── store_test.go     # integration tests against test Postgres
└── lifecycle_test.go # state machine tests
```

The **service** is what activities call. The **store** is what the
service calls for persistence. The **model** is what everyone
exchanges. The **lifecycle** owns the state machine if the domain
has one (event does; alias probably doesn't).

Activities in `internal/activity/` never call `store.go` directly.
They call `service.go`. This preserves the audit §13 layering:

```
Workflow  →  Activity  →  Service  →  Store  →  Postgres/S3
```

The service layer is the unit test boundary. Store tests are
integration tests. Everything else is workflow/activity tests.

### Why activities and workflows are separate from domain

Temporal-typed code (activities, workflows) has SDK dependencies that
domain code shouldn't inherit. If a domain package imports Temporal,
you can't use the same package from a non-Temporal context (e.g., a
one-shot migration script, a `scripts/` helper). Keeping activities
in `internal/activity/` means domain packages are Temporal-agnostic
and reusable from anywhere.

Activities are small — often 10-40 lines each — because they just
wrap a service call in Temporal's activity semantics (heartbeats,
typed input/output structs, error classification for retry
policies).

### Why `internal/infra/` groups adapters

`pg`, `s3`, `llm`, `temporal` are all "we talk to an external system
via a client we own." Grouping them signals "this is the boundary
between our code and the outside." Each has its own package with:
- Client construction from config
- Connection pooling / retry logic where relevant
- Test doubles (fakes for unit tests, testcontainers-based real
  clients for integration tests)

The `infra/llm` package is where the [`decisions.md`](./decisions.md)
2026-07-01 "LLM endpoint abstracted" invariant lives: one client
struct, reads `LLM_ENDPOINT_URL` from `internal/config`, exposes
methods like `AnalyzeFrames(ctx, images, prompt) → (Response, error)`.
Callers never know it's joi or nexus underneath.

### Migrations at the root

Migrations get their own top-level directory because:
- They're SQL, not Go — they don't belong under `internal/`.
- Multiple tools (worker on boot, dev CLI, CI pipeline) need to
  run them.
- The number grows monotonically over the project lifetime; keeping
  them one level deep makes the file listing readable.

Tool: [`golang-migrate/migrate`](https://github.com/golang-migrate/migrate).
Well-maintained, integrates with Postgres cleanly, supports
up/down pairs, versioned schemas via numbered filenames. Alternative
considered: `pressly/goose`. Either works; `golang-migrate` wins on
Postgres-specific features and CI integration.

Startup pattern: `cmd/worker/main.go` and `cmd/api/main.go` both
call `migrate.Up` on boot before starting their main loops. Idempotent
— re-running is a no-op. Prevents "we deployed the code but forgot
to migrate" incidents.

### Deploy artifacts under `deploy/`

Docker Compose files and Dockerfiles live in `deploy/`. Not at the
root, because there are multiple Dockerfiles (one per binary — a
minimal `alpine:latest` image + the Go static binary) and grouping
them keeps the root clean.

`deploy/caddy/found-footy.caddy` is the per-project Caddy fragment,
symlinked into `~/workspace/proxy/caddy/caddy.d/` per the existing
convention.

### `scripts/` for one-shot tooling

- `bin/deploy` — the deploy hook from audit
  [§1](./design-audit.md#1-the-builddeploy-gap-prod--main); Bash
  script wrapping `git pull` + `docker compose build` +
  `docker compose up -d --no-deps --no-build`.
- `capture_scenario.sh` — records real HTTP traffic for the
  synthetic test harness from audit
  [§12](./design-audit.md#12-testing-strategy).
- `pg_migrate.sh` — wraps `migrate` CLI for manual migration ops.

### `.claude/` for skills and memory

Empty at the start of the rebuild. Skills get added when we find
ourselves pasting the same procedure repeatedly (per the
`SKILL.md` docs). No pre-authoring — skills earn their keep by
usage frequency, and I'll suggest one when a procedure has been
repeated three or more times.

### Makefile for common tasks

Not Go-idiomatic (Go's own build tools are strong), but Makefile
targets are universally understood and integrate with editor
tooling. Targets:

```makefile
build:              # go build all binaries into bin/
test:               # go test ./...
test-integration:   # go test with testcontainers-go
migrate:            # run migrations against dev
migrate-create:     # scaffold new up/down migration pair
lint:               # golangci-lint run
fmt:                # gofmt + goimports
docker-build:       # build all four container images
deploy-dev:         # bin/deploy dev
deploy-prod:        # bin/deploy prod
```

### Testing colocation

Go's convention is `*_test.go` next to the file under test. Each
domain package has its own tests co-located. `internal/testutil/`
provides:
- **Factories** — `event.NewTestEvent(opts…)` that constructs
  realistic models for unit tests
- **Test containers** — helpers to spin up Postgres and Garage in
  Docker for integration tests via `testcontainers-go`
- **Fake LLM** — an in-process HTTP handler that serves canned
  responses for vision tests without hitting joi
- **Fake Twitter service** — same shape for discovery tests

This matches audit §12's three-tier pyramid: factories drive Tier 1
(unit), test containers drive Tier 2 (integration), and Tier 3
(synthetic harness) has its own `test/synthetic/` directory outside
`internal/` because it's not co-located with production code.

### Extensibility hook this structure enables

The **semantic intent extension** from §1 lands as
`internal/domain/textanalysis/` — a new bundle with its own model,
store, service, lifecycle-if-any, and tests. Wiring it in:

1. Add `internal/domain/textanalysis/` bundle (~500 lines total,
   fully tested from day one).
2. Add one migration under `migrations/` creating the `tweet_intent`
   table + FK to `videos`.
3. Add one activity in `internal/activity/text_analysis.go` that
   wraps `textanalysis.Service.Analyze(...)`.
4. Register the activity in `cmd/worker/main.go` (one line).
5. Add optional field to `internal/api/models` response types
   (Huma regenerates OpenAPI on next build; TS types regenerate on
   next vedanta-systems CI run).

Zero changes to `fixture`, `event`, `video`, `alias`, `discovery`,
`vision`, `session` packages. Zero changes to existing workflows.
The layout does the isolating.

---

## 3. Postgres schema

Concrete DDL for every table found-footy needs. This section is
prescriptive — every column, every constraint, every index named,
with reasoning where the choice is non-obvious.

### Design principles

**1. Single table per entity type; lifecycle state is a column, not a
table.** The current Mongo design has separate `fixtures_staging`,
`fixtures_active`, `fixtures_completed` collections. The *semantic*
lifecycle separation is genuinely useful — a fixture in staging is a
different concern than an active one. But the *physical* separation
creates the "which collection has this fixture" ambiguity that
required defensive code to check multiple places (and even then got
it wrong sometimes). One `fixtures` table with a `state` column
enum preserves the semantic separation (state is always visible in
one column, transitions are atomic UPDATE) without the ambiguity.
Partial indexes give the same performance as separate tables for
state-scoped queries; views (`CREATE VIEW fixtures_active AS SELECT
* FROM fixtures WHERE state = 'active'`) preserve the query
ergonomics if code reads more naturally that way.

**2. UUIDs for internal IDs, natural keys as sidecars for humans.**
Per audit [§3](./design-audit.md#3-data-model-mongo-discipline-typing-identity):
`_event_id` in the current Python system is a string-concat
(`"{fixture}_{team}_{player}_{type}_{seq}"`) that's simultaneously a
Mongo key, a workflow ID, a log key, a telemetry partition key, and
a display string. That's five jobs. The rebuild splits them: `UUID`
for internal identity, a `natural_key` text column for the
human-readable form. Workflow IDs reference the UUID; logs and
Temporal UI can show the natural key via the workflow's input args.

**3. No derived / concatenated fields.** Anything that can be
computed from other columns is *not stored*. `_s3_key` in current
Mongo is `"{fixture_id}/{event_id}/{hash}.mp4"` — a concat. In the
rebuild, the storage key is computed in the S3 client wrapper from
input parameters; the database stores only the source parts. Same
for any "display title" fields, formatted strings, or convenience
copies. Read-time composition is fine; write-time redundancy is not.

**4. No overwrite buffers.** `fixtures_live` in current Mongo exists
because updating an active fixture in-place would overwrite our
enhancement fields (embedded events with `_monitor_workflows`
arrays, etc.). It's a workaround for Mongo's embedded-document
merge semantics. In normalized Postgres, API-reported fields
(`api_status_short`, `api_elapsed`) live on the `fixtures` row and
get UPDATE'd each poll; enhancement fields live on `events` /
`event_monitor_workflows` / etc. and never conflict with the fixture
UPDATE. No buffer table needed.

**5. Foreign keys enforce every relationship.** Every reference
column has a `REFERENCES` clause. No dangling references at rest.
`ON DELETE CASCADE` for parent-owned children (events → event
tracking arrays), `ON DELETE RESTRICT` for content-referenced
entities (video_assets → video_shares — deleting an asset with live
shares would break URLs).

**6. Constraints encode invariants.** The rank-drift bug from
2026-06-30 (ranks 0, 0, 2, 3 on Norway-CIV) is a category of bug
Postgres can prevent at write time via a partial unique index on
`(event_id, rank)`. If code tries to write duplicate rank, the
constraint rejects. The database becomes the enforcement layer for
invariants that today live implicitly in code.

**7. Enums for constrained string values.** `event_type`,
`fixture_state`, `share_state`, `source_type` are all enums, not
free-form TEXT. Postgres enums prevent typos at write time and
document valid values in the schema itself.

**8. `pgvector` for embeddings, `pg_trgm` for fuzzy match, `pgcrypto`
for UUIDs.** All three enabled in the initial migration.

### Enums

```sql
-- Fixture lifecycle phase (our concept, derived from API status + our decisions)
CREATE TYPE fixture_state AS ENUM ('staging', 'active', 'completed');

-- Event type (API-Football's classification)
CREATE TYPE event_type AS ENUM ('Goal', 'Card', 'Subst', 'Var');

-- Video share state
CREATE TYPE share_state AS ENUM ('active', 'removed');

-- Tweet source classification (from semantic intent — extensibility hook §1)
CREATE TYPE source_type AS ENUM (
    'broadcaster',      -- official broadcaster account (BBC Sport, ESPN, etc.)
    'media_outlet',     -- media / journalist account
    'verified_fan',     -- verified account, fan-oriented
    'unverified'        -- random user
);

-- Removal reason for shares / events (why did we mark this removed)
CREATE TYPE removal_reason AS ENUM (
    'var',              -- VAR reversed the goal
    'policy',           -- manual policy decision
    'asset_gone'        -- underlying asset deleted (should be rare)
);
```

### Core tables

**`fixtures`** — one row per match, lifecycle state in a column:

```sql
CREATE TABLE fixtures (
    id BIGINT PRIMARY KEY,                                -- API-Football fixture ID
    state fixture_state NOT NULL DEFAULT 'staging',

    -- API-reported (refreshed on each monitor poll for state='active')
    api_status_short TEXT NOT NULL,                       -- 'NS', '1H', 'FT', etc.
    api_status_long TEXT NOT NULL,
    api_elapsed INT,                                      -- match minute (nullable pre-kickoff)
    api_extra INT,                                        -- stoppage time
    kickoff TIMESTAMPTZ NOT NULL,
    home_team_id INT NOT NULL,
    home_team_name TEXT NOT NULL,
    away_team_id INT NOT NULL,
    away_team_name TEXT NOT NULL,
    league_id INT NOT NULL,
    league_name TEXT NOT NULL,
    league_season INT NOT NULL,
    home_score INT,
    away_score INT,

    -- Our enhancement fields
    activated_at TIMESTAMPTZ,                             -- when we moved to 'active'
    completed_at TIMESTAMPTZ,                             -- when we moved to 'completed'
    last_activity_at TIMESTAMPTZ,                         -- for frontend sort ordering
    last_polled_at TIMESTAMPTZ,                           -- most recent monitor cycle

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CHECK (
        (state = 'staging' AND activated_at IS NULL AND completed_at IS NULL) OR
        (state = 'active' AND activated_at IS NOT NULL AND completed_at IS NULL) OR
        (state = 'completed' AND activated_at IS NOT NULL AND completed_at IS NOT NULL)
    )
);

-- Partial indexes per state: hot-path queries stay cheap regardless of table size
CREATE INDEX fixtures_staging_by_kickoff ON fixtures (kickoff) WHERE state = 'staging';
CREATE INDEX fixtures_active_by_polled ON fixtures (last_polled_at) WHERE state = 'active';
CREATE INDEX fixtures_completed_recent ON fixtures (completed_at DESC) WHERE state = 'completed';

-- Retention: rows with state='completed' AND completed_at < now() - interval '14 days' get pruned
```

The CHECK constraint enforces the state ↔ timestamp invariant. You
can't have `state='completed'` without `completed_at` set, and you
can't have `state='staging'` with `activated_at` already set. Whoever
transitions state must write the timestamp in the same UPDATE.

**`events`** — one row per API-reported event, per-fixture unique on
natural key:

```sql
CREATE TABLE events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,

    -- Natural key: unique per fixture, human-readable
    natural_key TEXT NOT NULL,                            -- '{team_id}_{player_id}_{type}_{seq}'

    -- API-reported
    event_type event_type NOT NULL,
    detail TEXT NOT NULL,                                 -- 'Normal Goal', 'Yellow Card', etc.
    team_id INT NOT NULL,
    team_name TEXT NOT NULL,
    player_id INT,                                        -- nullable: API sometimes reports goals with unknown player
    player_name TEXT,
    minute INT NOT NULL,
    extra INT,

    -- Our enhancement fields
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    monitor_complete BOOLEAN NOT NULL DEFAULT FALSE,      -- 3-poll debounce passed
    download_complete BOOLEAN NOT NULL DEFAULT FALSE,     -- 10 download attempts fired
    removed BOOLEAN NOT NULL DEFAULT FALSE,
    removed_reason removal_reason,
    removed_at TIMESTAMPTZ,

    -- Telemetry (Phase 1 from audit) — flexible JSONB for evolving structure
    telemetry JSONB,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (fixture_id, natural_key),                     -- prevents duplicate detection races
    CHECK ((removed = FALSE AND removed_reason IS NULL) OR (removed = TRUE AND removed_reason IS NOT NULL))
);

CREATE INDEX events_fixture ON events (fixture_id);
CREATE INDEX events_pending_work ON events (fixture_id)
    WHERE NOT removed AND (NOT monitor_complete OR NOT download_complete);
CREATE INDEX events_by_first_seen ON events (first_seen_at DESC);
```

The `UNIQUE (fixture_id, natural_key)` is what makes the
sequence-race from audit §3 impossible: two concurrent MonitorWorkflows
racing to detect the same "Goal by player 234 seq 1" both try to
INSERT with the same `(fixture_id, natural_key)` — one wins, the
other gets a `DuplicateKeyError` and knows to look up the winner's
UUID.

**`event_monitor_workflows`** — replaces the Mongo `_monitor_workflows`
array. Idempotent by primary key:

```sql
CREATE TABLE event_monitor_workflows (
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (event_id, workflow_id)
);
```

Registration: `INSERT INTO event_monitor_workflows (event_id, workflow_id) VALUES (...) ON CONFLICT DO NOTHING`. Idempotent. Count for the 3-poll debounce: `SELECT count(*) FROM event_monitor_workflows WHERE event_id = ?`.

**`event_download_workflows`** — same pattern for the 10-download completion tracking:

```sql
CREATE TABLE event_download_workflows (
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    outcome_class TEXT,                                   -- typed error class if failed, NULL if succeeded (Phase 1 taxonomy)
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (event_id, workflow_id)
);
```

Extension over Mongo: `outcome_class` lets telemetry aggregate failure classes per event without walking Loki. Completion marking (Sprint 2's atomic operation from audit §3) becomes an atomic `UPDATE events SET download_complete = TRUE WHERE id = ? AND (SELECT count(*) FROM event_download_workflows WHERE event_id = events.id) >= 10`.

**`event_drop_workflows`** — same pattern for the 3-drop VAR detection:

```sql
CREATE TABLE event_drop_workflows (
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (event_id, workflow_id)
);
```

**`video_assets`** — canonical byte-store per audit
[§4](./design-audit.md#4-dedup-strategy-end-to-end):

```sql
CREATE TABLE video_assets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE RESTRICT,

    -- Storage
    s3_bucket TEXT NOT NULL,
    s3_key TEXT NOT NULL,                                 -- computed from (fixture_id, id), not concatenated at write

    -- Content identity
    perceptual_hash BYTEA NOT NULL,                       -- dHash as raw 8 bytes for fast Hamming
    perceptual_hash_prefix INT NOT NULL,                  -- first 16 bits, indexable for LSH-style bucket lookup
    md5 BYTEA NOT NULL,

    -- Metadata
    width INT NOT NULL,
    height INT NOT NULL,
    duration_ms INT NOT NULL,
    file_size_bytes BIGINT NOT NULL,
    bitrate INT,
    aspect_ratio REAL GENERATED ALWAYS AS (width::REAL / height::REAL) STORED,

    -- Popularity (cross-event vote count)
    popularity INT NOT NULL DEFAULT 1,

    -- Supersession (for future dedup-merge / re-encode / higher-quality replacement)
    superseded_by UUID REFERENCES video_assets(id) ON DELETE SET NULL,

    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (fixture_id, md5),                             -- exact-byte dedup within a fixture
    UNIQUE (fixture_id, perceptual_hash)                  -- perceptual dedup — makes the audit §4 atomic INSERT work
);

CREATE INDEX video_assets_hash_prefix ON video_assets (fixture_id, perceptual_hash_prefix)
    WHERE superseded_by IS NULL;
CREATE INDEX video_assets_fixture_popularity ON video_assets (fixture_id, popularity DESC)
    WHERE superseded_by IS NULL;
```

The `aspect_ratio` is a generated column — computed at read time from
width/height, no double storage. `UNIQUE (fixture_id, perceptual_hash)`
makes the audit §4 concurrent-dedup pattern atomic: two workers
computing the same hash both try to INSERT, one wins, the other
catches `DuplicateKeyError` and looks up the winner to reuse.

**`video_shares`** — public share IDs per audit §4:

```sql
CREATE TABLE video_shares (
    id TEXT PRIMARY KEY,                                  -- 's_<12-hex>', public
    asset_id UUID NOT NULL REFERENCES video_assets(id) ON DELETE RESTRICT,
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE RESTRICT,

    -- Validation snapshot at share creation time
    timestamp_verified BOOLEAN NOT NULL,
    extracted_minute INT,

    -- State
    state share_state NOT NULL DEFAULT 'active',
    removed_reason removal_reason,
    removed_at TIMESTAMPTZ,

    -- Ranking — 1-indexed, unique per event within active state
    rank INT NOT NULL CHECK (rank >= 1),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CHECK ((state = 'active' AND removed_reason IS NULL) OR (state = 'removed' AND removed_reason IS NOT NULL))
);

CREATE UNIQUE INDEX video_shares_event_rank_active
    ON video_shares (event_id, rank)
    WHERE state = 'active';

CREATE INDEX video_shares_event ON video_shares (event_id);
CREATE INDEX video_shares_asset ON video_shares (asset_id);
```

The `UNIQUE INDEX ... WHERE state = 'active'` on `(event_id, rank)`
is the fix for the 2026-06-30 rank-drift bug (ranks `0, 0, 2, 3` on
Norway-CIV). Postgres will reject any attempt to write duplicate
active ranks per event. Rebuild code MUST rank in one transaction:
```
UPDATE video_shares SET rank = new_rank WHERE id = ? AND state = 'active';
```
inside a `BEGIN`/`COMMIT` per event, or the constraint rejects. Impossible to accidentally write 0, 0, 2, 3.

**`team_aliases`** — RAG cache:

```sql
CREATE TABLE team_aliases (
    team_id INT PRIMARY KEY,                              -- API-Football team ID
    team_name TEXT NOT NULL,                              -- original name, for display
    is_national BOOLEAN NOT NULL,
    country TEXT,
    city TEXT,
    wikidata_qid TEXT,
    wikidata_aliases TEXT[] NOT NULL DEFAULT '{}',        -- raw Wikidata pipeline output (audit trail)
    twitter_aliases TEXT[] NOT NULL DEFAULT '{}',         -- LLM-selected, normalized (diacritics stripped)
    llm_model TEXT,                                       -- which model generated twitter_aliases
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX team_aliases_name_trgm ON team_aliases USING gin (team_name gin_trgm_ops);
```

The GIN + `pg_trgm` index enables fuzzy team-name matching for the
RAG pipeline's Wikidata search stage. The `wikidata_aliases` array
stays for audit — we can always re-derive `twitter_aliases` from it
if the LLM model changes.

**`twitter_sessions`** — cookie coordination per audit §8:

```sql
CREATE TABLE twitter_sessions (
    id TEXT PRIMARY KEY,                                  -- 'canonical' — single-row pattern
    cookies BYTEA NOT NULL,                               -- serialized cookie blob
    cookies_version BIGINT NOT NULL DEFAULT 1,            -- monotonic; bumped on each re-auth
    authenticated BOOLEAN NOT NULL DEFAULT FALSE,
    last_refresh_at TIMESTAMPTZ,
    last_search_succeeded_at TIMESTAMPTZ,
    consecutive_auth_failures INT NOT NULL DEFAULT 0,
    estimated_expiry_at TIMESTAMPTZ,
    reauth_notes TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Twitter containers read `cookies_version` before every search; if
newer than their in-memory copy, they hot-swap. Re-auth writes a new
version. Fleet propagation is seconds, not restart-cycles.

**`event_log`** — the SSE fan-out backing table (Postgres LISTEN/NOTIFY):

```sql
CREATE TABLE event_log (
    id BIGSERIAL PRIMARY KEY,
    event_type TEXT NOT NULL,                             -- 'event.detected', 'event.video_ready', 'fixture.completed', ...
    fixture_id BIGINT,
    event_id UUID,
    video_share_id TEXT,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX event_log_created ON event_log (created_at DESC);
CREATE INDEX event_log_event ON event_log (event_id) WHERE event_id IS NOT NULL;

-- Retention: partition by day (pg_partman), drop old partitions after 30 days
```

Workers insert here + `NOTIFY found_footy_events, '<payload>'`. API's SSE handlers `LISTEN` on the channel and forward to connected clients. Webhook delivery worker polls `event_log` for undelivered `event.video_ready` and posts to registered subscribers. No Redis, no message broker.

**`webhook_subscriptions`** — for audit §11 webhook delivery:

```sql
CREATE TABLE webhook_subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    consumer_name TEXT NOT NULL,                          -- 'vedanta-systems', 'og-server', etc.
    url TEXT NOT NULL,
    event_types TEXT[] NOT NULL DEFAULT '{}',             -- empty = all
    hmac_secret TEXT NOT NULL,                            -- for X-FF-Signature
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (consumer_name, url)
);

CREATE TABLE webhook_deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id UUID NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    event_log_id BIGINT NOT NULL REFERENCES event_log(id) ON DELETE CASCADE,
    attempt_count INT NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    last_response_code INT,
    last_response_body TEXT,
    succeeded_at TIMESTAMPTZ,
    give_up_at TIMESTAMPTZ,                               -- set when max_attempts reached
    UNIQUE (subscription_id, event_log_id)                -- one delivery record per (subscription, event)
);

CREATE INDEX webhook_deliveries_pending ON webhook_deliveries (last_attempt_at)
    WHERE succeeded_at IS NULL AND give_up_at IS NULL;
```

Retry semantics live in the schema: pending deliveries are `succeeded_at IS NULL AND give_up_at IS NULL`; retry worker orders by `last_attempt_at` for exponential backoff. Consumer idempotency via `X-FF-Delivery-Id` = `deliveries.id`.

### Extensibility tables

**`tweet_intent`** — semantic intent extraction (§1 extensibility hook, wired in from day one):

```sql
CREATE TABLE tweet_intent (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    video_asset_id UUID NOT NULL REFERENCES video_assets(id) ON DELETE CASCADE,

    -- Source metadata (extracted from Twitter response, not LLM-classified)
    tweet_url TEXT NOT NULL,
    author_handle TEXT NOT NULL,
    author_verified BOOLEAN NOT NULL DEFAULT FALSE,

    -- LLM classification
    source_type source_type NOT NULL,
    event_type_mentioned event_type,
    confidence REAL NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    urgency REAL CHECK (urgency BETWEEN 0 AND 1),

    -- Embedding for similarity clustering
    embedding vector(768),                                -- Qwen3-Embedding-8B dim

    -- Raw text for auditing
    tweet_text TEXT NOT NULL,

    llm_model TEXT NOT NULL,                              -- which model classified
    analyzed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (video_asset_id)                               -- one intent per asset; re-analysis updates
);

CREATE INDEX tweet_intent_source ON tweet_intent (source_type);
CREATE INDEX tweet_intent_embedding ON tweet_intent
    USING hnsw (embedding vector_cosine_ops);
```

The schema is here from migration `0001`; the *domain and activity*
that populate it may not ship until later (audit §16 has this as a
post-MVP capability). Empty table until the domain lands. Zero cost
to leaving it in the schema; costs a lot to add it later if we didn't
plan for the FK from `video_assets`.

### Initial migration file

`migrations/0001_initial.up.sql`:

```sql
-- Extensions
CREATE EXTENSION IF NOT EXISTS pgcrypto;                  -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS pg_trgm;                   -- fuzzy team name matching
CREATE EXTENSION IF NOT EXISTS vector;                    -- pgvector for embeddings

-- Enums (as shown above)
-- Tables (in dependency order):
--   1. fixtures
--   2. events (FK: fixtures)
--   3. event_monitor_workflows, event_download_workflows, event_drop_workflows (FK: events)
--   4. video_assets (FK: fixtures)
--   5. video_shares (FK: video_assets, events)
--   6. tweet_intent (FK: video_assets)
--   7. team_aliases
--   8. twitter_sessions
--   9. event_log
--   10. webhook_subscriptions, webhook_deliveries (FK: event_log)
```

`0001_initial.down.sql` drops everything in reverse dependency order.

### What deliberately isn't stored

Fields present in the current Mongo docs that don't earn a place in
the schema:

- **`_event_id` as string concat.** Replaced by `events.id UUID` + `events.natural_key TEXT`.
- **`_s3_url` and `_s3_key`.** Computed by the S3 client from `(fixture_id, asset_id)`; the domain layer never stores the full URL/key as a redundant copy. If we ever migrate storage backends, no schema migration is needed.
- **`_display_title` and other formatted strings.** Composed at read time in the API projection layer.
- **`_last_monitor` on staging fixtures.** Not needed — staging polls are managed by the monitor workflow's schedule, not by per-row timestamps. If the frontend wants "last activity" ordering, that's `last_activity_at` and it's already there.
- **`_completion_first_seen` counter.** Not needed — completion is a state transition and the check is `all events download_complete = TRUE AND api_status_short IN (terminal_statuses)`. No per-fixture completion counter.
- **`fixtures_live` collection entirely.** As noted in principle #4 above.

### Relationship to audit's schemas

- Audit [§3](./design-audit.md#3-data-model-mongo-discipline-typing-identity) proposed
  JSON Schema validators on Mongo collections + UUID `_event_id` + Pydantic write layer. The rebuild
  achieves all three via native Postgres schema + Go struct tags for pgx (equivalent role to Pydantic).
- Audit [§4](./design-audit.md#4-dedup-strategy-end-to-end) proposed the `video_assets` + `video_shares`
  two-collection Mongo design with share-id indirection. This §3 delivers the Postgres equivalent with
  atomic dedup via `UNIQUE (fixture_id, perceptual_hash)`.
- Audit [§8](./design-audit.md#8-twitter-fleet-management) proposed the `twitter_sessions` Mongo collection
  for cookie coordination. This §3 delivers it as a Postgres table with the same single-row-canonical
  pattern.
- Audit [§9](./design-audit.md#9-observability-alerting-deploy-visibility) proposed structured event logging
  for SSE fan-out. This §3 delivers it as `event_log` + LISTEN/NOTIFY instead of pushing SSE messages from
  workers directly.

### Extensibility hook this schema enables

Beyond the pre-wired `tweet_intent`:

- **Adding a new event type (e.g., red-card highlighting)** = add `'RedCard'` to the `event_type` enum via
  `ALTER TYPE`. Existing tables reference the enum; no schema churn.
- **Adding a new discovery source (e.g., YouTube)** = tweet_intent grows an optional `source_platform` column
  or forks into `source_intent` (rename). Video assets are source-agnostic already.
- **Adding embedding-based dedup** (audit §4 Track 3) = add `embedding vector(768)` column to `video_assets`
  + swap the dedup lookup path from `perceptual_hash_prefix` to embedding similarity. Old rows keep
  `perceptual_hash`; migration is a background embedding backfill.
- **Adding per-league SLO tracking** = new `league_sla_targets` table with FK to `leagues`
  (if we ever normalize leagues). No changes to `fixtures` / `events`.

---

## 4. Domain model

Each domain from [§2](#2-repository-structure) has a `model.go` +
`store.go` + `service.go` (+ `lifecycle.go` where applicable) +
colocated tests. This section names the Go types, the store
interfaces, the service methods, and the state machines — the code
counterpart of the [§3](#3-postgres-schema) schema.

### The design principle everything else falls out of

**Activities never touch Postgres. They call services. Services call
stores. Stores call Postgres via pgx.**

```
Workflow  ─►  Activity  ─►  Service  ─►  Store  ─►  Postgres/S3
```

Consequences:
- **Services are unit-testable with mocked stores** (Tier 1 from audit
  [§12](./design-audit.md#12-testing-strategy)). Business logic gets
  tested without spinning up a database.
- **Stores are integration-testable against real Postgres** via
  testcontainers-go (Tier 2). Schema constraints and SQL behavior get
  exercised exactly as prod will see them.
- **Activities stay small** — 10-40 lines each, wrapping a service call
  in Temporal's `activity.Context` + typed error handling for retry
  decisions.
- **Domain packages don't import Temporal.** They're usable from
  migration scripts, dev CLIs, or one-shot scripts without the SDK
  along for the ride.
- **Cross-domain operations live in `internal/usecases/`**, not shoved
  into an arbitrary domain. `VARRemoveEvent` touches `event`,
  `video_asset`, `video_share` domains — it lives in usecases, calls
  each service, no domain "owns" the multi-touch orchestration.

### Fixture domain — worked example in full

Simplest domain, best to establish the pattern.

**What it owns:**
- `fixtures` table + its indexes and constraints
- Lifecycle transitions (`staging → active → completed`)
- Pre-activation logic (kickoff proximity, emergency activation on API status flip)
- Completion determination (terminal API status + all events download-complete)

**`internal/domain/fixture/model.go`:**

```go
package fixture

import "time"

type State string

const (
    StateStaging   State = "staging"
    StateActive    State = "active"
    StateCompleted State = "completed"
)

// Fixture mirrors the fixtures table from §3. Field order matches the
// schema for review-friendliness; pgx tags do the mapping.
type Fixture struct {
    ID    int64 `db:"id"`
    State State `db:"state"`

    // API-reported (mutable — refreshed each poll for state='active')
    APIStatusShort string     `db:"api_status_short"`
    APIStatusLong  string     `db:"api_status_long"`
    APIElapsed     *int       `db:"api_elapsed"`
    APIExtra       *int       `db:"api_extra"`
    Kickoff        time.Time  `db:"kickoff"`
    HomeTeamID     int        `db:"home_team_id"`
    HomeTeamName   string     `db:"home_team_name"`
    AwayTeamID     int        `db:"away_team_id"`
    AwayTeamName   string     `db:"away_team_name"`
    LeagueID       int        `db:"league_id"`
    LeagueName     string     `db:"league_name"`
    LeagueSeason   int        `db:"league_season"`
    HomeScore      *int       `db:"home_score"`
    AwayScore      *int       `db:"away_score"`

    // Enhancement fields (our concerns)
    ActivatedAt    *time.Time `db:"activated_at"`
    CompletedAt    *time.Time `db:"completed_at"`
    LastActivityAt *time.Time `db:"last_activity_at"`
    LastPolledAt   *time.Time `db:"last_polled_at"`

    CreatedAt time.Time `db:"created_at"`
    UpdatedAt time.Time `db:"updated_at"`
}

// IsAPITerminal reports whether the API status short-code indicates
// the match has ended (any way — including cancellation and forfeit).
func (f *Fixture) IsAPITerminal() bool {
    switch f.APIStatusShort {
    case "FT", "AET", "PEN", "CANC", "ABD", "AWD", "WO":
        return true
    }
    return false
}
```

**`internal/domain/fixture/store.go` (interface only shown):**

```go
type Store interface {
    GetByID(ctx context.Context, id int64) (*Fixture, error)
    GetByIDs(ctx context.Context, ids []int64) ([]Fixture, error)
    ListByState(ctx context.Context, state State) ([]Fixture, error)
    ListActiveIDs(ctx context.Context) ([]int64, error)

    // Upsert inserts or updates by primary key. Used by ingest.
    Upsert(ctx context.Context, f *Fixture) error

    // UpdateAPIFields writes only the API-mutable columns + last_polled_at.
    // Never touches state, activated_at, completed_at, last_activity_at.
    UpdateAPIFields(ctx context.Context, f *Fixture) error

    // TransitionState atomically updates state + the associated timestamp.
    // Rejects transitions that violate the §3 CHECK constraint.
    TransitionState(ctx context.Context, id int64, to State, ts StateTimestamps) error

    // PruneCompleted removes completed fixtures older than the cutoff.
    PruneCompleted(ctx context.Context, olderThan time.Time) (int, error)
}

type StateTimestamps struct {
    ActivatedAt    *time.Time
    CompletedAt    *time.Time
    LastActivityAt *time.Time
}
```

The pgx implementation lives in `store_pgx.go`; the interface is what
services depend on and tests mock.

**`internal/domain/fixture/service.go`:**

```go
type Service struct {
    store  Store
    now    func() time.Time  // injected for deterministic testing
    logger *slog.Logger
}

// Activate transitions staging → active. Called by pre-activation
// (kickoff within lookahead) OR by emergency activation (API already
// shows a live status while we still have it as staging).
func (s *Service) Activate(ctx context.Context, id int64) error {
    f, err := s.store.GetByID(ctx, id)
    if err != nil {
        return fmt.Errorf("fetch: %w", err)
    }
    if f.State != StateStaging {
        return fmt.Errorf("activate %d: %w", id, ErrInvalidTransition)
    }
    now := s.now()
    return s.store.TransitionState(ctx, id, StateActive, StateTimestamps{
        ActivatedAt: &now,
    })
}

// TryComplete moves active → completed IFF the API status is terminal
// AND all events are download-complete. Returns whether the transition
// happened.
func (s *Service) TryComplete(ctx context.Context, id int64, eventsComplete bool) (bool, error) {
    f, err := s.store.GetByID(ctx, id)
    if err != nil { return false, err }
    if f.State != StateActive || !f.IsAPITerminal() || !eventsComplete {
        return false, nil
    }
    now := s.now()
    err = s.store.TransitionState(ctx, id, StateCompleted, StateTimestamps{
        CompletedAt: &now,
    })
    return err == nil, err
}

// RecordPoll updates API-reported fields + last_polled_at. Called by
// monitor after each successful API-Football fetch.
func (s *Service) RecordPoll(ctx context.Context, f *Fixture) error {
    now := s.now()
    f.LastPolledAt = &now
    return s.store.UpdateAPIFields(ctx, f)
}
```

**`internal/domain/fixture/errors.go`:**

```go
var (
    ErrNotFound          = errors.New("fixture not found")
    ErrInvalidTransition = errors.New("invalid state transition")
)
```

**Lifecycle** (this domain's is trivial — one edge, both directions of "backward" impossible):

```
staging ─── Activate ───► active ─── TryComplete ───► completed
```

Emergency activation still routes through `Activate` — the caller decides whether the trigger is "kickoff proximity" or "API status flipped," but the transition primitive is the same. No backdoor transitions.

**Tests** (colocated in `service_test.go`, `store_test.go`, `lifecycle_test.go`):

```go
func TestActivate_FromStaging_Succeeds(t *testing.T) { ... }
func TestActivate_FromActive_ReturnsErrInvalidTransition(t *testing.T) { ... }
func TestActivate_FromCompleted_ReturnsErrInvalidTransition(t *testing.T) { ... }
func TestTryComplete_APINotTerminal_ReturnsFalse(t *testing.T) { ... }
func TestTryComplete_EventsIncomplete_ReturnsFalse(t *testing.T) { ... }
func TestTryComplete_AllReady_Transitions(t *testing.T) { ... }
```

`store_test.go` exercises the pgx implementation against a real Postgres via testcontainers-go — CRUD, CHECK constraint enforcement, PruneCompleted correctness.

### Event domain — the big one

Most consequential domain. Owns the debounce state machine, the tracking arrays, VAR detection, telemetry, and completion marking.

**`internal/domain/event/model.go`:**

```go
type Type string

const (
    TypeGoal  Type = "Goal"
    TypeCard  Type = "Card"
    TypeSubst Type = "Subst"
    TypeVar   Type = "Var"
)

type Event struct {
    ID         uuid.UUID `db:"id"`
    FixtureID  int64     `db:"fixture_id"`
    NaturalKey string    `db:"natural_key"`     // "{team_id}_{player_id}_{type}_{seq}"

    // API-reported
    Type       Type       `db:"event_type"`
    Detail     string     `db:"detail"`
    TeamID     int        `db:"team_id"`
    TeamName   string     `db:"team_name"`
    PlayerID   *int       `db:"player_id"`
    PlayerName *string    `db:"player_name"`
    Minute     int        `db:"minute"`
    Extra      *int       `db:"extra"`

    // Enhancement
    FirstSeenAt      time.Time  `db:"first_seen_at"`
    MonitorComplete  bool       `db:"monitor_complete"`
    DownloadComplete bool       `db:"download_complete"`
    Removed          bool       `db:"removed"`
    RemovedReason    *string    `db:"removed_reason"`
    RemovedAt        *time.Time `db:"removed_at"`

    Telemetry *Telemetry `db:"telemetry"`  // JSONB

    CreatedAt time.Time `db:"created_at"`
    UpdatedAt time.Time `db:"updated_at"`
}

// Telemetry corresponds to the JSONB column. Marshaled/unmarshaled by pgx via
// json.Marshaler; field additions don't require migrations.
type Telemetry struct {
    SearchAttempts           int            `json:"search_attempts"`
    VideosDiscovered         int            `json:"videos_discovered"`
    VideosDownloaded         int            `json:"videos_downloaded"`
    DownloadFailureClasses   map[string]int `json:"download_failure_classes"`
    ValidationPassRate       *float64       `json:"validation_pass_rate,omitempty"`
    PrimaryFailureClass      *string        `json:"primary_failure_class,omitempty"`
    TimeToFirstS3Seconds     *float64       `json:"time_to_first_s3_seconds,omitempty"`
}

// PlayerKnown reports whether the API has identified the scorer.
// The 3-poll debounce is gated on this — no player name → no Twitter search.
func (e *Event) PlayerKnown() bool {
    return e.PlayerID != nil && e.PlayerName != nil && *e.PlayerName != ""
}
```

**Store** (highlights — the interface is broader):

```go
type Store interface {
    GetByID(ctx context.Context, id uuid.UUID) (*Event, error)
    GetByFixtureID(ctx context.Context, fixtureID int64) ([]Event, error)
    GetByNaturalKey(ctx context.Context, fixtureID int64, naturalKey string) (*Event, error)

    // Upsert inserts if new, returns existing if the (fixture_id, natural_key)
    // unique constraint fires. The returned bool indicates whether we created it.
    // This is how the sequence-race from audit §3 gets absorbed atomically.
    Upsert(ctx context.Context, e *Event) (result *Event, created bool, err error)

    UpdateAPIFields(ctx context.Context, e *Event) error

    // Tracking-table operations — all idempotent via ON CONFLICT DO NOTHING.
    // Each returns the resulting count.
    RegisterMonitorWorkflow(ctx context.Context, eventID uuid.UUID, workflowID string) (count int, err error)
    RegisterDownloadWorkflow(ctx context.Context, eventID uuid.UUID, workflowID string, outcome *string) (count int, err error)
    RegisterDropWorkflow(ctx context.Context, eventID uuid.UUID, workflowID string) (count int, err error)

    // Atomic completion marking. §3 §6 pattern:
    //   UPDATE events SET download_complete = TRUE
    //     WHERE id = ? AND NOT download_complete
    //       AND (SELECT count(*) FROM event_download_workflows WHERE event_id = events.id) >= ?
    // Returns whether the flip happened.
    TryMarkMonitorComplete(ctx context.Context, eventID uuid.UUID) (flipped bool, err error)
    TryMarkDownloadComplete(ctx context.Context, eventID uuid.UUID, required int) (flipped bool, err error)

    MarkRemoved(ctx context.Context, eventID uuid.UUID, reason string) error

    // Telemetry patches merge into the JSONB column atomically via jsonb_set.
    UpdateTelemetry(ctx context.Context, eventID uuid.UUID, patch TelemetryPatch) error
}
```

**Service** (highlights):

```go
type Service struct {
    store             Store
    monitorThreshold  int  // 3 for debounce
    downloadThreshold int  // 10 for completion
    dropThreshold     int  // 3 for VAR
    now               func() time.Time
    logger            *slog.Logger
}

// DetectChanges compares API-reported events for a fixture against the
// stored set and returns which need action. Called by
// monitor.process_fixture_events activity.
type DetectionResult struct {
    NewEvents      []Event       // never seen before — upsert them
    UpdatedEvents  []Event       // API fields changed — refresh
    RemovedIDs     []uuid.UUID   // vanished from API — VAR drop candidates
}

func (s *Service) DetectChanges(ctx context.Context, fixtureID int64, apiEvents []APIEventInput) (*DetectionResult, error) {
    stored, err := s.store.GetByFixtureID(ctx, fixtureID)
    if err != nil { return nil, err }
    return diff(stored, apiEvents), nil
}

// RegisterMonitorAndCheckStable registers a monitor workflow's touch and reports
// whether the event has now passed the 3-poll debounce AND has a known player.
func (s *Service) RegisterMonitorAndCheckStable(ctx context.Context, eventID uuid.UUID, workflowID string) (stable bool, err error) {
    e, err := s.store.GetByID(ctx, eventID)
    if err != nil { return false, err }
    count, err := s.store.RegisterMonitorWorkflow(ctx, eventID, workflowID)
    if err != nil { return false, err }
    return count >= s.monitorThreshold && e.PlayerKnown() && !e.MonitorComplete, nil
}

// FlagMonitorComplete atomically flips monitor_complete. Returns whether
// the flip actually happened (false = already true).
func (s *Service) FlagMonitorComplete(ctx context.Context, eventID uuid.UUID) (bool, error) {
    return s.store.TryMarkMonitorComplete(ctx, eventID)
}

// RegisterDownloadAndTryComplete records a completed download workflow with its
// typed outcome class and atomically flips download_complete if the threshold
// is met. Returns (currentCount, flippedComplete, err).
func (s *Service) RegisterDownloadAndTryComplete(ctx context.Context, eventID uuid.UUID, workflowID string, outcome *string) (int, bool, error) {
    count, err := s.store.RegisterDownloadWorkflow(ctx, eventID, workflowID, outcome)
    if err != nil { return 0, false, err }
    if count < s.downloadThreshold {
        return count, false, nil
    }
    flipped, err := s.store.TryMarkDownloadComplete(ctx, eventID, s.downloadThreshold)
    return count, flipped, err
}

// RegisterVARDropAndCheckThreshold records a drop-workflow observation and
// returns whether the 3-drop threshold has been met (caller then marks removed).
func (s *Service) RegisterVARDropAndCheckThreshold(ctx context.Context, eventID uuid.UUID, workflowID string) (bool, error) {
    count, err := s.store.RegisterDropWorkflow(ctx, eventID, workflowID)
    if err != nil { return false, err }
    return count >= s.dropThreshold, nil
}
```

**Lifecycle** (state machine, formalized in `lifecycle.go` as an
explicit type):

```
                                    ┌──── (3 drop workflows) ─────► removed_var (early)
                                    │
detected ─── (3 monitor workflows   │
             AND player_known) ────►│ stable ── (spawns TwitterWorkflow) ─── monitor_complete flipped
                                    │                                       │
                                    │                                       ▼
                                    │                        (10 download workflows registered)
                                    │                                       │
                                    │                                       ▼
                                    │                              download_complete flipped
                                    │                                       │
                                    │                                       ▼
                                    │                                  terminal (per event)
                                    │
                                    └─── (3 drop workflows while in progress) ─► removed_var (mid-flight)
```

Every state transition has:
- A precondition (checked by the service before calling the store)
- An atomic store operation (schema constraints enforce the postcondition)
- A typed outcome the caller can act on

**Typed errors:**

```go
var (
    ErrNotFound          = errors.New("event not found")
    ErrAlreadyRemoved    = errors.New("event already removed")
    ErrInvalidTransition = errors.New("invalid state transition")
)
```

**Tests** — same three-file pattern. `lifecycle_test.go` is
particularly thorough because the state machine is the load-bearing
correctness surface:

```go
func TestDebounce_TwoWorkflows_NotStable(t *testing.T) { ... }
func TestDebounce_ThreeWorkflows_PlayerKnown_Stable(t *testing.T) { ... }
func TestDebounce_ThreeWorkflows_PlayerUnknown_NotStable(t *testing.T) { ... }
func TestDownloadCompletion_NineWorkflows_NoFlip(t *testing.T) { ... }
func TestDownloadCompletion_TenWorkflows_Flip(t *testing.T) { ... }
func TestDownloadCompletion_ElevenWorkflows_Idempotent(t *testing.T) { ... }
func TestVARDrop_TwoDrops_NotRemoved(t *testing.T) { ... }
func TestVARDrop_ThreeDrops_Removed(t *testing.T) { ... }
```

### Video domain

Audit §4 realized: `video_assets` (canonical byte-store) + `video_shares` (public IDs).

**Model highlights:**

```go
type Asset struct {
    ID                   uuid.UUID  `db:"id"`
    FixtureID            int64      `db:"fixture_id"`
    S3Bucket             string     `db:"s3_bucket"`
    S3Key                string     `db:"s3_key"`
    PerceptualHash       []byte     `db:"perceptual_hash"`
    PerceptualHashPrefix int32      `db:"perceptual_hash_prefix"`
    MD5                  []byte     `db:"md5"`
    Width, Height        int        `db:"width" db:"height"`
    DurationMs           int        `db:"duration_ms"`
    FileSizeBytes        int64      `db:"file_size_bytes"`
    Bitrate              *int       `db:"bitrate"`
    AspectRatio          float32    `db:"aspect_ratio"`  // generated column
    Popularity           int        `db:"popularity"`
    SupersededBy         *uuid.UUID `db:"superseded_by"`
    FirstSeenAt          time.Time  `db:"first_seen_at"`
}

type ShareState string
const (
    ShareStateActive  ShareState = "active"
    ShareStateRemoved ShareState = "removed"
)

type Share struct {
    ID                string     `db:"id"`  // "s_<12-hex>"
    AssetID           uuid.UUID  `db:"asset_id"`
    EventID           uuid.UUID  `db:"event_id"`
    TimestampVerified bool       `db:"timestamp_verified"`
    ExtractedMinute   *int       `db:"extracted_minute"`
    State             ShareState `db:"state"`
    RemovedReason     *string    `db:"removed_reason"`
    RemovedAt         *time.Time `db:"removed_at"`
    Rank              int        `db:"rank"`
    CreatedAt         time.Time  `db:"created_at"`
}
```

**Service — the atomic dedup path:**

```go
type AssetService struct { store AssetStore; s3 s3.Client; ... }

// UpsertWithHashDedup attempts to insert a new asset. If a row with the same
// (fixture_id, perceptual_hash) already exists (concurrent UploadWorkflow
// beat us), returns the existing asset and (false, nil).
//
// Under the hood: INSERT ... ON CONFLICT (fixture_id, perceptual_hash)
// DO UPDATE SET popularity = video_assets.popularity + 1 RETURNING id, popularity, ...
//
// The RETURNING clause + ON CONFLICT DO UPDATE make this a single round-trip
// AND bump popularity on the winning row. No two-step "try insert, then look
// up existing" pattern.
func (s *AssetService) UpsertWithHashDedup(ctx context.Context, incoming *Asset) (*Asset, bool, error) { ... }
```

**Service — the ranking path (the load-bearing fix for the 2026-06-30 bug):**

```go
type ShareService struct { store ShareStore; ... }

// RecalculateRanksForEvent recomputes ranks for all active shares of an event
// in a single serializable transaction. The partial UNIQUE INDEX from §3
// makes duplicate ranks physically impossible; if the transaction can't
// achieve a valid ordering (extremely rare — only concurrent share creation
// between SELECT and UPDATE), pgx retries the whole transaction.
//
// Ordering: (timestamp_verified DESC, popularity DESC, file_size DESC).
func (s *ShareService) RecalculateRanksForEvent(ctx context.Context, eventID uuid.UUID) error {
    // Executed inside a REPEATABLE READ transaction with retry-on-serialization-failure.
    // SELECT active shares of eventID JOIN assets for the sort key columns
    // ORDER BY (verified DESC, popularity DESC, file_size DESC)
    // Loop through, i from 1: UPDATE video_shares SET rank = i WHERE id = <share_id>
    // COMMIT
    return s.store.WithRetryableTx(ctx, func(tx pgx.Tx) error { ... })
}
```

The 0-0-2-3 bug from Norway-CIV can't happen here: the partial unique
index rejects duplicate ranks at write time, the transaction ensures
all ranks land together, and pgx retries on serialization failure. No
manual concurrency reasoning by callers.

### Alias domain

Team aliases + RAG pipeline. Wraps Wikidata + LLM calls behind a service.

```go
type Store interface {
    Get(ctx context.Context, teamID int) (*TeamAlias, error)
    Upsert(ctx context.Context, a *TeamAlias) error
    SearchByName(ctx context.Context, query string, limit int) ([]TeamAlias, error)  // pg_trgm
}

type Service struct {
    store    Store
    wikidata WikidataClient  // internal/infra/wikidata
    llm      llm.Client      // internal/infra/llm — the config-swappable client
    logger   *slog.Logger
}

// GetOrResolve returns cached aliases if present, else runs the full RAG
// pipeline: Wikidata search → aliases fetch → LLM narrowing → cache write.
func (s *Service) GetOrResolve(ctx context.Context, teamID int, teamName string) ([]string, error) { ... }
```

The `llm.Client` reference is where the config-swap invariant materializes:
`Service` doesn't know whether it's talking to joi or nexus, and doesn't care.

### Discovery domain

Twitter search orchestration. Consumes the twitter container's HTTP API.

```go
type SearchRequest struct {
    Query         string   `json:"query"`
    ExcludeURLs   []string `json:"exclude_urls"`
    MaxAgeMinutes int      `json:"max_age_minutes"`
}

type DiscoveredVideo struct {
    TweetURL        string  `json:"tweet_url"`
    TweetText       string  `json:"tweet_text"`      // NEW — semantic-intent input
    AuthorHandle    string  `json:"author_handle"`
    AuthorVerified  bool    `json:"author_verified"`  // NEW — source scoring input
    VideoPageURL    string  `json:"video_page_url"`
    DurationSeconds float64 `json:"duration_seconds"`
}

type Service struct {
    twitter TwitterServiceClient  // HTTP client to twitter container
    logger  *slog.Logger
}

func (s *Service) Search(ctx context.Context, req SearchRequest) ([]DiscoveredVideo, error) { ... }
```

Two forward-looking additions vs today's Python: `TweetText` and
`AuthorHandle` + `AuthorVerified`. These aren't used by any current
activity — they land in the response DTOs from day one so the
`textanalysis` domain can consume them when it ships (§1 extensibility
hook).

### Vision domain

Frame extraction (ffmpeg CLI) + dHash (native Go) + LLM vision validation.

```go
type Service struct {
    ffmpeg   FFmpegClient  // shells out to ffmpeg CLI
    llm      llm.Client
    logger   *slog.Logger
}

// ExtractFrames pulls frames at the given normalized time positions
// (0.0 to 1.0 of video duration) using ffmpeg.
func (s *Service) ExtractFrames(ctx context.Context, videoPath string, positions []float64) ([][]byte, error) { ... }

// ComputeDHash produces a 64-bit perceptual hash for a JPEG frame.
// Native Go, ~30 lines: decode → resize 9x8 → grayscale → row-adjacent
// differences → 64-bit output.
func (s *Service) ComputeDHash(ctx context.Context, frameJPEG []byte) ([8]byte, error) { ... }

// ValidateFrames sends multi-image LLM request expecting structured JSON:
//   { SOCCER: "yes"/"no", SCREEN: "yes"/"no", CLOCK: "MM:SS", ADDED: "+N", STOPPAGE_CLOCK: "MM:SS" }
// Returns typed classification per frame + typed error on LLM failure
// (LLMUnavailableError, LLMTimeoutError, etc.).
func (s *Service) ValidateFrames(ctx context.Context, frames [][]byte) (*ValidationResult, error) { ... }
```

### Session domain (Twitter fleet management)

Audit §8 realized. Cookie coordination via the `twitter_sessions` table.

```go
type Session struct {
    ID                      string     `db:"id"`  // always 'canonical'
    Cookies                 []byte     `db:"cookies"`
    CookiesVersion          int64      `db:"cookies_version"`
    Authenticated           bool       `db:"authenticated"`
    LastRefreshAt           *time.Time `db:"last_refresh_at"`
    LastSearchSucceededAt   *time.Time `db:"last_search_succeeded_at"`
    ConsecutiveAuthFailures int        `db:"consecutive_auth_failures"`
    EstimatedExpiryAt       *time.Time `db:"estimated_expiry_at"`
    ReauthNotes             *string    `db:"reauth_notes"`
    UpdatedAt               time.Time  `db:"updated_at"`
}

type Service struct { store Store; logger *slog.Logger }

// GetCanonical returns the canonical session row. Twitter containers call this
// every N seconds; if CookiesVersion differs from their local copy, they
// hot-swap cookies in the running Firefox without restarting.
func (s *Service) GetCanonical(ctx context.Context) (*Session, error) { ... }

// RecordAuthFailure atomically increments consecutive_auth_failures.
// Used by twitter containers when a search hits an auth-required response.
func (s *Service) RecordAuthFailure(ctx context.Context) error { ... }

// PromoteFreshCookies is called after a successful VNC re-auth. It bumps
// cookies_version, sets authenticated=true, resets consecutive_auth_failures.
// All twitter containers see the new cookies on their next GetCanonical call.
func (s *Service) PromoteFreshCookies(ctx context.Context, cookies []byte, notes string) error { ... }
```

Fleet propagation: seconds, not restart-cycles. The stale-auth-across-
replicas bug from audit §8 (silently costing search quality after every
manual VNC re-auth) becomes impossible.

### Text analysis domain (extensibility, stubbed day one)

Populated when semantic intent ships. Schema lives in migration `0001`; domain package is scaffolded with typed shapes and `ErrNotImplemented` bodies.

```go
type SourceType string
const (
    SourceBroadcaster SourceType = "broadcaster"
    SourceMediaOutlet SourceType = "media_outlet"
    SourceVerifiedFan SourceType = "verified_fan"
    SourceUnverified  SourceType = "unverified"
)

type Intent struct {
    ID                 uuid.UUID  `db:"id"`
    VideoAssetID       uuid.UUID  `db:"video_asset_id"`
    TweetURL           string     `db:"tweet_url"`
    AuthorHandle       string     `db:"author_handle"`
    AuthorVerified     bool       `db:"author_verified"`
    SourceType         SourceType `db:"source_type"`
    EventTypeMentioned *event.Type `db:"event_type_mentioned"`
    Confidence         float32    `db:"confidence"`
    Urgency            *float32   `db:"urgency"`
    Embedding          []float32  `db:"embedding"`  // pgvector
    TweetText          string     `db:"tweet_text"`
    LLMModel           string     `db:"llm_model"`
    AnalyzedAt         time.Time  `db:"analyzed_at"`
}

type Service struct { store Store; llm llm.Client; ... }

var ErrNotImplemented = errors.New("textanalysis: not yet implemented")

// Analyze classifies a tweet's text alongside a discovered video and stores
// the result. Called by AnalyzeTweetIntent activity when it eventually ships.
func (s *Service) Analyze(ctx context.Context, req AnalyzeRequest) (*Intent, error) {
    return nil, ErrNotImplemented
}

// FindSimilar returns intent rows whose embedding is within threshold of query.
// Used by future "other tweets talking about this moment" clustering features.
func (s *Service) FindSimilar(ctx context.Context, embedding []float32, threshold float32, limit int) ([]Intent, error) {
    return nil, ErrNotImplemented
}
```

The Store, Service interface, model, and error types all exist. `Analyze` and `FindSimilar` return `ErrNotImplemented`. When someone wants to ship semantic intent, they fill in the bodies + wire the activity to call it. Zero cost to leaving the stubs in place.

### Cross-domain operations — `internal/usecases/`

Operations that touch more than one domain live in `usecases`. They compose domain services + own the cross-domain orchestration + do their own transactionality.

```go
// internal/usecases/var_remove_event.go
package usecases

type VARDeps struct {
    Events        event.Service
    VideoAssets   video.AssetService
    VideoShares   video.ShareService
}

type VAROutcome struct {
    EventID        uuid.UUID
    SharesRemoved  int
    NoOp           bool
}

// VARRemoveEvent handles the multi-domain teardown when the API surfaces
// an event as removed (VAR). Touches event + video_asset + video_share domains.
func VARRemoveEvent(ctx context.Context, deps VARDeps, fixtureID int64, eventID uuid.UUID) (*VAROutcome, error) {
    e, err := deps.Events.GetByID(ctx, eventID)
    if err != nil { return nil, err }
    if e.Removed {
        return &VAROutcome{EventID: eventID, NoOp: true}, nil
    }
    if err := deps.Events.MarkRemoved(ctx, eventID, "var"); err != nil { return nil, err }

    shares, err := deps.VideoShares.GetActiveForEvent(ctx, eventID)
    if err != nil { return nil, err }
    for _, sh := range shares {
        if err := deps.VideoShares.MarkRemoved(ctx, sh.ID, "var"); err != nil { return nil, err }
    }

    return &VAROutcome{EventID: eventID, SharesRemoved: len(shares)}, nil
}
```

Activities call use cases:

```go
// internal/activity/var_remove.go
func VARRemoveActivity(ctx context.Context, input VARInput) (VAROutput, error) {
    outcome, err := usecases.VARRemoveEvent(ctx, deps, input.FixtureID, input.EventID)
    if err != nil {
        // Classify + wrap as typed error for retry policy
        return VAROutput{}, classifyError(err)
    }
    return VAROutput{SharesRemoved: outcome.SharesRemoved, NoOp: outcome.NoOp}, nil
}
```

Workflows call activities. The layering stays clean.

### Testing shape per domain

Every domain ships with three test files:

- **`service_test.go`** — unit tests with mocked store. State machine
  transitions, business rules, error paths. Runs in milliseconds. Uses
  `internal/testutil/factories` to construct realistic models. Uses
  `internal/testutil/mocks` (auto-generated from Store interfaces via
  `mockery` or hand-rolled) for the store.
- **`store_test.go`** — integration tests against a real Postgres via
  testcontainers-go. Constraint enforcement, unique-index race
  scenarios, jsonb_set correctness, transaction retry logic.
- **`lifecycle_test.go`** — pure state machine tests (fixture, event
  domains). No I/O.

Coverage target: ≥ 70% on `service.go`, ≥ 80% on `lifecycle.go`, ≥ 50%
on `store.go` (queries + constraint tests, not every SQL branch).

### Extensibility — adding a new domain

Same pattern every time:

1. Create `internal/domain/<name>/` with `doc.go`, `model.go`,
   `store.go`, `service.go` (+ `lifecycle.go` if the domain has one),
   `errors.go`, and `*_test.go`.
2. Add migration(s) creating the required tables with FKs to existing
   domains.
3. If activities need to call it, add `internal/activity/<name>.go`
   that wraps service calls in Temporal's activity semantics.
4. If it participates in workflows, register the activity in
   `cmd/worker/main.go` (one line each).
5. If it exposes API surface, add response models in `internal/api/models`
   and route handlers in `internal/api/handlers`.

Zero changes to unrelated domains. Zero cross-cutting refactor. The
layering does the isolating.

---

*(Remaining §5..§16 to follow. §4 established the domain-code shape
that §5 orchestration and §6 discovery build against.)*
