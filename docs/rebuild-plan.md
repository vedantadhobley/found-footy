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

*(Remaining sections §3..§16 to follow. §2 established the module
layout that §3 Postgres schema, §4 domain model, §5 orchestration
build against.)*
