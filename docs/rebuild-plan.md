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

## Post-drafting revision: workspace NATS as event bus

**Revised 2026-07-01, per [`decisions.md`](./decisions.md) 2026-07-01 "Workspace
NATS as event bus" entry:** early revisions of this document spec'd Postgres
LISTEN/NOTIFY as the semantic event stream fan-out mechanism. After further
discussion, the decision landed to use **workspace-shared NATS at
`~/workspace/nats/`** instead, sitting alongside Caddy + monitor stack + Tailscale
as ecosystem infrastructure.

Concretely for the sections below:

- **§3 schema**: `event_log` table stays — still the durable audit trail +
  reconnect backfill source.
- **§8**: SSE handler subscribes to NATS `event.>` / `fixture.>` subjects
  instead of `LISTEN`ing on a Postgres channel. Webhook delivery worker
  consumes a JetStream durable consumer instead of polling
  `webhook_deliveries` with `FOR UPDATE SKIP LOCKED`.
- **§9**: new `internal/infra/nats/` adapter (specified below).
- **§10**: docker-compose does NOT run a per-project NATS — NATS is a
  workspace-shared dependency listed in `~/workspace/nats/`.
- **§11**: the "semantic event stream" pillar is NATS, not LISTEN/NOTIFY.
- **§14**: cutover includes vedanta-systems' NATS subscription setup.

Where earlier sections still reference LISTEN/NOTIFY (§1's initial "Why
Postgres" prose, §3's `event_log` comment), those references are historical
and should read as "the durable audit table that ALSO feeds NATS
publications." The wiring landing pattern is: worker/api both INSERTs into
`event_log` (durability) AND calls `nats.Publish` (realtime fan-out); the
two paths are always paired.

Rationale + ecosystem-level context in
[`~/workspace/vedanta-dhobley/docs/decisions.md`](../../vedanta-dhobley/docs/decisions.md)
2026-07-01 entry.

---

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

**Service — the atomic mint+rank path (the load-bearing fix for the 2026-06-30 bug):**

```go
type ShareService struct { store ShareStore; ... }

type MintResult struct {
    ShareID       string    // s_<12-hex>
    ComputedRank  int       // >= 1
}

// MintShareAndRecalculate inserts a new video_shares row at its correct
// rank AND rewrites any displaced ranks in the same transaction.
//
// This is one atomic operation because the schema's CHECK (rank >= 1) is
// not deferrable — a two-step "mint at rank=0, recompute later" flow would
// fail at INSERT time. Computing the rank up front against the event's
// current locked active set + writing the INSERT with the real value
// eliminates the placeholder-rank failure mode.
//
// Sort ordering: (timestamp_verified DESC, popularity DESC, file_size DESC).
// The partial UNIQUE INDEX from §3 makes duplicate active ranks physically
// impossible; if the transaction can't achieve a valid ordering (concurrent
// mint on the same event between SELECT and UPDATE), pgx retries the whole
// function.
func (s *ShareService) MintShareAndRecalculate(
    ctx context.Context,
    assetID, eventID uuid.UUID,
    timestampVerified bool,
    extractedMinute *int,
) (MintResult, error) {
    // Executed inside a REPEATABLE READ transaction with retry-on-serialization-failure:
    //
    //   1. SELECT id, asset_id FROM video_shares WHERE event_id = ?
    //        AND state = 'active' FOR UPDATE
    //      JOIN video_assets ON ...
    //      ORDER BY (timestamp_verified DESC, popularity DESC, file_size DESC)
    //   2. Determine where the incoming share sorts against that set.
    //   3. INSERT INTO video_shares (..., rank) VALUES (..., computedRank).
    //   4. UPDATE displaced shares' rank column so no active rank collides.
    //      The partial UNIQUE INDEX prevents any intermediate collision from
    //      committing.
    //   5. COMMIT. Serialization failure retries from step 1.
    return s.store.WithRetryableTx(ctx, func(tx pgx.Tx) (MintResult, error) { ... })
}
```

The 0-0-2-3 bug from Norway-CIV can't happen here: the partial UNIQUE
INDEX rejects duplicate active ranks at write time, all rank writes for a
mint operation land inside one transaction, `CHECK (rank >= 1)` blocks
placeholder-rank INSERTs at the schema level, and pgx retries on
serialization failure. No manual concurrency reasoning by callers.

### Alias domain

Team aliases for Twitter search queries. Owns the RAG pipeline (Wikidata + LLM) and the cache of team → normalized-aliases mappings.

**What it owns:**
- `team_aliases` table + its `pg_trgm` GIN index
- The RAG pipeline: team_name → Wikidata QID → aliases fetch → LLM narrowing → cache write
- Cache staleness policy (30 days)
- Fuzzy team-name search for disambiguation
- Fallback alias derivation when the RAG pipeline fails (deterministic, name-derived)

**`internal/domain/alias/model.go`:**

```go
package alias

import "time"

type TeamAlias struct {
    TeamID          int       `db:"team_id"`
    TeamName        string    `db:"team_name"`
    IsNational      bool      `db:"is_national"`
    Country         *string   `db:"country"`
    City            *string   `db:"city"`
    WikidataQID     *string   `db:"wikidata_qid"`
    WikidataAliases []string  `db:"wikidata_aliases"`
    TwitterAliases  []string  `db:"twitter_aliases"`
    LLMModel        *string   `db:"llm_model"`
    CreatedAt       time.Time `db:"created_at"`
    UpdatedAt       time.Time `db:"updated_at"`
}

// IsStale reports whether this cache entry is old enough to warrant re-resolution.
func (t *TeamAlias) IsStale(now time.Time, ttl time.Duration) bool {
    return now.Sub(t.UpdatedAt) > ttl
}
```

**Store interface:**

```go
type Store interface {
    Get(ctx context.Context, teamID int) (*TeamAlias, error)
    GetMany(ctx context.Context, teamIDs []int) ([]TeamAlias, error)
    Upsert(ctx context.Context, a *TeamAlias) error

    // SearchByName uses pg_trgm GIN index for fuzzy matching. Returns
    // matches ordered by similarity DESC, capped at limit. Typical
    // similarity threshold: 0.6 (Postgres default).
    SearchByName(ctx context.Context, query string, limit int) ([]TeamAlias, error)

    // ListStale returns cached entries whose updated_at is older than the
    // cutoff, capped at limit. Used by the ingest workflow's background
    // refresh pass.
    ListStale(ctx context.Context, olderThan time.Time, limit int) ([]TeamAlias, error)
}
```

**Service:**

```go
type Service struct {
    store    Store
    wikidata infra.WikidataClient
    llm      infra.LLMClient          // config-swappable joi/nexus
    now      func() time.Time
    cacheTTL time.Duration            // 30 * 24 * time.Hour
    logger   *slog.Logger
}

// GetOrResolve returns the cached twitter_aliases for a team, running the
// full RAG pipeline if not cached or if the cache is stale.
//
// Fallback behavior: if the RAG pipeline fails (Wikidata unreachable, LLM
// unavailable, or LLM returns garbage), returns a deterministic
// name-derived alias set AND ErrRAGFailedFallback so callers can log at
// warning level. The fallback is never cached, so the next call will
// re-attempt the full pipeline.
//
// Retry classification for typed errors this returns:
//   ErrWikidataUnreachable → retry-eligible with backoff
//   ErrLLMUnavailable      → retry-eligible
//   ErrRAGFailedFallback   → not an error; log warning, continue
//   ErrNotFound            → not retry-eligible
func (s *Service) GetOrResolve(ctx context.Context, teamID int) ([]string, error)

// ForceResolve runs the RAG pipeline unconditionally, replacing any cached
// entry. Used by the ingest workflow's pre-caching pass and by manual
// invalidation flows.
func (s *Service) ForceResolve(ctx context.Context, teamID int, teamName string, isNational bool) (*TeamAlias, error)

// InvalidateCache marks a team's cached entry as stale (via UPDATE
// updated_at = epoch), forcing the next GetOrResolve to re-run RAG.
// Used when we detect a team rename in API-Football or want to test
// the RAG pipeline against a specific team.
func (s *Service) InvalidateCache(ctx context.Context, teamID int) error

// SearchByName does fuzzy team-name lookup for disambiguation. Returns
// results ordered by trigram similarity DESC, capped at limit.
// Empty result on no matches (not an error).
func (s *Service) SearchByName(ctx context.Context, query string, limit int) ([]TeamAlias, error)
```

**Typed errors:**

```go
var (
    ErrNotFound            = errors.New("alias: team not found in cache")
    ErrWikidataUnreachable = errors.New("alias: wikidata service unreachable")
    ErrLLMUnavailable      = errors.New("alias: llm service unavailable")
    ErrRAGFailedFallback   = errors.New("alias: rag pipeline failed; using fallback aliases")
)
```

**Lifecycle:** none — there's no state machine. Cache entries are either fresh, stale, or missing. Fresh entries get returned as-is; stale/missing trigger RAG.

**Tests** (`service_test.go`, `store_test.go`):

```go
// service_test.go — mocked store + mocked wikidata + mocked llm
func TestGetOrResolve_CacheHit_ReturnsCached(t *testing.T)
func TestGetOrResolve_CacheMiss_RunsRAGAndCaches(t *testing.T)
func TestGetOrResolve_StaleCache_RunsRAGAndUpdatesCache(t *testing.T)
func TestGetOrResolve_WikidataUnreachable_ReturnsErr(t *testing.T)
func TestGetOrResolve_LLMUnavailable_ReturnsFallbackAndErr(t *testing.T)
func TestGetOrResolve_RAGReturnsGarbage_ReturnsFallbackAndErr(t *testing.T)
func TestForceResolve_Always_ReplacesCache(t *testing.T)
func TestInvalidateCache_MarksStale(t *testing.T)
func TestSearchByName_Match_ReturnsOrderedBySimilarity(t *testing.T)
func TestSearchByName_NoMatch_ReturnsEmpty(t *testing.T)

// store_test.go — real Postgres via testcontainers-go
func TestStore_Upsert_InsertNew(t *testing.T)
func TestStore_Upsert_UpdatesExisting(t *testing.T)
func TestStore_SearchByName_UsesTrgmIndex(t *testing.T)
func TestStore_ListStale_RespectsCutoff(t *testing.T)
```

### Discovery domain

Twitter search orchestration. Owns query construction, response parsing, source-quality scoring, and URL validation. Consumes the twitter container's HTTP API (see §9 for `TwitterServiceClient` interface details).

**What it owns:**
- Twitter search query construction (from player names + team aliases)
- Response DTO shapes for videos discovered from tweets
- Source-quality scoring (broadcaster vs media vs verified fan vs random)
- URL validation at extraction time (snowflake-ID length checks per audit §8)
- Duration pre-filtering (drops < 3s or > 90s at discovery boundary, before download)
- Deduplication of already-discovered URLs across search attempts

**`internal/domain/discovery/model.go`:**

```go
package discovery

import "time"

type SearchRequest struct {
    Query         string   `json:"query"`
    ExcludeURLs   []string `json:"exclude_urls"`
    MaxAgeMinutes int      `json:"max_age_minutes"`  // stop scrolling when tweet is older
}

type SearchResponse struct {
    Videos    []DiscoveredVideo `json:"videos"`
    Metadata  SearchMetadata    `json:"metadata"`
}

type SearchMetadata struct {
    InstanceID       string    `json:"instance_id"`         // which twitter container served this
    ExecutedAt       time.Time `json:"executed_at"`
    LatencyMs        int       `json:"latency_ms"`
    CookiesVersion   int64     `json:"cookies_version"`     // for staleness diagnosis
    TotalTweetsSeen  int       `json:"total_tweets_seen"`   // for observability
}

type DiscoveredVideo struct {
    TweetURL        string    `json:"tweet_url"`
    TweetText       string    `json:"tweet_text"`         // extensibility hook input
    AuthorHandle    string    `json:"author_handle"`
    AuthorVerified  bool      `json:"author_verified"`    // source-scoring input
    AuthorFollowers *int      `json:"author_followers,omitempty"`  // populated when available
    VideoPageURL    string    `json:"video_page_url"`
    DurationSeconds float64   `json:"duration_seconds"`
    PostedAt        time.Time `json:"posted_at"`
}

type SourceScore struct {
    Type            SourceType `json:"type"`
    Score           float64    `json:"score"`         // 0.0-1.0
    ReasoningTag    string     `json:"reasoning_tag"` // "verified_broadcaster" | "media_outlet" | ...
}

type SourceType string
const (
    SourceTypeBroadcaster SourceType = "broadcaster"
    SourceTypeMediaOutlet SourceType = "media_outlet"
    SourceTypeVerifiedFan SourceType = "verified_fan"
    SourceTypeUnverified  SourceType = "unverified"
)
```

**Store interface:** None. Discovery is pure orchestration — no state persists in a discovery-owned table. Source scoring uses in-memory rules and (later) the `textanalysis` domain's classifier when it ships.

**Service:**

```go
type Service struct {
    twitter    infra.TwitterServiceClient  // HTTP client to twitter container
    minDurationSec float64                 // pre-filter floor, e.g. 3.0
    maxDurationSec float64                 // pre-filter ceiling, e.g. 90.0
    logger     *slog.Logger
}

// Search runs one Twitter search attempt via the twitter container.
// Applies URL validation + duration pre-filtering before returning.
// Returns typed errors for retry-classification:
//   ErrTwitterUnreachable    → retry-eligible with backoff
//   ErrTwitterAuthRequired   → not retry-eligible; needs VNC re-auth (alert)
//   ErrTwitterSearchTimeout  → retry-eligible once
//   ErrTwitterFleetDrained   → retry-eligible on different instance
func (s *Service) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)

// BuildQuery constructs a Twitter search string from event details.
// Format: "(FirstName OR LastName) (TeamAlias1 OR TeamAlias2 OR ...)"
// Handles player-name normalization (accent stripping, hyphen splitting).
func (s *Service) BuildQuery(playerName string, teamAliases []string) string

// ValidateURL enforces snowflake-ID length invariants at discovery time
// (audit §8 lived problem 1). Returns ErrURLMalformed for tweets whose
// status IDs are truncated (< 18 or > 19 digits).
func (s *Service) ValidateURL(tweetURL string) error

// ScoreSource assigns a preliminary source-quality score based on
// available Twitter response fields (verified flag, follower count,
// handle pattern matching known broadcaster accounts).
//
// This is the "day one" version; when textanalysis domain ships,
// it will call textanalysis.Analyze() for a richer LLM-based
// classification and merge results.
func (s *Service) ScoreSource(video DiscoveredVideo) SourceScore
```

**Typed errors:**

```go
var (
    ErrTwitterUnreachable   = errors.New("discovery: twitter service unreachable")
    ErrTwitterAuthRequired  = errors.New("discovery: twitter auth expired; needs VNC re-auth")
    ErrTwitterSearchTimeout = errors.New("discovery: search exceeded max duration")
    ErrTwitterFleetDrained  = errors.New("discovery: no healthy twitter instance available")
    ErrURLMalformed         = errors.New("discovery: tweet URL failed snowflake-ID validation")
    ErrInvalidQuery         = errors.New("discovery: query construction failed (missing player name or team aliases)")
)
```

**Lifecycle:** none. Each search is independent; there's no state machine.

**Tests** (`service_test.go`, no store to test):

```go
// service_test.go — mocked twitter client
func TestSearch_HappyPath_ReturnsFilteredResults(t *testing.T)
func TestSearch_DropsShortDurationsAtBoundary(t *testing.T)
func TestSearch_DropsLongDurationsAtBoundary(t *testing.T)
func TestSearch_DropsMalformedURLsAtBoundary(t *testing.T)
func TestSearch_TwitterUnreachable_ReturnsErr(t *testing.T)
func TestSearch_AuthRequired_ReturnsErrNotRetryable(t *testing.T)
func TestSearch_Timeout_ReturnsErr(t *testing.T)
func TestBuildQuery_SinglePlayerNameSingleAlias(t *testing.T)
func TestBuildQuery_MultiPartPlayerName_FirstLastOnly(t *testing.T)
func TestBuildQuery_AccentedName_NormalizesForSearch(t *testing.T)
func TestValidateURL_ValidSnowflake_Passes(t *testing.T)
func TestValidateURL_TruncatedSnowflake_ReturnsErrURLMalformed(t *testing.T)
func TestValidateURL_NonTwitterHost_ReturnsErrURLMalformed(t *testing.T)
func TestScoreSource_VerifiedBroadcasterHandle_HighScore(t *testing.T)
func TestScoreSource_UnverifiedRandom_LowScore(t *testing.T)
```

### Vision domain

Frame extraction, perceptual hashing, AI vision classification, and clock-timestamp verification. Owns the entire "is this actually a soccer goal clip and does its broadcast clock match the reported match minute?" pipeline.

**What it owns:**
- Frame extraction from video files via `ffmpeg` CLI at specified time positions
- 64-bit dHash computation in native Go
- LLM vision classification (soccer/phone-screen/broadcast-clock extraction)
- Broadcast-clock parsing (main clock, added time, stoppage sub-clock)
- Timestamp validation against API's reported match minute (±3 tolerance)
- Smart 2-3 frame strategy (frames at 25%, 75%, tiebreaker at 50%)

**`internal/domain/vision/model.go`:**

```go
package vision

import "time"

type Frame struct {
    JPEGBytes    []byte
    PositionSecs float64  // seconds into video
    PositionFrac float64  // 0.0-1.0 of video duration
}

type DHash struct {
    Bytes [8]byte  // 64-bit perceptual hash
}

// HammingDistance returns the number of differing bits between two hashes.
func (h DHash) HammingDistance(other DHash) int { ... }

type ValidationResult struct {
    IsSoccer          bool                `json:"is_soccer"`
    IsPhoneScreen     bool                `json:"is_phone_screen"`
    SoccerConfidence  float64             `json:"soccer_confidence"`
    ClockExtractions  []ClockExtraction   `json:"clock_extractions"`  // one per frame checked
    FramesChecked     int                 `json:"frames_checked"`     // 2 or 3
    Verdict           ValidationVerdict   `json:"verdict"`
}

type ValidationVerdict string
const (
    VerdictAccepted        ValidationVerdict = "accepted"
    VerdictRejectedNotSoccer ValidationVerdict = "rejected_not_soccer"
    VerdictRejectedPhoneScreen ValidationVerdict = "rejected_phone_screen"
    VerdictRejectedClockMismatch ValidationVerdict = "rejected_clock_mismatch"
    VerdictInconclusive    ValidationVerdict = "inconclusive"
)

type ClockExtraction struct {
    FrameIndex        int      `json:"frame_index"`
    RawClock          string   `json:"raw_clock"`          // e.g. "45:23"
    RawAdded          string   `json:"raw_added"`          // e.g. "+2"
    RawStoppageClock  string   `json:"raw_stoppage_clock"` // e.g. "02:36"
    ParsedMinute      *int     `json:"parsed_minute"`      // combined absolute minute
    ParseError        *string  `json:"parse_error,omitempty"`
}

type TimestampVerification struct {
    APIElapsed        int      // reported by API-Football
    APIExtra          *int
    ExtractedMinute   *int     // computed from vision extractions
    Delta             *int     // extracted - (api_elapsed + api_extra)
    Verified          bool     // |delta| <= 3
    TolerancesTried   []string // for diagnostics: "direct", "stoppage_correction", etc.
}
```

**Store interface:** None. Vision is stateless — the persistence of hashes and validation results lives in the `video` domain's `video_assets` and `video_shares` tables.

**Service:**

```go
type Service struct {
    ffmpeg          infra.FFmpegClient   // shells out to ffmpeg CLI
    llm             infra.LLMClient      // config-swappable joi/nexus
    validationPrompt string              // the multi-image classification prompt
    tolerance       int                  // clock-vs-api tolerance in minutes; default 3
    logger          *slog.Logger
}

// ExtractFrames pulls JPEG frames at normalized positions (0.0-1.0) of
// the video's duration. Uses ffmpeg via os/exec. Positions map to
// -ss seek offsets; each extraction is a separate ffmpeg invocation
// (deterministic seek beats keyframe-relative reads for accuracy).
//
// Returns typed errors:
//   ErrFFmpegNotFound         → environment problem; not retry-eligible
//   ErrFFmpegExtractionFailed → transient; retry-eligible
//   ErrVideoDurationUnknown   → probe failure; not retry-eligible
func (s *Service) ExtractFrames(ctx context.Context, videoPath string, positions []float64) ([]Frame, error)

// ComputeDHash produces a 64-bit perceptual hash for a JPEG frame.
// Native Go: decode → resize to 9x8 → grayscale → row-adjacent
// pixel differences → 64-bit output. Deterministic, ~30 LOC.
func (s *Service) ComputeDHash(ctx context.Context, frame Frame) (DHash, error)

// ComputeDHashDense samples every-N-seconds and returns a slice of
// (positionSecs, hash) pairs. Used for the dense-sampling perceptual
// dedup approach from audit §4.
func (s *Service) ComputeDHashDense(ctx context.Context, videoPath string, intervalSecs float64) ([]DenseHashSample, error)

// ValidateFrames sends a multi-image LLM request expecting structured JSON:
//   { SOCCER: "yes"/"no", SCREEN: "yes"/"no",
//     CLOCK: "MM:SS", ADDED: "+N", STOPPAGE_CLOCK: "MM:SS" }
// per frame.
//
// Smart 2-3 strategy: if 25% and 75% frames agree on SOCCER/SCREEN,
// only two calls are made. If they disagree, 50% frame breaks the tie
// (three calls total). Reduces LLM cap pressure by ~33%.
//
// Returns typed errors:
//   ErrLLMUnavailable → retry-eligible
//   ErrLLMTimeout     → retry-eligible once
//   ErrLLMCapExceeded → retry-eligible with longer backoff
//   ErrLLMBadResponse → not retry-eligible; log for prompt-engineering
func (s *Service) ValidateFrames(ctx context.Context, frames []Frame) (*ValidationResult, error)

// VerifyTimestamp compares the vision-extracted absolute minute against
// the API-reported minute. Applies tolerance (default ±3).
//
// Includes smart OCR correction for stoppage-time: vision may read
// "02:36" (stoppage sub-clock) instead of "92:36" (absolute). Tries
// api_elapsed + parsed as an alternative interpretation before
// declaring mismatch.
func (s *Service) VerifyTimestamp(apiElapsed int, apiExtra *int, extractions []ClockExtraction) TimestampVerification

// PickBestExtraction chooses the most reliable clock extraction from
// multiple frames, weighting by parse success + confidence.
func (s *Service) PickBestExtraction(extractions []ClockExtraction) *ClockExtraction
```

**Helpers (`internal/domain/vision/clockparse.go`):**

```go
// Named helpers, exported for direct testing of the parsers.
func ParseClockField(raw string) (minute int, isRunning bool, err error)
func ParseAddedField(raw string) (extra int, err error)
func ParseStoppageClockField(raw string) (subMinute int, subSecond int, err error)
func ComputeAbsoluteMinute(clockMinute int, isStoppage bool, subMinute int) int
```

**Typed errors:**

```go
var (
    ErrFFmpegNotFound         = errors.New("vision: ffmpeg binary not found")
    ErrFFmpegExtractionFailed = errors.New("vision: frame extraction failed")
    ErrVideoDurationUnknown   = errors.New("vision: could not probe video duration")
    ErrDHashComputeFailed     = errors.New("vision: dhash computation failed")
    ErrLLMUnavailable         = errors.New("vision: llm unavailable")
    ErrLLMTimeout             = errors.New("vision: llm timeout")
    ErrLLMCapExceeded         = errors.New("vision: llm concurrent-cap exceeded")
    ErrLLMBadResponse         = errors.New("vision: llm returned invalid structured json")
    ErrInsufficientFrames     = errors.New("vision: fewer than 2 frames provided to validation")
)
```

**Lifecycle:** none — stateless.

**Tests** (`service_test.go`, `clockparse_test.go`):

```go
// service_test.go — mocked ffmpeg + mocked LLM
func TestExtractFrames_HappyPath(t *testing.T)
func TestExtractFrames_FFmpegMissing_ReturnsErrFFmpegNotFound(t *testing.T)
func TestComputeDHash_DeterministicForSameFrame(t *testing.T)
func TestComputeDHash_HammingDistance_IdenticalFrames_Zero(t *testing.T)
func TestValidateFrames_TwoFramesAgree_ReturnsAccepted(t *testing.T)
func TestValidateFrames_TwoFramesDisagree_CallsThird(t *testing.T)
func TestValidateFrames_LLMUnavailable_ReturnsErr(t *testing.T)
func TestValidateFrames_LLMCapExceeded_ReturnsRetryableErr(t *testing.T)
func TestVerifyTimestamp_WithinTolerance_Verified(t *testing.T)
func TestVerifyTimestamp_OutsideTolerance_NotVerified(t *testing.T)
func TestVerifyTimestamp_StoppageOCRCorrection_Verifies(t *testing.T)
func TestPickBestExtraction_MultipleValid_PicksHighestConfidence(t *testing.T)

// clockparse_test.go — pure functions, table-driven
func TestParseClockField_Running(t *testing.T)         // "45:23"
func TestParseClockField_Halftime(t *testing.T)         // "HT" / "45:00"
func TestParseClockField_FullTime(t *testing.T)         // "FT" / "90:00"
func TestParseClockField_Malformed_Errors(t *testing.T)
func TestParseAddedField_ValidExtra(t *testing.T)       // "+4"
func TestParseAddedField_Empty(t *testing.T)
func TestParseStoppageClock_MinuteSecond(t *testing.T)  // "02:36"
func TestComputeAbsoluteMinute_RegularTime(t *testing.T)
func TestComputeAbsoluteMinute_StoppageTime(t *testing.T)
```

### Session domain (Twitter fleet management)

Audit §8 realized. Owns cookie coordination across Twitter fleet replicas, staleness detection, health-check aggregation, and re-auth orchestration.

**What it owns:**
- `twitter_sessions` table (single-row canonical pattern)
- `cookies_version` monotonic counter for hot-swap coordination
- Consecutive-auth-failure tracking for staleness alerting
- Estimated cookie expiry based on observed lifetimes
- Re-auth notes for operational forensics
- Health-status aggregation for the scaler's fleet-quality decisions

**`internal/domain/session/model.go`:**

```go
package session

import "time"

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

// IsStale reports whether this session is considered stale enough to
// warrant an alert.
func (s *Session) IsStale(now time.Time, failureThreshold int) bool {
    return s.ConsecutiveAuthFailures >= failureThreshold ||
        (s.EstimatedExpiryAt != nil && now.After(*s.EstimatedExpiryAt))
}

// HealthReport is what a twitter container reports to callers via its
// /health endpoint. Audit §8 rich-health protocol.
type HealthReport struct {
    Healthy                  bool      `json:"healthy"`
    Authenticated            bool      `json:"authenticated"`
    CookiesVersionLocal      int64     `json:"cookies_version_local"`
    CookiesVersionCanonical  int64     `json:"cookies_version_canonical"`
    CookiesAgeSecs           int       `json:"cookies_age_seconds"`
    LastSearchLatencyMs      *int      `json:"last_search_latency_ms,omitempty"`
    LastSearchSucceededAt    *time.Time `json:"last_search_succeeded_at,omitempty"`
    ConsecutiveSearchFailures int      `json:"consecutive_search_failures"`
    ConsecutiveAuthFailures   int      `json:"consecutive_auth_failures"`
    BrowserPID               int       `json:"browser_pid"`
    MemoryRSSMB              int       `json:"memory_rss_mb"`
    InFlightSearches         int       `json:"in_flight_searches"`
    Draining                 bool      `json:"draining"`
    DOMCanaryLastStatus      string    `json:"dom_canary_last_status"`
    DOMCanaryLastCheckAt     *time.Time `json:"dom_canary_last_check,omitempty"`
}

// FleetHealth is the scaler-side aggregate view across all twitter instances.
type FleetHealth struct {
    TotalInstances       int
    HealthyInstances     int
    AuthenticatedInstances int
    DrainingInstances    int
    MedianConsecutiveAuthFailures int
    MedianSearchLatencyMs int
    OldestCookiesAgeSecs  int
}
```

**Store interface:**

```go
type Store interface {
    // GetCanonical returns the single canonical row. Creates it with empty
    // cookies and cookies_version=0 if it doesn't exist yet.
    GetCanonical(ctx context.Context) (*Session, error)

    // UpdateCookies atomically writes a new cookie blob AND increments
    // cookies_version in one round-trip. Used by re-auth flow.
    UpdateCookies(ctx context.Context, cookies []byte, notes string) (*Session, error)

    // IncrementAuthFailures atomically bumps consecutive_auth_failures.
    IncrementAuthFailures(ctx context.Context) (newCount int, err error)

    // RecordSearchSuccess atomically resets consecutive_auth_failures
    // and updates last_search_succeeded_at to now.
    RecordSearchSuccess(ctx context.Context) error

    // SetEstimatedExpiry updates the projection based on observed lifetimes.
    SetEstimatedExpiry(ctx context.Context, at time.Time) error
}
```

**Service:**

```go
type Service struct {
    store             Store
    failureThreshold  int              // e.g. 2 — alert threshold
    now               func() time.Time
    logger            *slog.Logger
}

// GetCanonical returns the session for hot-swap comparison.
// Twitter containers call this every N seconds (e.g. 30s) and compare
// cookies_version against their in-memory copy; if newer, they hot-swap.
func (s *Service) GetCanonical(ctx context.Context) (*Session, error)

// HotSwapIfNewer returns (cookies, true) if the canonical cookies_version
// is greater than localVersion, else (nil, false). Simplifies twitter
// container's polling loop.
func (s *Service) HotSwapIfNewer(ctx context.Context, localVersion int64) ([]byte, int64, bool, error)

// RecordAuthFailure increments the failure counter. If it crosses the
// alert threshold, logs at WARN level with structured fields so the
// alert rule fires on the next Prometheus scrape.
func (s *Service) RecordAuthFailure(ctx context.Context) error

// RecordSearchSuccess resets the failure counter atomically. Called by
// twitter containers after any search that returned results without
// auth-required signals.
func (s *Service) RecordSearchSuccess(ctx context.Context) error

// PromoteFreshCookies is called after a successful VNC re-auth. Writes
// the new cookie blob, bumps cookies_version, resets failure counter,
// updates estimated_expiry_at based on observed prior lifetimes.
// Returns the updated Session so callers can log the new version.
func (s *Service) PromoteFreshCookies(ctx context.Context, cookies []byte, notes string) (*Session, error)

// ShouldWarnStale reports whether the canonical session is stale enough
// to fire an operational alert. Composes multiple signals (failure count,
// estimated expiry, time since last success).
func (s *Service) ShouldWarnStale(ctx context.Context) (bool, string, error)
```

**Fleet-wide helpers (aggregating across twitter container `/health` responses):**

```go
// AggregateFleetHealth polls every registered twitter instance's /health
// endpoint and returns the FleetHealth summary. Used by the scaler to
// make quality-aware scaling decisions (audit §8: don't just count
// active goals, factor in fleet health).
func (s *Service) AggregateFleetHealth(ctx context.Context, instanceURLs []string) (*FleetHealth, error)
```

**Typed errors:**

```go
var (
    ErrNoCanonicalSession   = errors.New("session: canonical row missing")
    ErrConcurrentPromotion  = errors.New("session: cookies_version race; retry")
    ErrEmptyCookies         = errors.New("session: cannot promote empty cookies")
)
```

**Lifecycle:** none — the session row exists always (created lazily on first `GetCanonical`). Cookies mutate; the row identity is stable.

**Tests** (`service_test.go`, `store_test.go`):

```go
// service_test.go — mocked store
func TestGetCanonical_HappyPath(t *testing.T)
func TestHotSwapIfNewer_NewerVersion_ReturnsCookies(t *testing.T)
func TestHotSwapIfNewer_SameVersion_ReturnsFalse(t *testing.T)
func TestHotSwapIfNewer_OlderVersion_ReturnsFalse(t *testing.T)
func TestRecordAuthFailure_IncrementsCounter(t *testing.T)
func TestRecordAuthFailure_CrossesThreshold_LogsWarn(t *testing.T)
func TestRecordSearchSuccess_ResetsCounter(t *testing.T)
func TestPromoteFreshCookies_BumpsVersion(t *testing.T)
func TestPromoteFreshCookies_EmptyCookies_ReturnsErr(t *testing.T)
func TestShouldWarnStale_FailuresBelowThreshold_False(t *testing.T)
func TestShouldWarnStale_FailuresAboveThreshold_True(t *testing.T)
func TestShouldWarnStale_ExpiryPassed_True(t *testing.T)
func TestAggregateFleetHealth_AllHealthy_ReturnsCorrectAggregate(t *testing.T)
func TestAggregateFleetHealth_SomeUnhealthy_ReportsMedians(t *testing.T)

// store_test.go — real Postgres via testcontainers-go
func TestStore_GetCanonical_CreatesRowIfMissing(t *testing.T)
func TestStore_UpdateCookies_IncrementsVersionAtomically(t *testing.T)
func TestStore_UpdateCookies_ConcurrentCallsSerialize(t *testing.T)
func TestStore_IncrementAuthFailures_Atomic(t *testing.T)
func TestStore_RecordSearchSuccess_ResetsCounterAtomically(t *testing.T)
```

Fleet propagation: seconds, not restart-cycles. The stale-auth-across-replicas bug from audit §8 (silently costing search quality after every manual VNC re-auth) becomes impossible.

### Text analysis domain (extensibility, stubbed day one)

Populated when semantic intent ships. Full domain package scaffolded from day one: typed shapes, interfaces, and `ErrNotImplemented` service bodies. Schema lives in migration `0001` (per §3) so the FK from `video_assets` exists forever without a retroactive migration.

**What it will own (when implemented):**
- LLM-based tweet-text classification (source type, event type mentioned, confidence, urgency)
- Semantic embedding via `pgvector` for similarity clustering
- Source-quality boost signal fed back to the discovery domain
- Cross-tweet clustering ("other tweets talking about this moment")

**What it owns from day one (stubs):**
- All types, interfaces, struct definitions
- `Service` methods returning `ErrNotImplemented`
- Store implementation (real, functional — the schema exists)

**`internal/domain/textanalysis/model.go`:**

```go
package textanalysis

import (
    "time"
    "github.com/google/uuid"
    "yourorg/found-footy/internal/domain/event"
)

type SourceType string
const (
    SourceBroadcaster SourceType = "broadcaster"
    SourceMediaOutlet SourceType = "media_outlet"
    SourceVerifiedFan SourceType = "verified_fan"
    SourceUnverified  SourceType = "unverified"
)

type Intent struct {
    ID                 uuid.UUID   `db:"id"`
    VideoAssetID       uuid.UUID   `db:"video_asset_id"`
    TweetURL           string      `db:"tweet_url"`
    AuthorHandle       string      `db:"author_handle"`
    AuthorVerified     bool        `db:"author_verified"`
    SourceType         SourceType  `db:"source_type"`
    EventTypeMentioned *event.Type `db:"event_type_mentioned"`
    Confidence         float32     `db:"confidence"`
    Urgency            *float32    `db:"urgency"`
    Embedding          []float32   `db:"embedding"`  // pgvector 768-dim (Qwen3-Embedding-8B)
    TweetText          string      `db:"tweet_text"`
    LLMModel           string      `db:"llm_model"`
    AnalyzedAt         time.Time   `db:"analyzed_at"`
}

type AnalyzeRequest struct {
    VideoAssetID   uuid.UUID
    TweetURL       string
    AuthorHandle   string
    AuthorVerified bool
    TweetText      string
    LLMModelHint   string // optional pin to a specific model version
}

type SimilarityQuery struct {
    Embedding    []float32
    Threshold    float32  // cosine similarity threshold, e.g. 0.85
    Limit        int
    FixtureID    *int64   // scope to a fixture if set
    SourceTypeIn []SourceType  // filter by source types if non-empty
}

type SimilarityResult struct {
    Intent    Intent
    Similarity float32  // 0.0-1.0
}
```

**Store interface** (fully implemented from day one — the schema exists):

```go
type Store interface {
    Get(ctx context.Context, id uuid.UUID) (*Intent, error)
    GetForVideoAsset(ctx context.Context, videoAssetID uuid.UUID) (*Intent, error)
    Upsert(ctx context.Context, i *Intent) error
    ListByFixture(ctx context.Context, fixtureID int64) ([]Intent, error)
    ListBySourceType(ctx context.Context, sourceType SourceType, limit int) ([]Intent, error)

    // FindByEmbedding uses the HNSW vector_cosine_ops index from §3.
    // Returns results ordered by cosine similarity DESC.
    FindByEmbedding(ctx context.Context, q SimilarityQuery) ([]SimilarityResult, error)

    // DeleteByVideoAsset removes an intent row when its parent asset
    // is superseded or deleted. Called by video domain's teardown.
    DeleteByVideoAsset(ctx context.Context, videoAssetID uuid.UUID) error
}
```

**Service** (stubbed with `ErrNotImplemented` until it ships):

```go
type Service struct {
    store           Store
    llm             infra.LLMClient
    classifyPrompt  string  // structured-json classification prompt
    embeddingModel  string  // model ID for embedding call, e.g. "qwen3-embedding-8b"
    logger          *slog.Logger
}

// Analyze runs LLM classification + embedding for a tweet + video pair,
// upserting the resulting Intent row. Idempotent by video_asset_id
// (schema unique constraint enforces one intent per asset; re-analysis
// replaces).
//
// STUBBED: returns ErrNotImplemented.
//
// When implemented, typed errors:
//   ErrLLMUnavailable      → retry-eligible
//   ErrLLMBadResponse      → not retry-eligible; log for prompt work
//   ErrEmbeddingMismatch   → embedding dimensionality != 768; config error
//   ErrVideoAssetNotFound  → not retry-eligible
func (s *Service) Analyze(ctx context.Context, req AnalyzeRequest) (*Intent, error) {
    return nil, ErrNotImplemented
}

// ReAnalyze forces re-classification of an existing intent row (e.g.
// after we've upgraded the classifier prompt or LLM model).
//
// STUBBED: returns ErrNotImplemented.
func (s *Service) ReAnalyze(ctx context.Context, videoAssetID uuid.UUID) (*Intent, error) {
    return nil, ErrNotImplemented
}

// FindSimilar returns intents whose embedding is within threshold of
// the query. Used for cross-tweet clustering ("other tweets talking
// about this moment") and for dedup boost signals.
//
// STUBBED: returns ErrNotImplemented.
func (s *Service) FindSimilar(ctx context.Context, q SimilarityQuery) ([]SimilarityResult, error) {
    return nil, ErrNotImplemented
}

// ClassifySource is a helper for the discovery domain to get a source
// boost signal for a discovered video BEFORE downloading. Cached lookups
// against already-analyzed sources; falls back to rule-based scoring
// when no cached data exists.
//
// STUBBED: returns rule-based fallback only until fully implemented.
func (s *Service) ClassifySource(ctx context.Context, authorHandle string, authorVerified bool) (discovery.SourceScore, error)
```

**Typed errors:**

```go
var (
    ErrNotImplemented     = errors.New("textanalysis: not yet implemented")
    ErrLLMUnavailable     = errors.New("textanalysis: llm unavailable")
    ErrLLMBadResponse     = errors.New("textanalysis: llm returned invalid classification")
    ErrEmbeddingMismatch  = errors.New("textanalysis: embedding dimensionality mismatch")
    ErrVideoAssetNotFound = errors.New("textanalysis: video asset not found for intent")
)
```

**Lifecycle:** none — stateless service, embedding+classification is one-shot per video asset.

**Tests** (`service_test.go`, `store_test.go`):

```go
// service_test.go — stubbed for ErrNotImplemented from day one
func TestAnalyze_Stubbed_ReturnsErrNotImplemented(t *testing.T)
func TestReAnalyze_Stubbed_ReturnsErrNotImplemented(t *testing.T)
func TestFindSimilar_Stubbed_ReturnsErrNotImplemented(t *testing.T)
func TestClassifySource_ReturnsRuleBasedFallback(t *testing.T)

// store_test.go — real from day one (schema exists in migration 0001)
func TestStore_Upsert_InsertNew(t *testing.T)
func TestStore_Upsert_ReplaceExisting(t *testing.T)
func TestStore_GetForVideoAsset_Missing_ReturnsNil(t *testing.T)
func TestStore_FindByEmbedding_UsesHNSWIndex(t *testing.T)
func TestStore_FindByEmbedding_RespectsThreshold(t *testing.T)
func TestStore_ListBySourceType_Filters(t *testing.T)
func TestStore_DeleteByVideoAsset_Cascades(t *testing.T)
```

When someone wants to ship semantic intent, they:
1. Replace the `ErrNotImplemented` returns with real LLM calls
2. Add the corresponding `service_test.go` cases for the real logic
3. Register an `AnalyzeTweetIntent` activity in `internal/activity/text_analysis.go`
4. Wire the activity into whichever workflow needs it (probably `UploadWorkflow` after asset commit)
5. Add API surface (optional field on video/event response types)

Zero changes to unrelated domains.

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

## 5. Orchestration layer — Temporal workflows and activities

Five workflows, ~25 activities. Full spec below: signatures, retry policies,
timeout configs, signal patterns, workflow ID conventions per audit
[§2](./design-audit.md#2-workflow-id-conventions-and-identity),
error handling classification. Activities are grouped by domain
(matching §4's package layout).

### Design principles

**Workflows are deterministic. Activities do side effects.** Every I/O
call (Postgres, S3, HTTP, ffmpeg) happens inside an activity. Workflows
compose activities and encode retry/timeout/heartbeat policies. This is
the standard Temporal split and non-negotiable — workflow determinism
enables replay.

**Workflow IDs are UUID-anchored, not concat-anchored** (audit §2). Every
workflow ID is derived from the UUID `_event_id` from §3, never from
API-mutable strings like team names or player names. Human-readable
identity travels through the workflow's input args, not through the ID.

```
IngestWorkflow    → id = "ingest-scheduled" (schedule owns it)
MonitorWorkflow   → id = "monitor-scheduled"
DiscoveryWorkflow → id = "discovery-<event_uuid>"
DownloadWorkflow  → id = "download-<NN>-<event_uuid>"  // NN = attempt zero-padded
UploadWorkflow    → id = "upload-<event_uuid>"          // deterministic, serialized
```

**Fire-and-forget child workflows use `ABANDON` parent close policy**
(audit §0 invariant). Monitor is a 30s cycle; discovery is a ~10-minute
loop — child MUST outlive parent.

**Per-event upload serialization via `SignalWithStartWorkflow`.** One
UploadWorkflow per event, deterministic ID `upload-<event_uuid>`.
Multiple DownloadWorkflows feed it via signals. FIFO queue inside
UploadWorkflow processes batches. This preserves the audit §0
"per-event dedup serialization" invariant.

**Idempotent counters via Postgres `INSERT ... ON CONFLICT DO NOTHING`.**
Every workflow that participates in a threshold count (monitor debounce,
download completion, VAR drop) registers itself via one of the
`event_*_workflows` tables from §3. Count is `SELECT count(*)` — no
`$addToSet` array manipulation, no counter increments.

**Error classification drives retry policy.** Every activity returns
either a domain typed error (from §4) or a temporal error. Retry policies
are configured per activity based on which error classes are retry-
eligible. Non-retry-eligible errors (`ErrNotFound`, `ErrURLMalformed`,
`ErrLLMBadResponse`) get zero retries; retry-eligible errors
(`ErrLLMUnavailable`, `ErrTwitterUnreachable`) get exponential backoff.

**No workflow calls a service or store directly.** Workflows only call
activities. Activities call services. This preserves testability
(mock-friendly at the activity boundary) and replay safety (services can
be non-deterministic; workflows can't).

### Workflow inventory

| Workflow | Schedule / Trigger | Purpose | Duration |
|---|---|---|---|
| `IngestWorkflow` | Daily 00:05 UTC (schedule) | Fetch 3 days of fixtures, categorize, pre-cache aliases, prune retention | Minutes |
| `MonitorWorkflow` | Every 30s (schedule, SKIP overlap) | Poll active fixtures, detect events, trigger discovery, complete finished fixtures | Seconds |
| `DiscoveryWorkflow` | Fire-and-forget from Monitor (ABANDON) | Twitter search loop per event: up to 10 attempts, spawn DownloadWorkflow per attempt | ~10 min |
| `DownloadWorkflow` | Fire-and-forget from Discovery (ABANDON) | Download videos from one search attempt, validate, hash, signal UploadWorkflow | 1-3 min |
| `UploadWorkflow` | SignalWithStart from Download | Serialized per-event upload queue: dedup, S3 upload, rank recalc | Idle-timeout 5 min |

Down from Python's 6 (RAGWorkflow absorbed into an activity called by
IngestWorkflow — no need for a workflow when it's a bounded synchronous
operation).

### Workflow 1: `IngestWorkflow`

**Purpose:** Daily fixture ingest. Fetches upcoming and in-progress
fixtures from API-Football across a 3-day window, categorizes by
status, upserts into Postgres, pre-caches team aliases via RAG,
prunes fixtures beyond retention.

**Schedule:** `5 0 * * *` (daily 00:05 UTC) — Temporal schedule owns
the workflow ID.

**Signature:**

```go
package workflow

type IngestWorkflowInput struct {
    ManualDate      *time.Time  // nil for scheduled; set for manual re-run
    ManualFixtureIDs []int64    // nil for full-window fetch; set for manual re-ingest
    RetentionDays   int         // default 14
}

type IngestWorkflowOutput struct {
    FixturesUpserted int
    AliasesCached    int
    FixturesPruned   int
    Errors           []string  // non-fatal errors surfaced for observability
}

func IngestWorkflow(ctx workflow.Context, in IngestWorkflowInput) (*IngestWorkflowOutput, error)
```

**Activity call sequence:**

```
1. activity.FetchFixturesForWindow(from, to)      → []APIFixture
2. activity.CategorizeAndUpsertFixtures(fixtures) → { staging, active, completed, skipped counts }
3. activity.ListUniqueTeamsFromFixtures(fixtures) → []TeamRef
4. activity.PreCacheAliasesBatch(teams)           → { success, failed counts }
5. activity.PruneOldFixtures(retentionCutoff)     → count
```

**Retry policy (workflow-level):** none — the workflow itself is idempotent
(re-running the same date reprocesses the same fixtures with UPSERT semantics).
Failure results in a Temporal-level workflow failure that operator can restart.

**Timeout config:**
- Workflow execution timeout: none (runs to completion)
- Task timeout: 60s
- Per-activity: see activity inventory below

### Workflow 2: `MonitorWorkflow`

**Purpose:** Every 30s, poll active fixtures for API updates, detect new/
changed/removed events, trigger DiscoveryWorkflow for newly-stable events,
complete finished fixtures.

**Schedule:** `*/30 * * * * *` (every 30s) — schedule with SKIP overlap
policy (if prior instance still running, skip this cycle).

**Signature:**

```go
type MonitorWorkflowInput struct{}  // no inputs — schedule-driven

type MonitorWorkflowOutput struct {
    FixturesPolled        int
    EventsDetectedNew     int
    EventsMarkedStable    int
    DiscoveriesTriggered  int
    FixturesCompleted     int
    Errors                []string
}

func MonitorWorkflow(ctx workflow.Context, in MonitorWorkflowInput) (*MonitorWorkflowOutput, error)
```

**Activity call sequence:**

```
1. activity.PreActivateUpcoming(30min)              → { activated_count }
2. activity.ListActiveFixtureIDs()                  → []int64
3. activity.FetchFixturesByIDs(ids)                 → []APIFixture (batch)

For each fixture, in parallel via workflow.Go:
  4. activity.RecordFixturePoll(fixture)            → error
  5. activity.DetectEventChanges(fixtureID, apiEvents) → DetectionResult
  6. activity.RegisterEventMonitorWorkflow(eventID, workflow.GetInfo().WorkflowExecution.ID) → count

For each newly-stable event (count >= 3 AND player known):
  7. activity.FlagMonitorComplete(eventID)          → flipped bool
  IF flipped:
    workflow.ExecuteChildWorkflow(DiscoveryWorkflow, ...) with ABANDON

For each removed event (VAR candidate):
  8. activity.RegisterEventDropWorkflow(eventID, workflow.GetInfo().WorkflowExecution.ID) → count
  IF count >= 3:
    activity.MarkEventRemoved(eventID, "var")

For each fixture:
  9. activity.EventsFullyComplete(fixtureID)        → bool (all events download_complete OR removed)
  IF bool AND fixture.APITerminal:
    activity.CompleteFixture(fixtureID)             → success/no-op

10. activity.PublishFrontendRefresh()               → error (best-effort SSE trigger)
```

**Retry policy (workflow-level):** none — SKIP overlap policy handles
"prior instance still running" gracefully. A failed instance just means
we'll try again in 30 seconds.

**Timeout config:**
- Workflow execution timeout: 25s (leave headroom before next scheduled cycle)
- Task timeout: 10s (fast fail; next cycle retries)

**Child workflow spawn pattern (DiscoveryWorkflow):**

```go
discoveryOpts := workflow.ChildWorkflowOptions{
    WorkflowID: fmt.Sprintf("discovery-%s", event.ID.String()),
    ParentClosePolicy: enums.PARENT_CLOSE_POLICY_ABANDON,
    WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
    TaskQueue: "found-footy",
}
workflow.ExecuteChildWorkflow(
    workflow.WithChildOptions(ctx, discoveryOpts),
    DiscoveryWorkflow,
    DiscoveryInput{EventID: event.ID, /*...*/},
)
// NOTE: we don't await the future — fire-and-forget.
```

`REJECT_DUPLICATE` is what makes the stable workflow ID from audit §2
load-bearing: Temporal itself rejects a second discovery spawn for the
same event.

### Workflow 3: `DiscoveryWorkflow`

**Purpose:** Per event. Twitter search loop with up to 10 attempts,
1-minute spacing (audit §8 discovery-hardening addition: adaptive,
can early-exit if N consecutive attempts yield no new videos).

**Trigger:** Child workflow of MonitorWorkflow with ABANDON.

**Signature:**

```go
type DiscoveryWorkflowInput struct {
    EventID       uuid.UUID
    FixtureID     int64
    PlayerName    string     // for logs + query construction
    TeamName      string     // for logs + query construction
    TeamID        int        // for alias resolution
    Minute        int
    Extra         *int
    FirstSeenAt   time.Time  // for spacing computation
    MaxAttempts   int        // default 10
}

type DiscoveryWorkflowOutput struct {
    AttemptsExecuted    int
    VideosDiscovered    int
    DownloadsSpawned    int
    EarlyExitReason     string  // "downloads_target_met" | "consecutive_empty" | "event_removed" | ""
}

func DiscoveryWorkflow(ctx workflow.Context, in DiscoveryWorkflowInput) (*DiscoveryWorkflowOutput, error)
```

**Activity call sequence:**

```
1. activity.GetOrResolveTeamAliases(TeamID)       → []string
2. activity.SaveEventTwitterAliases(eventID, aliases) → error (for post-hoc audit)

For attempt = 1..MaxAttempts:
  3. activity.CheckEventStillLive(eventID)        → bool (false = VAR'd or removed)
     IF false: return early ("event_removed")

  4. activity.CountDownloadWorkflowsForEvent(eventID) → int
     IF >= 10: return early ("downloads_target_met")

  5. activity.SearchTwitter(discovery.SearchRequest{
       Query: BuildQuery(PlayerName, aliases),
       ExcludeURLs: workflow-local-set,
       MaxAgeMinutes: 3,
     }) → discovery.SearchResponse

  Update workflow-local excludeURLs with each returned tweet_url.

  IF response.Videos empty AND consecutive_empty_count >= 2:
    early-exit ("consecutive_empty") — the adaptive addition from audit §8

  6. For each returned video (top 5 by duration):
     activity.RegisterEventDiscoveredVideo(eventID, video) → error

  7. workflow.ExecuteChildWorkflow(
       DownloadWorkflow,
       DownloadInput{EventID, Attempt: N, Videos: top5},
     ) with ABANDON
     // fire-and-forget; DownloadWorkflow registers itself into
     // event_download_workflows on start (idempotency invariant).

  8. workflow.Sleep(spacing) — 60s from start of attempt, min 10s guard
```

**Retry policy (workflow-level):** none — the workflow's own retry loop
is the retry mechanism. Individual activity failures within an attempt
are handled by activity-level retry policies (below).

**Timeout config:**
- Workflow execution timeout: 20 min (10 attempts × 1 min + buffer)
- Task timeout: 60s

### Workflow 4: `DownloadWorkflow`

**Purpose:** One search attempt's worth of downloads. Register itself in
tracking table AT START (idempotency invariant). Download in parallel,
MD5 batch-dedup, AI validate (sequential to respect LLM cap), hash,
signal UploadWorkflow with the surviving batch.

**Trigger:** Child of DiscoveryWorkflow with ABANDON.

**Signature:**

```go
type DownloadWorkflowInput struct {
    EventID     uuid.UUID
    FixtureID   int64
    Attempt     int              // 1..10
    Videos      []discovery.DiscoveredVideo
    APIElapsed  int              // for VerifyTimestamp
    APIExtra    *int
}

type DownloadWorkflowOutput struct {
    VideosDownloaded  int
    ValidatedSoccer   int
    HashesGenerated   int
    SignaledUpload    bool
    FailureClasses    map[string]int  // typed error class name → count
}

func DownloadWorkflow(ctx workflow.Context, in DownloadWorkflowInput) (*DownloadWorkflowOutput, error)
```

**Activity call sequence:**

```
// STEP 0: Register self FIRST (audit §0 invariant). Retry hard because
// idempotency requires this to succeed.
1. activity.RegisterDownloadWorkflow(
     eventID, workflow.GetInfo().WorkflowExecution.ID, initialOutcome=nil,
   ) → count

2. activity.CheckEventStillLive(eventID) → bool (VAR abort)
   IF false: skip to signal-upload-empty step

// STEP 1: Parallel downloads via workflow.Go
For each video in Input.Videos (up to 5 per attempt):
  3. activity.DownloadVideo(video.TweetURL) → downloadedFile OR typed error
     Retry policy: 3 attempts, 2x backoff from 2s. Non-retryable classes:
       ErrURLMalformed, VideoGeoRestrictedError, VideoNotAvailableError,
       VideoDeletedError

// STEP 2: MD5 batch dedup
4. activity.DedupBatchByMD5(downloadedFiles) → survivorFiles

// STEP 3: Sequential AI validation (respects LLM cap)
For each surviving file:
  5. activity.ValidateVideoIsSoccer(file, APIElapsed, APIExtra)
     → ValidationResult

     Retry policy: 4 attempts, 2x backoff from 3s. Retry-eligible:
       ErrLLMUnavailable, ErrLLMTimeout, ErrLLMCapExceeded.
     Non-retryable: ErrLLMBadResponse (log for prompt work).

  IF Verdict != Accepted: mark file as filtered, continue

// STEP 4: Parallel hash generation
For each accepted file, workflow.Go:
  6. activity.GenerateVideoHash(file) → dHashDenseSamples
     Heartbeat every 5 frames.
     Retry: 2 attempts.

// STEP 5: Signal UploadWorkflow (always, even if empty — see reason below)
7. activity.SignalUploadBatch(
     eventID,
     batch of (file, validationResult, hash, source_url),
   ) → error

// STEP 6: Mark self complete in registration row
8. activity.RegisterDownloadWorkflow(
     eventID, workflowID,
     initialOutcome=classifyOverallOutcome(),  // "success", "empty", "all_geo_restricted", etc.
   ) → count  // updates existing row with outcome_class
```

**Why signal upload even when empty:** audit §7 lived problem. The
UploadWorkflow's `check_and_mark_download_complete` needs to run
even for empty batches — otherwise events where all 10 attempts
fail get stuck at `download_complete=false` forever.

**Retry policy (workflow-level):** none — a failed DownloadWorkflow just
becomes one of the 10 attempts that didn't produce videos. Discovery's
loop keeps going.

**Timeout config:**
- Workflow execution timeout: 5 min
- Task timeout: 30s

**Workflow ID convention:** `download-<NN>-<event_uuid>` where NN is
attempt zero-padded. `WorkflowIDReusePolicy: REJECT_DUPLICATE`. This
means the same attempt can't be re-spawned; if discovery loop
re-fires attempt 3, Temporal rejects.

### Workflow 5: `UploadWorkflow`

**Purpose:** Serialized per-event upload queue. Receives batches from
DownloadWorkflows via signals. Runs dedup, S3 upload, share creation,
rank recalculation. Idle-times out after 5 min of no signals; auto-restarts
on next signal via SignalWithStart.

**Trigger:** `SignalWithStartWorkflow` from `DownloadWorkflow` step 7.
Deterministic ID: `upload-<event_uuid>`.

**Signature:**

```go
type UploadWorkflowInput struct {
    EventID    uuid.UUID
    FixtureID  int64
    // Initial batch arrives in the start-signal, not the input struct
}

type UploadBatchSignal struct {
    BatchID       uuid.UUID
    Files         []DownloadedFile  // full metadata + hash
}

type UploadWorkflowOutput struct {
    BatchesProcessed  int
    AssetsCreated     int
    AssetsReused      int
    SharesCreated     int
    RanksRecalculated int
    IdleTimeout       bool
}

func UploadWorkflow(ctx workflow.Context, in UploadWorkflowInput) (*UploadWorkflowOutput, error) {
    signalCh := workflow.GetSignalChannel(ctx, "add_videos")
    var pending []UploadBatchSignal

    for {
        // Wait for signal OR idle timeout OR fixture completion
        selector := workflow.NewSelector(ctx)
        selector.AddReceive(signalCh, func(c workflow.ReceiveChannel, more bool) {
            var batch UploadBatchSignal
            c.Receive(ctx, &batch)
            pending = append(pending, batch)
        })
        selector.AddFuture(workflow.NewTimer(ctx, 5*time.Minute), func(f workflow.Future) {
            // Idle timeout — exit
        })
        selector.Select(ctx)

        if len(pending) == 0 {
            // Idle timeout hit; check completion state and exit
            break
        }

        // Process oldest batch
        batch := pending[0]
        pending = pending[1:]
        processBatch(ctx, in.EventID, batch)
    }

    return &UploadWorkflowOutput{...}, nil
}
```

**Per-batch activity sequence (`processBatch`):**

```
1. activity.CheckEventStillLive(eventID) → bool
   IF false: skip batch (VAR aborted mid-flight)

For each file in batch, in parallel via workflow.Go:
  2. activity.UpsertVideoAssetWithHashDedup(fixtureID, file, hash)
     → (assetID, wasCreated bool)
     // Atomic INSERT ... ON CONFLICT (fixture_id, perceptual_hash)
     // DO UPDATE SET popularity = popularity + 1 RETURNING id, popularity

  IF wasCreated:
    3. activity.UploadFileToS3(assetID, file) → error
    // Move the temp file to canonical S3 key derived from (fixtureID, assetID)
  ELSE:
    // Delete the local temp file — asset already exists in S3
    3. activity.DeleteLocalTempFile(file) → error

  4. activity.MintShareAndRecalculateRanks(assetID, eventID, timestampVerified, extractedMinute)
     → (shareID, computedRank)
     // Single atomic activity. Inside one WithRetryableTx:
     //   (a) locks event's active shares FOR UPDATE
     //   (b) computes the new share's rank from the sort predicate
     //       (timestamp_verified DESC, popularity DESC, file_size_bytes DESC)
     //   (c) INSERTs video_shares row with the real rank (never rank=0 — the
     //       CHECK (rank >= 1) constraint would reject it and there's no
     //       "recompute later" escape hatch, so we compute up front)
     //   (d) UPDATEs any displaced ranks under the partial UNIQUE INDEX
     //       ON (event_id, rank) WHERE state = 'active'
     //   COMMIT. Serialization-failure retries the whole function.

  5. activity.EmitSemanticEvent("event.video_ready", payload) → error
     // Dual-write: INSERT INTO event_log (durable audit + SSE backfill source)
     // then nats.Publish(subject, payload) for realtime fan-out. NATS publish
     // failure does NOT roll back the event_log commit — SSE reconnect covers
     // the drop; the dual_write_skew_total counter fires.

// After all files processed:
6. activity.TryFlagDownloadComplete(eventID, requiredCount=10) → flipped bool
   // Even if this batch was empty; this is the "always signal" path

IF flipped:
  7. activity.EmitSemanticEvent("event.download_complete", payload) → error
```

**Why `MintShareAndRecalculateRanks` is a single atomic activity:** the
schema's `CHECK (rank >= 1)` constraint on `video_shares.rank` is not
deferrable — a two-step "mint at rank=0, recompute after" flow would fail
at INSERT time and step 5 would never run. Merging the two operations into
one transaction lets us compute the correct rank up front against the
event's current set of active shares (all locked FOR UPDATE), then INSERT
with the real rank. The partial UNIQUE INDEX from §3
(`ON (event_id, rank) WHERE state = 'active'`) enforces the invariant
regardless of concurrent runs — the second COMMIT of a racing pair fails
with `SerializationFailure` and `WithRetryableTx` re-executes.

Ranks depend on cross-event popularity that can shift with each new asset
attach, so re-running the sort inside the same transaction keeps the
event's `active` share set consistent per batch.

**Retry policy (workflow-level):** none — SignalWithStart handles
"workflow already exists" cleanly. Idle-timeout is expected behavior.

**Timeout config:**
- Workflow execution timeout: 1 hour (well above idle-timeout)
- Task timeout: 60s

**Signal semantics:**

- Signal name: `"add_videos"`
- Signal payload: `UploadBatchSignal`
- Ordering: FIFO within workflow (Temporal guarantee)
- Backpressure: workflow processes one batch at a time; multiple
  downloads signaling concurrently just queue up

### Activity inventory (by domain package)

Retry policy defaults for all:
```go
DefaultRetryPolicy = &temporal.RetryPolicy{
    InitialInterval:    2 * time.Second,
    BackoffCoefficient: 2.0,
    MaximumInterval:    30 * time.Second,
    MaximumAttempts:    3,
    NonRetryableErrorTypes: []string{
        "ErrNotFound",
        "ErrInvalidTransition",
        "ErrURLMalformed",
        "ErrLLMBadResponse",
        "VideoGeoRestrictedError",
        "VideoNotAvailableError",
        "VideoDeletedError",
    },
}
```

Per-activity overrides shown below.

**`internal/activity/fixture.go`** (calls `domain/fixture`):

| Activity | Input | Output | Timeout | Retry override |
|---|---|---|---|---|
| `FetchFixturesForWindow` | (from, to time.Time) | []APIFixture | 30s | 3 attempts, 2x from 1s |
| `FetchFixturesByIDs` | []int64 | []APIFixture | 30s | 3 attempts |
| `CategorizeAndUpsertFixtures` | []APIFixture | CategorizeOutput | 30s | 3 attempts |
| `PreActivateUpcoming` | lookahead time.Duration | ActivateOutput | 30s | 2 attempts |
| `ActivateFixture` | fixtureID int64 | error | 10s | 2 attempts |
| `TryCompleteFixture` | fixtureID int64 | flipped bool | 10s | 3 attempts, 2x from 1s |
| `CompleteFixture` | fixtureID int64 | error | 10s | 2 attempts |
| `RecordFixturePoll` | *fixture.Fixture | error | 5s | 3 attempts |
| `PruneOldFixtures` | cutoff time.Time | count int | 120s | 2 attempts |

**`internal/activity/event.go`** (calls `domain/event`):

| Activity | Input | Output | Timeout | Retry override |
|---|---|---|---|---|
| `DetectEventChanges` | (fixtureID, []APIEvent) | DetectionResult | 30s | 3 attempts |
| `UpsertEvent` | *Event | (*Event, wasCreated, error) | 10s | 3 attempts, 2x from 1s |
| `RegisterEventMonitorWorkflow` | (eventID, workflowID) | count int | 10s | 5 attempts, 2x from 2s (retry hard) |
| `RegisterEventDownloadWorkflow` | (eventID, workflowID, outcome *string) | count int | 10s | 5 attempts, 2x from 2s |
| `RegisterEventDropWorkflow` | (eventID, workflowID) | count int | 10s | 5 attempts |
| `FlagMonitorComplete` | eventID | flipped bool | 10s | 3 attempts |
| `TryFlagDownloadComplete` | (eventID, required int) | flipped bool | 10s | 3 attempts |
| `MarkEventRemoved` | (eventID, reason string) | error | 10s | 3 attempts |
| `UpdateEventTelemetry` | (eventID, TelemetryPatch) | error | 10s | 2 attempts |
| `CheckEventStillLive` | eventID | bool | 10s | 3 attempts |
| `CountDownloadWorkflowsForEvent` | eventID | int | 10s | 3 attempts |
| `EventsFullyComplete` | fixtureID | bool | 10s | 3 attempts |
| `SaveEventTwitterAliases` | (eventID, aliases []string) | error | 10s | 2 attempts |
| `RegisterEventDiscoveredVideo` | (eventID, DiscoveredVideo) | error | 10s | 3 attempts |

**`internal/activity/discovery.go`** (calls `domain/discovery`):

| Activity | Input | Output | Timeout | Retry override |
|---|---|---|---|---|
| `SearchTwitter` | SearchRequest | SearchResponse | 60s | 3 attempts, 1.5x from 10s. Non-retryable: ErrTwitterAuthRequired |

**`internal/activity/download.go`** (composes `domain/vision` + `infra/twitter-syndication`):

| Activity | Input | Output | Timeout | Retry override |
|---|---|---|---|---|
| `DownloadVideo` | video URL | DownloadedFile | 90s | 3 attempts, 2x from 2s. Non-retryable: geo/deleted/notavailable |
| `DedupBatchByMD5` | []DownloadedFile | []DownloadedFile | 30s | 2 attempts |
| `ValidateVideoIsSoccer` | (file, apiElapsed, apiExtra) | ValidationResult | 90s | 4 attempts, 2x from 3s. Non-retryable: ErrLLMBadResponse |
| `GenerateVideoHash` | file | dHashSamples | 60s heartbeat | 2 attempts, heartbeat every 5 frames |
| `SignalUploadBatch` | (eventID, batch) | error | 60s | 3 attempts |
| `DeleteLocalTempFile` | file | error | 30s | 2 attempts |

**`internal/activity/upload.go`** (calls `domain/video`):

| Activity | Input | Output | Timeout | Retry override |
|---|---|---|---|---|
| `UpsertVideoAssetWithHashDedup` | (fixtureID, file, hash) | (assetID, wasCreated) | 30s | 3 attempts |
| `UploadFileToS3` | (assetID, file) | error | 60s | 3 attempts, 2x from 2s |
| `MintShareAndRecalculateRanks` | (assetID, eventID, verified, minute) | (shareID, computedRank) | 30s | 3 attempts (serialization retry inside txn) |
| `MarkShareRemoved` | (shareID, reason) | error | 10s | 3 attempts |

**`internal/activity/alias.go`** (calls `domain/alias`):

| Activity | Input | Output | Timeout | Retry override |
|---|---|---|---|---|
| `GetOrResolveTeamAliases` | teamID | []string | 60s | 3 attempts, 2x from 2s |
| `PreCacheAliasesBatch` | []TeamRef | (success, failed int) | 5 min | 2 attempts (each team is independent) |
| `ListUniqueTeamsFromFixtures` | []APIFixture | []TeamRef | 10s | 2 attempts |

**`internal/activity/session.go`** (calls `domain/session`):

Twitter containers own most session state via HTTP endpoints; the worker
only needs a few activities for observability + operational alerts.

| Activity | Input | Output | Timeout | Retry override |
|---|---|---|---|---|
| `AggregateFleetHealth` | []instanceURL | FleetHealth | 30s | 2 attempts (best-effort) |
| `AlertIfSessionStale` | (via ShouldWarnStale) | error | 10s | 2 attempts |

**`internal/activity/textanalysis.go`** (stubbed until domain ships):

| Activity | Input | Output | Timeout | Retry override |
|---|---|---|---|---|
| `AnalyzeTweetIntent` | (assetID, tweetURL, text, author) | *Intent | 60s | 3 attempts, 2x from 2s. STUBBED. |

**`internal/activity/eventlog.go`** (SSE fan-out + webhook trigger):

| Activity | Input | Output | Timeout | Retry override |
|---|---|---|---|---|
| `NotifyEventLog` | (eventType, payload) | error | 5s | 2 attempts. Best-effort — SSE is not durable. |
| `PublishFrontendRefresh` | (nil) | error | 5s | 1 attempt. Best-effort. |

Total: ~30 activities across 8 files. Down from Python's 42.

### Error taxonomy for retry classification

Errors surface at three layers:

1. **Domain errors** (from §4) — service-layer typed errors. Retry
   eligibility is documented on each `Err*` variable.
2. **Infrastructure errors** (§9) — client-layer wrapped errors
   (`ErrLLMUnavailable`, `ErrPGConnectionLost`, `ErrS3AccessDenied`, etc.).
   Retry eligibility inherited from the underlying transport class.
3. **Temporal errors** — wrapping of the above at activity boundaries.
   Workflow's error handler classifies via `underlying_error_class` field
   (audit §7 telemetry contract).

Every activity that wraps a service call classifies its returned error
into one of these `error_class` values for telemetry:

| Class | Retry-eligible? | Examples |
|---|---|---|
| `not_found` | no | event/fixture/asset not in DB |
| `invalid_input` | no | ErrURLMalformed, ErrInvalidTransition |
| `invalid_state` | no | event already removed |
| `transient_infra` | yes | ErrPGConnectionLost, ErrS3Timeout |
| `llm_unavailable` | yes | ErrLLMUnavailable, ErrLLMTimeout |
| `llm_cap_exceeded` | yes (longer backoff) | ErrLLMCapExceeded |
| `llm_bad_response` | no | ErrLLMBadResponse |
| `twitter_unreachable` | yes | ErrTwitterUnreachable |
| `twitter_auth` | no | ErrTwitterAuthRequired (alert!) |
| `twitter_search_timeout` | yes | ErrTwitterSearchTimeout |
| `video_download_failed` | conditional | see subclasses below |
| `video_geo_restricted` | no (retry-eligible-per-proxy when §11 lands) | VideoGeoRestrictedError |
| `video_not_available` | no | VideoNotAvailableError |
| `video_deleted` | no | VideoDeletedError |
| `unknown` | yes (default; investigate) | uncategorized exception |

The activity records `error_class` in the event's `Telemetry`
(`event.download_failure_classes` map) via `UpdateEventTelemetry`. This
is how post-fixture summaries answer "why did this event fail to
capture" without Loki archaeology.

### Concurrency guardrails

**LLM cap enforcement:** `ValidateVideoIsSoccer` and `GetOrResolveTeamAliases`
both call the LLM endpoint. Global concurrency is limited by:
- Activity-level: `MaxConcurrentActivityExecutions: 2` on the worker
  registration for LLM-bearing activities (see §9 registration section).
- Endpoint-level: joi's hard cap (currently 2). When nexus lands, its
  own limit will replace this.

**Per-instance twitter search cap:** `SearchTwitter` activity's HTTP
call has a 60s timeout. The twitter container itself handles the per-
Firefox-instance serialization (one search at a time per instance).

**Postgres connection pool:** Configured in `infra/pg` (§9). Default
pool size: 25 per worker container. Activities are short; contention
should be minimal.

**S3 client:** `aws-sdk-go-v2` default transport pool.

### Testing shape

**Workflow tests** use `temporaltest.NewTestSuiteInstance()` (from
`go.temporal.io/sdk/testsuite`). Mock all activity calls; assert the
right activities were called in the right order with the right args.

```go
func TestMonitorWorkflow_NewEvent_TriggersDiscovery(t *testing.T) {
    ts := &testsuite.WorkflowTestSuite{}
    env := ts.NewTestWorkflowEnvironment()

    env.OnActivity(activity.ListActiveFixtureIDs, mock.Anything).Return([]int64{5000}, nil)
    env.OnActivity(activity.FetchFixturesByIDs, mock.Anything, []int64{5000}).Return(...)
    env.OnActivity(activity.DetectEventChanges, mock.Anything, ...).Return(DetectionResult{
        NewEvents: []Event{newGoalEvent},
    }, nil)
    env.OnActivity(activity.RegisterEventMonitorWorkflow, mock.Anything, ...).Return(3, nil)
    env.OnActivity(activity.FlagMonitorComplete, mock.Anything, newGoalEvent.ID).Return(true, nil)

    // Assert DiscoveryWorkflow spawned
    env.OnWorkflow(DiscoveryWorkflow, mock.Anything, mock.Anything).Return(&DiscoveryOutput{}, nil)

    env.ExecuteWorkflow(MonitorWorkflow, MonitorWorkflowInput{})

    require.True(t, env.IsWorkflowCompleted())
    require.NoError(t, env.GetWorkflowError())

    var out MonitorWorkflowOutput
    require.NoError(t, env.GetWorkflowResult(&out))
    require.Equal(t, 1, out.DiscoveriesTriggered)
}
```

Test naming convention: `Test<WorkflowName>_<Scenario>_<Assertion>`.

**Activity tests** are unit tests of the service composition — mocked
service, real activity function. Fast.

**Integration tests** live at `test/synthetic/` (audit §12 Tier 3
harness). Runs whole match scenarios end-to-end against a real
Postgres/Garage/Temporal in docker-compose.

### Workflow ID collision handling

Every workflow ID uses `WorkflowIDReusePolicy: REJECT_DUPLICATE`. This
means if code tries to spawn a workflow with an ID that already exists
(running or completed), Temporal returns `WorkflowExecutionAlreadyStarted`.

For `discovery-<event_uuid>` and `download-<NN>-<event_uuid>`: the
rejection is *load-bearing*. It's how we prevent duplicate spawns from
buggy monitor cycles. Handle by logging + continuing.

For `upload-<event_uuid>`: we use `SignalWithStartWorkflow` which
implicitly allows "workflow already exists" (starts if not; signals if
so). This is the exception to the REJECT_DUPLICATE rule and it's why
UploadWorkflow uses `WorkflowIDReusePolicy: ALLOW_DUPLICATE` — a
completed UploadWorkflow that has idle-timed-out CAN be restarted by
a fresh signal from a late-arriving DownloadWorkflow.

### Extensibility hook

Adding a new workflow follows this pattern:

1. Define input/output structs in `internal/workflow/<name>.go`.
2. Define workflow function with `func (ctx workflow.Context, in Input) (*Output, error)`.
3. Compose activity calls; encode retry/timeout policies inline.
4. Register in `cmd/worker/main.go` (one `w.RegisterWorkflow(NewFooWorkflow)` line).
5. Add workflow-level tests using `testsuite.WorkflowTestSuite`.
6. If it's schedule-driven, register the schedule alongside workflow.
7. If it spawns children, use ABANDON policy for fire-and-forget.

Adding a new activity follows this pattern:

1. Define input/output structs colocated with the activity function.
2. Function signature: `func FooActivity(ctx context.Context, in FooInput) (*FooOutput, error)`.
3. Body composes 1 or more domain service calls + returns typed error.
4. Register in `cmd/worker/main.go` (one `w.RegisterActivity(FooActivity)` line).
5. Add activity-level tests: unit tests with mocked services.
6. Add to the retry-classification table above if it introduces a new
   error class.

The concrete example the textanalysis domain hook was aiming at:

`activity.AnalyzeTweetIntent(assetID, tweetURL, text, author) → *Intent`.
Wire it into `UploadWorkflow` step 4 (after `MintShareAndRecalculateRanks`).
Zero workflow rewrites. Zero cross-cutting refactor. The layering handles it.

---

## 9. Infrastructure adapters

Nine adapters in `internal/infra/`. Each wraps an external system
(database, cache, HTTP service, subprocess, external API) behind a Go
interface that the domain services from §4 and activities from §5
depend on. This is the "we talk to the outside world" boundary; every
crossing is typed, timeout-bounded, and error-classified.

### Design principles

**Every adapter defines an interface, ships a real implementation, and
ships a fake for testing.** Domain services and activities depend on the
interface, not the concrete type. `internal/testutil/fakes/` provides
in-memory fakes for unit tests; integration tests use testcontainers-go
where a real backend is required (Postgres, Garage).

**Every method has an explicit timeout.** Contexts are respected; if a
caller cancels, the adapter surfaces `context.Canceled` and cleans up
in-flight resources (subprocesses, connections). Default timeouts are
documented per adapter; callers can override via `ctx` deadlines.

**Every error is typed.** Wrapped underlying errors carry through via
`errors.Is` / `errors.As` semantics. Retry classification lives in the
adapter (which knows whether "connection refused" means "try again in
2s" or "give up"), not in the caller. This is what makes §5's activity
retry-policy overrides possible — the activity trusts the adapter's
error class.

**Every adapter has an observability hook.** Structured JSON log lines
(module + action + fields) on every call boundary. Prometheus counters
for calls, errors, and latency histograms. Loki-queryable per audit §9.

**Config-driven, not code-driven.** Every adapter reads its config from
`internal/config` (Pydantic-equivalent: `envconfig` package parses env
vars into typed structs). No hard-coded URLs, credentials, or timeouts.

**Idempotent where possible.** POST-heavy adapters (e.g., `NotifyEventLog`
signals) include idempotency keys where the receiver supports them. GET
paths are inherently idempotent. State-mutating operations that aren't
naturally idempotent (Garage PUT with content-addressed keys IS
idempotent; Twitter service `/search` isn't) get retry classifications
that reflect that.

### Adapter inventory

| Package | Purpose | Complexity |
|---|---|---|
| `internal/infra/pg` | Postgres connection pool + transaction helpers | High |
| `internal/infra/nats` | Workspace NATS client (publish + subscribe + JetStream) | Medium |
| `internal/infra/s3` | Garage/S3 client + presigned URLs + streaming upload | Medium |
| `internal/infra/llm` | LLM endpoint client (config-swappable joi/nexus) | High |
| `internal/infra/temporal` | Temporal client construction + shared config | Low |
| `internal/infra/apifootball` | API-Football REST client + rate limiting | Medium |
| `internal/infra/twitter` | HTTP client to the twitter container (search service) | Medium |
| `internal/infra/syndication` | Twitter syndication API client (video downloads) | Medium |
| `internal/infra/ffmpeg` | ffmpeg CLI subprocess wrapper + probe | Medium |
| `internal/infra/wikidata` | Wikidata SPARQL client (RAG for team aliases) | Low |

Total: **ten** adapters. Each gets a full spec below.

**Note on `internal/infra/pg`:** the earlier draft had this adapter carrying
LISTEN/NOTIFY methods (`Listen`, `Notify`) as the event fan-out mechanism.
Per the top-of-doc revision (workspace NATS decision), those responsibilities
move to `internal/infra/nats`. The pg adapter retains only pool + transaction
helpers. The `Listen` / `Notify` methods shown in the §pg spec below are
historical; they do NOT ship in the real adapter.

### `internal/infra/pg`

Postgres connection pool + LISTEN/NOTIFY plumbing + transaction helpers.
The most-called adapter — every domain store depends on it.

**Client interface:**

```go
package pg

import (
    "context"
    "time"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"
)

type Pool interface {
    // Basic operations — pass through to pgx with observability wrapping
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
    Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)

    // Transaction helpers
    WithTx(ctx context.Context, fn func(pgx.Tx) error) error
    WithRetryableTx(ctx context.Context, fn func(pgx.Tx) error) error

    // LISTEN/NOTIFY for SSE fan-out
    Listen(ctx context.Context, channel string) (<-chan *pgconn.Notification, error)
    Notify(ctx context.Context, channel string, payload string) error

    // Lifecycle
    Ping(ctx context.Context) error
    Close()
}
```

**Real implementation** wraps `*pgxpool.Pool`. Constructor reads pool
config from env vars via `internal/config`:

```go
type Config struct {
    DSN                string        `env:"PG_DSN,required"`
    MaxConns           int32         `env:"PG_MAX_CONNS" envDefault:"25"`
    MinConns           int32         `env:"PG_MIN_CONNS" envDefault:"5"`
    MaxConnLifetime    time.Duration `env:"PG_MAX_CONN_LIFETIME" envDefault:"1h"`
    MaxConnIdleTime    time.Duration `env:"PG_MAX_CONN_IDLE" envDefault:"15m"`
    HealthCheckPeriod  time.Duration `env:"PG_HEALTH_CHECK_PERIOD" envDefault:"1m"`
    ConnectTimeout     time.Duration `env:"PG_CONNECT_TIMEOUT" envDefault:"5s"`
}

func New(ctx context.Context, cfg Config, logger *slog.Logger) (Pool, error)
```

**Transaction helpers:**

```go
// WithTx executes fn inside a BEGIN/COMMIT boundary. If fn returns an
// error, the transaction is rolled back. If fn panics, the transaction
// is rolled back and the panic is re-raised.
//
// Isolation level: pgx.ReadCommitted (Postgres default).
func (p *pgxPool) WithTx(ctx context.Context, fn func(pgx.Tx) error) error

// WithRetryableTx executes fn inside a REPEATABLE READ transaction with
// automatic retry on serialization-failure (SQLSTATE 40001) up to 3
// attempts with exponential backoff. Used by any transactional operation
// that might race with concurrent writers — notably
// video.MintShareAndRecalculate (§4).
//
// Returns ErrSerializationFailedAfterRetries if all 3 attempts fail;
// caller should escalate to human observation.
func (p *pgxPool) WithRetryableTx(ctx context.Context, fn func(pgx.Tx) error) error
```

**LISTEN/NOTIFY for SSE fan-out (audit §11 + §5):**

```go
// Listen acquires a dedicated connection from the pool for LISTEN commands.
// Returns a receive-only channel of notifications until ctx is canceled.
// Reconnects transparently on connection loss.
//
// Used by internal/api SSE handlers to receive event_log updates.
func (p *pgxPool) Listen(ctx context.Context, channel string) (<-chan *pgconn.Notification, error)

// Notify emits a NOTIFY on the given channel with the payload. Called by
// activities.NotifyEventLog after INSERT INTO event_log succeeds.
// Payload is arbitrary text (JSON blob typically); Postgres NOTIFY has
// a per-payload size limit of 8KB.
func (p *pgxPool) Notify(ctx context.Context, channel string, payload string) error
```

**Typed errors:**

```go
var (
    ErrConnectionLost              = errors.New("pg: connection lost")
    ErrConnectionTimeout           = errors.New("pg: connection timeout")
    ErrSerializationFailure        = errors.New("pg: serialization failure")
    ErrSerializationFailedAfterRetries = errors.New("pg: serialization failure after retries")
    ErrDuplicateKey                = errors.New("pg: duplicate key violation")
    ErrForeignKeyViolation         = errors.New("pg: foreign key violation")
    ErrCheckConstraintViolation    = errors.New("pg: check constraint violation")
    ErrNotifyChannelClosed         = errors.New("pg: notify channel closed")
    ErrPoolExhausted               = errors.New("pg: pool exhausted")
)

// ClassifyError takes any error returned from pgx and returns the typed
// wrapper. Handles pgx.PgError SQLSTATE codes; falls through to
// underlying error type otherwise.
func ClassifyError(err error) error
```

**Retry classification:**
- `ErrConnectionLost`, `ErrConnectionTimeout` → retry-eligible (transient)
- `ErrPoolExhausted` → retry-eligible with backoff
- `ErrSerializationFailure` → retry-eligible (WithRetryableTx handles this internally)
- `ErrDuplicateKey` → NOT retry-eligible (caller decides — often means "someone beat us, look up existing")
- `ErrForeignKeyViolation`, `ErrCheckConstraintViolation` → NOT retry-eligible (bug in the caller)

**Lifecycle:**

- Construction: `pg.New(ctx, cfg, logger)` builds the pool and pings.
- Health: `pool.Ping(ctx)` used by `/healthz` handlers in each binary.
- Shutdown: `pool.Close()` on SIGTERM. Drains in-flight queries with a
  configurable grace period (default 30s).

**Migration handling:**

Migrations aren't part of the runtime interface. `cmd/worker/main.go`
and `cmd/api/main.go` call `pg.RunMigrations(ctx, cfg, migrationsFS)`
at startup, before constructing the pool. This is idempotent — safe to
re-run. Uses `golang-migrate/migrate` under the hood.

```go
// RunMigrations applies all pending migrations from the embedded migrations/
// directory. Safe to call at every startup. Blocks until complete.
func RunMigrations(ctx context.Context, cfg Config, migrationsFS embed.FS) error
```

**Observability:** every `Query`/`Exec`/`WithTx` call emits a structured
log line + a Prometheus histogram observation. Slow queries (> 500ms)
get flagged at WARN level with the SQL + args.

**Testing:**

- Unit tests use `internal/testutil/fakes/pg.FakePool` — an in-memory
  implementation of the `Pool` interface backed by hash maps. Fast, no
  Docker, no migrations. Good for testing store logic in isolation.
- Integration tests use testcontainers-go to spin up a real Postgres
  container with migrations applied. Runs in CI.

### `internal/infra/nats`

Workspace NATS client — the async event bus. Publishes semantic events
(§11) and consumes them for SSE fan-out and webhook delivery (§8).
Replaces the Postgres LISTEN/NOTIFY mechanism from the earlier draft.

**Client interface:**

```go
package nats

import (
    "context"
    "time"

    "github.com/nats-io/nats.go"
    "github.com/nats-io/nats.go/jetstream"
)

type Client interface {
    // Core NATS — transient at-most-once delivery.
    // Used by SSE fan-out where the browser reconnects and backfills from
    // event_log if it missed anything.
    Publish(ctx context.Context, subject string, payload []byte) error
    Subscribe(ctx context.Context, subject string) (<-chan Msg, error)

    // JetStream — durable at-least-once delivery with replay.
    // Used by webhook delivery: each subscriber has a durable consumer;
    // NATS holds the message until the consumer acks.
    PublishStream(ctx context.Context, subject string, payload []byte) (*jetstream.PubAck, error)
    Consume(ctx context.Context, streamName, consumerName string) (<-chan JetStreamMsg, error)

    // Lifecycle
    Ping(ctx context.Context) error
    Close()
}

type Msg struct {
    Subject string
    Data    []byte
    Header  nats.Header
}

type JetStreamMsg struct {
    Msg
    Ack   func() error
    Nak   func() error
    Term  func() error  // Poison-message: don't retry
}
```

**Real implementation** wraps `nats.Conn` + `jetstream.JetStream`.
Constructor reads config from env vars via `internal/config`:

```go
type Config struct {
    URL         string        `env:"NATS_URL,required"`         // nats://nats:4222
    Account     string        `env:"NATS_ACCOUNT" envDefault:"found-footy"`
    CredsFile   string        `env:"NATS_CREDS_FILE"`           // Path to .creds file (JWT + nkey)
    Name        string        `env:"NATS_CLIENT_NAME" envDefault:"found-footy-worker"`

    // Connection resilience
    ReconnectWait     time.Duration `env:"NATS_RECONNECT_WAIT" envDefault:"2s"`
    MaxReconnects     int           `env:"NATS_MAX_RECONNECTS" envDefault:"-1"` // Retry forever
    PingInterval      time.Duration `env:"NATS_PING_INTERVAL" envDefault:"20s"`

    // JetStream domain (typically empty; overridden if using leaf nodes)
    JetStreamDomain   string        `env:"NATS_JS_DOMAIN"`
}

func New(ctx context.Context, cfg Config, logger *slog.Logger) (Client, error)
```

**Subject scheme (found-footy account):**

Within the found-footy NATS account, subjects use dot-separated hierarchy:

```
event.detected              // event enters pipeline
event.stable                // debounce threshold reached
event.video_ready           // first S3 upload complete
event.rank_recalculated     // ranks changed
event.removed               // VAR'd off
event.download_complete     // 10-attempt loop terminated
fixture.activated           // moved staging → active
fixture.completed           // match final + all events done
```

Cross-account subscribers (e.g., vedanta-systems) reference these as
`found-footy.event.>` — the account prefix is applied at the broker level.

**Publish semantics:**

- `Publish` = core NATS. Fire-and-forget; broker drops if no subscriber.
  Used for SSE fan-out because browsers reconnect + backfill from
  `event_log` audit table on drop.
- `PublishStream` = JetStream. Broker persists to disk until every
  durable consumer has acked. Used for webhook delivery where at-least-
  once with automatic retry is required.

**Consumer semantics:**

- `Subscribe` returns a channel of transient messages. Caller iterates and
  processes; drops on ctx cancel or subscription tear-down.
- `Consume` returns a channel of durable messages with explicit ack. Each
  consumer name is a shared durable consumer; multiple worker replicas
  competing on the same consumer name split load automatically (JetStream
  push consumers).

**Durability + persistence:**

Every semantic event is BOTH INSERTed into `event_log` AND published to
NATS in the same activity boundary. The pattern is:

```go
func PublishEvent(ctx context.Context, ev SemanticEvent) error {
    if err := store.InsertEventLog(ctx, ev); err != nil {
        return err  // Durable write must succeed first.
    }
    subject := "event." + ev.Kind  // e.g., "event.video_ready"
    return nats.Publish(ctx, subject, ev.MarshalJSON())
    // NATS publish failure logs a warning but does NOT roll back the
    // event_log insert. SSE clients that reconnect will see the event via
    // backfill from event_log. Webhook clients (JetStream) get catch-up
    // via a background worker that scans event_log for events not yet
    // in JetStream.
}
```

**Error classification:**

- `ErrConnectionLost` — NATS connection dropped. Client is auto-reconnecting;
  publishes fail until reconnect. Callers should log + continue (audit
  trail is preserved in `event_log`).
- `ErrPublishTimeout` — synchronous JetStream publish exceeded 5s.
  Callers may retry; message may or may not have landed.
- `ErrSubjectInvalid` — configuration error, not runtime.
- `ErrJetStreamNotEnabled` — misconfiguration; account lacks JetStream
  permissions.

**Testing:**

- Unit tests use `internal/testutil/fakes/nats.FakeClient` — in-memory
  pub/sub with configurable delivery guarantees (drop / duplicate /
  reorder) for exercising consumer edge cases.
- Integration tests use testcontainers-go with `nats:2.10-alpine`
  + JetStream enabled. The workspace NATS at `~/workspace/nats/` is NOT
  reused for tests — each test suite gets a fresh container.

### `internal/infra/s3`

Garage / S3-compatible client. Uploads video files, generates presigned
URLs for the share-id redirect endpoint (audit §11).

**Client interface:**

```go
package s3

type Client interface {
    // Upload streams a local file to a bucket/key. content_type is
    // auto-detected from file extension if empty.
    Upload(ctx context.Context, bucket, key string, filePath, contentType string) error

    // UploadReader streams from an io.Reader with known size. Used by
    // video-download flow where we're streaming from HTTP → S3 without
    // touching local disk.
    UploadReader(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error

    // Delete removes an object. Idempotent (no-op if missing).
    Delete(ctx context.Context, bucket, key string) error

    // Head returns metadata for an object without downloading its body.
    Head(ctx context.Context, bucket, key string) (*ObjectMetadata, error)

    // PresignedGetURL returns a time-limited public URL for the object.
    // Used by the share-id redirect endpoint (§4). Default expiry: 1 hour.
    PresignedGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error)

    // ListPrefix returns keys under a prefix, paginated.
    ListPrefix(ctx context.Context, bucket, prefix string, limit int) ([]string, error)

    // Ping confirms bucket accessibility (used by /healthz).
    Ping(ctx context.Context, bucket string) error
}

type ObjectMetadata struct {
    ContentType   string
    ContentLength int64
    ETag          string
    LastModified  time.Time
}
```

**Configuration:**

```go
type Config struct {
    Endpoint        string `env:"S3_ENDPOINT,required"`     // e.g. "http://garage:3900"
    Region          string `env:"S3_REGION" envDefault:"us-east-1"`
    AccessKeyID     string `env:"S3_ACCESS_KEY_ID,required"`
    SecretAccessKey string `env:"S3_SECRET_ACCESS_KEY,required"`
    UsePathStyle    bool   `env:"S3_USE_PATH_STYLE" envDefault:"true"`  // required for Garage
    Bucket          string `env:"S3_BUCKET,required"`
}
```

**Real implementation** wraps `aws-sdk-go-v2/service/s3`. `UsePathStyle: true`
is critical for Garage (and MinIO); Garage doesn't support virtual-hosted-
style URLs.

**Typed errors:**

```go
var (
    ErrObjectNotFound      = errors.New("s3: object not found")
    ErrBucketNotFound      = errors.New("s3: bucket not found")
    ErrAccessDenied        = errors.New("s3: access denied")
    ErrTimeout             = errors.New("s3: request timeout")
    ErrUnreachable         = errors.New("s3: endpoint unreachable")
    ErrInvalidCredentials  = errors.New("s3: invalid credentials")
    ErrChecksumMismatch    = errors.New("s3: uploaded content checksum mismatch")
)
```

**Retry classification:**
- `ErrTimeout`, `ErrUnreachable` → retry-eligible (transient network)
- `ErrObjectNotFound`, `ErrAccessDenied`, `ErrInvalidCredentials` → NOT retry-eligible (config problem or genuine miss)

**Testing:**

- Fake at `internal/testutil/fakes/s3.FakeClient` — backed by
  `map[string][]byte`. Supports all Client methods; presigned URLs
  return `fake://` scheme so tests can assert against them.
- Integration tests use testcontainers-go with a Garage container.

### `internal/infra/llm`

The **config-swappable LLM endpoint client**. The one adapter that
codifies the joi-today-nexus-tomorrow invariant from
[`decisions.md`](./decisions.md) 2026-07-01. All LLM calls go through
this client; endpoint URL is one env var.

**Client interface:**

```go
package llm

type Client interface {
    // ChatCompletion sends a chat-completion request. Returns the assistant's
    // response text + typed token usage. Configuration lives in the request:
    // model, temperature, max_tokens, response_format.
    ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error)

    // ChatCompletionMultiImage is a specialization for vision calls that
    // sends multiple images in one request. Returns the assistant's
    // response text (typically structured JSON that callers parse).
    // The 2-image / 3-image strategy from vision domain calls this with
    // len(images) == 2 or 3.
    ChatCompletionMultiImage(ctx context.Context, req MultiImageRequest) (*ChatResponse, error)

    // Embedding returns a vector representation of the input text.
    // Used by textanalysis domain's semantic similarity + storage in
    // pgvector columns from §3.
    Embedding(ctx context.Context, req EmbeddingRequest) (*EmbeddingResponse, error)

    // ListModels queries /v1/models on the endpoint. Called once at
    // startup + on demand to discover live model IDs (audit §6 nexus
    // swap invariant: model IDs come from the endpoint, not hard-coded).
    ListModels(ctx context.Context) ([]ModelInfo, error)

    // Ping confirms endpoint reachability (used by /healthz).
    Ping(ctx context.Context) error
}

type ChatRequest struct {
    Model          string          `json:"model"`
    Messages       []ChatMessage   `json:"messages"`
    Temperature    float32         `json:"temperature"`
    MaxTokens      int             `json:"max_tokens"`
    ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type ResponseFormat struct {
    Type string `json:"type"`  // "text" | "json_object"
}

type MultiImageRequest struct {
    Model       string          `json:"model"`
    ImagesJPEG  [][]byte        // raw bytes; adapter base64-encodes for JSON transport
    Prompt      string
    Temperature float32
    MaxTokens   int
    ResponseFormat *ResponseFormat
}

type ChatResponse struct {
    Content       string
    Usage         Usage
    ModelUsed     string
    LatencyMs     int
}

type EmbeddingRequest struct {
    Model string   `json:"model"`
    Input []string `json:"input"`  // one or more strings
}

type EmbeddingResponse struct {
    Embeddings [][]float32
    Usage      Usage
    ModelUsed  string
}

type Usage struct {
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
}

type ModelInfo struct {
    ID          string
    OwnedBy     string
    ContextSize int
}
```

**Configuration:**

```go
type Config struct {
    EndpointURL    string        `env:"LLM_ENDPOINT_URL,required"`  // "http://llama-small.joi" today; nexus URL later
    APIKey         string        `env:"LLM_API_KEY" envDefault:"not-required"`  // llama.cpp ignores; kept for OpenAI-compat
    DefaultTimeout time.Duration `env:"LLM_DEFAULT_TIMEOUT" envDefault:"60s"`
    ChatModel      string        `env:"LLM_CHAT_MODEL" envDefault:""`   // empty = discover from /v1/models
    EmbeddingModel string        `env:"LLM_EMBEDDING_MODEL" envDefault:""`
    MaxRetries     int           `env:"LLM_MAX_RETRIES" envDefault:"3"`
}
```

**The swap invariant.** When nexus lands, `LLM_ENDPOINT_URL` changes.
Application code, service methods, activities — all unchanged. The
adapter transparently talks to whichever endpoint the env var points at.

**Real implementation** uses `github.com/openai/openai-go` (Anthropic's
official Go OpenAI SDK compatible with any OpenAI-shaped API — which
llama.cpp is, and nexus will be).

**Typed errors:**

```go
var (
    ErrUnavailable       = errors.New("llm: endpoint unavailable")
    ErrTimeout           = errors.New("llm: request timeout")
    ErrCapExceeded       = errors.New("llm: concurrent-cap exceeded (server 503)")
    ErrBadResponse       = errors.New("llm: response not parseable")
    ErrModelNotFound     = errors.New("llm: requested model not available")
    ErrContextTooLong    = errors.New("llm: input exceeds model context")
    ErrRateLimited       = errors.New("llm: rate limit hit (server 429)")
    ErrInvalidJSON       = errors.New("llm: response_format=json_object but content not valid JSON")
)
```

**Retry classification:**
- `ErrUnavailable`, `ErrTimeout` → retry-eligible with exponential backoff
- `ErrCapExceeded`, `ErrRateLimited` → retry-eligible with LONGER backoff (respect Retry-After header if present)
- `ErrBadResponse`, `ErrInvalidJSON` → NOT retry-eligible (log for prompt-engineering work)
- `ErrModelNotFound`, `ErrContextTooLong` → NOT retry-eligible (config/input problem)

**Observability:** every call records model, prompt length, response
length, latency. Structured logs at INFO for success, WARN for
retry-eligible errors, ERROR for non-retry-eligible.

**Testing:**

- `internal/testutil/fakes/llm.FakeClient` allows registering canned
  responses per request-hash. Vision-domain tests register a
  `SOCCER: yes` response for a specific image hash and assert the
  service returns Verdict=Accepted.
- No real-integration tests against joi in CI (joi isn't in CI); the
  fake covers behavior. Real joi calls happen in dev via
  `scripts/manual_llm_probe.sh` when we want to sanity-check the
  contract against actual model outputs.

### `internal/infra/temporal`

Temporal client construction + shared config. Small; almost pure config.

**Client interface:**

```go
package temporal

type Config struct {
    HostPort      string        `env:"TEMPORAL_HOSTPORT,required"`  // e.g. "temporal:7233"
    Namespace     string        `env:"TEMPORAL_NAMESPACE" envDefault:"default"`
    TaskQueue     string        `env:"TEMPORAL_TASK_QUEUE" envDefault:"found-footy"`
    ConnectTimeout time.Duration `env:"TEMPORAL_CONNECT_TIMEOUT" envDefault:"30s"`
}

// NewClient constructs a Temporal client. Retries connection up to 5
// times with exponential backoff before giving up.
func NewClient(ctx context.Context, cfg Config, logger *slog.Logger) (client.Client, error)

// NewWorker constructs a worker for the given task queue with sensible
// defaults for found-footy: MaxConcurrentActivityExecutions:30,
// MaxConcurrentWorkflowTasks:10. Activities that need lower concurrency
// (LLM-bearing) are overridden via activity registration options.
func NewWorker(c client.Client, cfg Config, logger *slog.Logger) worker.Worker

// DefaultRetryPolicy returns the retry policy shared by most activities.
// Individual activities override in their ActivityOptions.
func DefaultRetryPolicy() *temporal.RetryPolicy
```

**Typed errors:** Temporal SDK errors are surfaced as-is. This adapter
doesn't wrap them because the Temporal SDK's error types are already
usable via `errors.As`.

**Testing:** Temporal test framework (`testsuite.WorkflowTestSuite`)
handles this at the workflow-test layer. This adapter has no
non-trivial logic to unit-test.

### `internal/infra/apifootball`

API-Football REST client. Rate-limited (10 req/sec free tier, 300
req/min Pro). Timeout-bounded. Response-shape typed.

**Client interface:**

```go
package apifootball

type Client interface {
    // FetchFixturesForDate returns fixtures on the given UTC date for
    // any of the tracked leagues (top-5 European + FIFA nationals). Set
    // by IngestWorkflow for the 3-day window fetch.
    FetchFixturesForDate(ctx context.Context, date time.Time) ([]Fixture, error)

    // FetchFixturesByIDs is a batch fetch — up to 20 IDs per call.
    // Used by MonitorWorkflow for active-fixture polling.
    FetchFixturesByIDs(ctx context.Context, ids []int64) ([]Fixture, error)

    // FetchTeamInfo returns team metadata (name, national flag, country,
    // city, venue). Used by alias domain during RAG.
    FetchTeamInfo(ctx context.Context, teamID int) (*TeamInfo, error)

    // FetchTeamsInLeague returns all teams for a given league+season.
    // Used at ingest-time to build the tracked-team set.
    FetchTeamsInLeague(ctx context.Context, leagueID, season int) ([]Team, error)

    // CurrentSeasonForLeague queries the /leagues endpoint to discover
    // the current season for a league. Fallback: use latest available.
    CurrentSeasonForLeague(ctx context.Context, leagueID int) (int, error)

    // QuotaStatus returns remaining daily quota (parsed from response
    // headers). Used for observability + circuit-breaking near limits.
    QuotaStatus(ctx context.Context) (*Quota, error)

    // Ping confirms endpoint reachability + valid API key.
    Ping(ctx context.Context) error
}

// Response types match the API-Football schema (documented at
// https://www.api-football.com/documentation-v3). Full field
// definitions in models.go — this list shows the top-level shapes.
type Fixture struct {
    ID       int64
    Date     time.Time
    Status   FixtureStatus
    Teams    Teams
    League   League
    Score    Score
    Events   []APIEvent
}

type FixtureStatus struct {
    Short   string  // "NS", "1H", "FT", etc.
    Long    string
    Elapsed *int
    Extra   *int
}

type APIEvent struct {
    Time    EventTime
    Team    Team
    Player  Player   // may have nil ID+Name early in match
    Type    string   // "Goal", "Card", "Subst", "Var"
    Detail  string
}

// (rest of response shapes omitted here — fully defined in models.go)
```

**Configuration:**

```go
type Config struct {
    APIKey           string        `env:"API_FOOTBALL_KEY,required"`
    BaseURL          string        `env:"API_FOOTBALL_BASE_URL" envDefault:"https://v3.football.api-sports.io"`
    RateLimit        int           `env:"API_FOOTBALL_RATE_LIMIT" envDefault:"300"`  // per minute
    RequestTimeout   time.Duration `env:"API_FOOTBALL_TIMEOUT" envDefault:"30s"`
    TrackedLeagues   []int         `env:"API_FOOTBALL_TRACKED_LEAGUES" envDefault:"39,140,78,135,61,1"`
}
```

**Typed errors:**

```go
var (
    ErrUnauthorized     = errors.New("apifootball: invalid API key")
    ErrRateLimited      = errors.New("apifootball: rate limit hit")
    ErrQuotaExhausted   = errors.New("apifootball: daily quota exhausted")
    ErrTimeout          = errors.New("apifootball: request timeout")
    ErrUnreachable      = errors.New("apifootball: endpoint unreachable")
    ErrInvalidResponse  = errors.New("apifootball: response schema mismatch")
    ErrFixtureNotFound  = errors.New("apifootball: fixture not found")
)
```

**Retry classification:**
- `ErrTimeout`, `ErrUnreachable`, `ErrRateLimited` → retry-eligible with backoff
- `ErrQuotaExhausted` → retry-eligible but with LONG delay (until tomorrow's quota reset); typically escalates to human
- `ErrUnauthorized`, `ErrInvalidResponse` → NOT retry-eligible

**Testing:**

- Fake at `internal/testutil/fakes/apifootball.FakeClient` — allows
  registering canned fixture / event responses per (date, leagueID) or
  fixtureID. Used extensively by fixture-domain and event-domain tests.
- No CI integration test against the real API-Football (paid quota;
  keep it out of CI). Sanity-check via `scripts/manual_api_probe.sh`.

### `internal/infra/twitter`

HTTP client to the twitter container's search service. Handles instance
discovery (multiple twitter replicas — pick a healthy one) + retry on
instance-drain.

**Client interface:**

```go
package twitter

type Client interface {
    // Search issues a POST /search to a healthy twitter instance.
    // Returns the raw SearchResponse from discovery domain.
    Search(ctx context.Context, req discovery.SearchRequest) (*discovery.SearchResponse, error)

    // GetHealth polls /health on a specific instance.
    GetHealth(ctx context.Context, instanceURL string) (*session.HealthReport, error)

    // ListHealthyInstances returns URLs of currently-healthy twitter
    // instances. Uses the scaler's registry endpoint if configured,
    // else the fallback list from env config.
    ListHealthyInstances(ctx context.Context) ([]string, error)

    // Ping confirms at least one twitter instance is reachable.
    Ping(ctx context.Context) error
}
```

**Configuration:**

```go
type Config struct {
    InstanceURLs        []string      `env:"TWITTER_INSTANCE_URLS"`   // fallback list
    RegistryURL         string        `env:"TWITTER_REGISTRY_URL"`    // if using scaler registry
    RequestTimeout      time.Duration `env:"TWITTER_REQUEST_TIMEOUT" envDefault:"120s"`
    HealthCheckInterval time.Duration `env:"TWITTER_HEALTH_CHECK_INTERVAL" envDefault:"30s"`
}
```

**Instance-selection logic:**

1. `ListHealthyInstances` — refresh every `HealthCheckInterval` (cached).
2. For a search call, pick the instance with lowest recent search latency
   among the healthy set (from `HealthReport.LastSearchLatencyMs`).
3. If the chosen instance returns 503 with `X-Drain: true`, retry against
   a different healthy instance (once).
4. If ALL instances are unhealthy or draining, return `ErrFleetDrained`
   — activity's retry policy will back off.

**Typed errors:**

```go
var (
    ErrUnreachable       = errors.New("twitter: no instance reachable")
    ErrAuthRequired      = errors.New("twitter: 401 — cookies expired, VNC re-auth needed")
    ErrSearchTimeout     = errors.New("twitter: search exceeded max duration")
    ErrFleetDrained      = errors.New("twitter: no healthy instance available")
    ErrInstanceUnhealthy = errors.New("twitter: chosen instance is unhealthy")
    ErrMalformedResponse = errors.New("twitter: response body not valid JSON")
)
```

**Retry classification:**
- `ErrUnreachable`, `ErrSearchTimeout`, `ErrFleetDrained` → retry-eligible (may resolve with backoff)
- `ErrAuthRequired` → NOT retry-eligible (alert to operator; needs VNC re-auth)
- `ErrMalformedResponse` → NOT retry-eligible (bug in twitter container)

**Testing:**

- `internal/testutil/fakes/twitter.FakeClient` allows registering canned
  responses per query. Discovery-domain tests use this.
- No CI integration against a real twitter container (needs Firefox +
  cookies + Playwright); manual verification in dev.

### `internal/infra/syndication`

Twitter syndication API client. Used to fetch the actual video URLs
from tweet status URLs (distinct from the twitter container's search
service).

**Client interface:**

```go
package syndication

type Client interface {
    // FetchVideoVariants extracts video download URLs from a tweet.
    // Returns all available quality variants (mp4 URLs at different
    // bitrates); caller picks the best.
    FetchVideoVariants(ctx context.Context, tweetURL string) ([]VideoVariant, error)

    // DownloadVideo streams a video file from the CDN URL to a local path.
    // Returns typed errors classifying the failure mode (geo, deleted,
    // rate-limited, etc.).
    DownloadVideo(ctx context.Context, cdnURL, destPath string) (*DownloadResult, error)
}

type VideoVariant struct {
    URL      string
    Bitrate  int   // bits/second; higher = better quality
    Width    int
    Height   int
    Duration float64
}

type DownloadResult struct {
    FilePath    string
    FileSize    int64
    Duration    float64
    Width       int
    Height      int
    Bitrate     int
    MD5         []byte
}
```

**Configuration:**

```go
type Config struct {
    SyndicationBaseURL string        `env:"TWITTER_SYNDICATION_URL" envDefault:"https://cdn.syndication.twimg.com"`
    UserAgent          string        `env:"TWITTER_SYNDICATION_UA"  envDefault:"Mozilla/5.0 (compatible; found-footy/1.0)"`
    RequestTimeout     time.Duration `env:"TWITTER_SYNDICATION_TIMEOUT" envDefault:"90s"`
    MinSnowflakeLen    int           `env:"TWITTER_SYNDICATION_MIN_SNOWFLAKE_LEN" envDefault:"18"`
}
```

**URL validation** (audit §8 snowflake-truncation defense — this is where the fix from Sprint P2b lives):

```go
// ValidateStatusURL enforces the snowflake-ID length invariant on tweet
// URLs before attempting a syndication API call. Extracted status IDs
// must be >= MinSnowflakeLen digits and <= 19 (real snowflakes are 18-19).
// Returns ErrURLMalformed for truncated or padded IDs.
//
// This is where the audit §8 lived problem gets defended against at
// the earliest possible point in the pipeline.
func ValidateStatusURL(tweetURL string, minLen int) error
```

**Typed errors:**

```go
var (
    ErrURLMalformed        = errors.New("syndication: URL failed snowflake-ID validation")
    ErrTweetNotFound       = errors.New("syndication: tweet returned 404")
    ErrTweetDeleted        = errors.New("syndication: tweet deleted")
    ErrGeoRestricted       = errors.New("syndication: video geo-restricted (403)")
    ErrRateLimited         = errors.New("syndication: rate limit hit (429)")
    ErrTimeout             = errors.New("syndication: request timeout")
    ErrCDNUnreachable      = errors.New("syndication: CDN unreachable")
    ErrNoVariants          = errors.New("syndication: no video variants in response")
    ErrDownloadTruncated   = errors.New("syndication: download body shorter than expected")
    ErrDownloadChecksumFail = errors.New("syndication: download checksum mismatch")
)
```

**Retry classification:**
- `ErrTimeout`, `ErrRateLimited`, `ErrCDNUnreachable` → retry-eligible
- `ErrURLMalformed`, `ErrTweetNotFound`, `ErrTweetDeleted`, `ErrGeoRestricted` → NOT retry-eligible (log with class for telemetry)
- `ErrNoVariants` → NOT retry-eligible (nothing to download)

**Testing:**

- Fake with registered canned responses (tweet URL → variants or error).
- Real integration is done manually via `scripts/manual_syndication_probe.sh`
  when investigating specific bugs like the snowflake-truncation issue.

### `internal/infra/ffmpeg`

Subprocess wrapper for the `ffmpeg` CLI. Handles frame extraction,
duration/metadata probes, and any future video-manipulation needs.
Always uses `os/exec` with an explicit `ctx` (SIGKILL on context cancel).

**Client interface:**

```go
package ffmpeg

type Client interface {
    // ExtractFrame extracts a single frame at the given position (seconds)
    // into a JPEG. Returns the JPEG bytes.
    ExtractFrame(ctx context.Context, videoPath string, positionSecs float64, quality int) ([]byte, error)

    // ExtractFramesAtFractions is a convenience: given normalized positions
    // 0.0-1.0 and the video's known duration, extracts JPEGs at each.
    // Each extraction is a separate ffmpeg invocation (deterministic seek).
    ExtractFramesAtFractions(ctx context.Context, videoPath string, durationSecs float64, fractions []float64, quality int) ([]FrameJPEG, error)

    // ProbeMetadata returns duration + resolution + bitrate via ffprobe.
    ProbeMetadata(ctx context.Context, videoPath string) (*VideoMetadata, error)

    // ExtractDenseFrames extracts frames at fixed intervals (e.g. every 0.25s)
    // for perceptual-hash dense sampling.
    ExtractDenseFrames(ctx context.Context, videoPath string, intervalSecs float64, quality int) ([]FrameJPEG, error)

    // Ping checks that ffmpeg + ffprobe binaries are present + runnable.
    Ping(ctx context.Context) error
}

type FrameJPEG struct {
    PositionSecs float64
    JPEGBytes    []byte
}

type VideoMetadata struct {
    DurationSecs float64
    Width        int
    Height       int
    Bitrate      int
    Codec        string
    ContainerFmt string
    FrameRate    float64
}
```

**Configuration:**

```go
type Config struct {
    FFmpegPath  string        `env:"FFMPEG_PATH" envDefault:"ffmpeg"`
    FFprobePath string        `env:"FFPROBE_PATH" envDefault:"ffprobe"`
    DefaultTimeout time.Duration `env:"FFMPEG_DEFAULT_TIMEOUT" envDefault:"30s"`
    MaxProcesses int           `env:"FFMPEG_MAX_CONCURRENT" envDefault:"4"`  // semaphore
}
```

**Concurrency limit.** ffmpeg extractions are CPU-heavy. The adapter
holds an internal semaphore of `MaxProcesses`; extraction calls block
until a slot is available. Prevents accidental fork-bomb from a
DownloadWorkflow with many parallel activities each spawning ffmpeg.

**Subprocess lifecycle.** Every invocation:
1. `exec.CommandContext(ctx, ...)` — SIGKILL on context cancel.
2. Capture stdout + stderr to bounded buffers (avoid OOM on runaway
   output).
3. On non-zero exit: parse stderr for known error patterns → typed
   error.
4. On timeout: SIGKILL, return `ErrExtractionTimeout`.

**Typed errors:**

```go
var (
    ErrBinaryNotFound      = errors.New("ffmpeg: binary not found in PATH")
    ErrProbeFailed         = errors.New("ffmpeg: probe failed")
    ErrExtractionFailed    = errors.New("ffmpeg: extraction failed")
    ErrExtractionTimeout   = errors.New("ffmpeg: extraction timeout")
    ErrInputNotFound       = errors.New("ffmpeg: input file not found")
    ErrInputCorrupted      = errors.New("ffmpeg: input file corrupted/unreadable")
    ErrOutputWriteFailed   = errors.New("ffmpeg: output write failed (disk full?)")
    ErrConcurrencyExhausted = errors.New("ffmpeg: max concurrent extractions in flight")
)
```

**Retry classification:**
- `ErrExtractionTimeout`, `ErrOutputWriteFailed`, `ErrConcurrencyExhausted` → retry-eligible
- `ErrBinaryNotFound`, `ErrInputNotFound`, `ErrInputCorrupted` → NOT retry-eligible

**Testing:**

- Fake at `internal/testutil/fakes/ffmpeg.FakeClient` — returns
  pre-registered JPEGs per (video path, position). Vision-domain tests
  use this heavily.
- Integration tests use a small `testdata/sample.mp4` fixture; runs
  real ffmpeg in CI (ffmpeg is available on GitHub Actions runners).

### `internal/infra/wikidata`

Wikidata SPARQL client. Used by alias domain's RAG pipeline.

**Client interface:**

```go
package wikidata

type Client interface {
    // SearchEntities does a full-text search returning candidate QIDs.
    // Ordered by Wikidata's relevance ranking.
    SearchEntities(ctx context.Context, query string, limit int) ([]Entity, error)

    // FetchEntityAliases returns all known aliases for a QID in the
    // specified language (default "en").
    FetchEntityAliases(ctx context.Context, qid, lang string) ([]string, error)

    // FetchEntity returns full entity metadata (labels, descriptions,
    // claims). Used sparingly — most calls are the two above.
    FetchEntity(ctx context.Context, qid string) (*Entity, error)
}

type Entity struct {
    QID         string
    Label       string
    Description string
}
```

**Configuration:**

```go
type Config struct {
    SPARQLEndpoint string        `env:"WIKIDATA_SPARQL_URL" envDefault:"https://query.wikidata.org/sparql"`
    APIBaseURL     string        `env:"WIKIDATA_API_URL"    envDefault:"https://www.wikidata.org/wiki/Special:EntityData"`
    UserAgent      string        `env:"WIKIDATA_USER_AGENT" envDefault:"found-footy/1.0 (self-hosted; https://example)"`
    RequestTimeout time.Duration `env:"WIKIDATA_TIMEOUT"    envDefault:"10s"`
}
```

**Rate limiting:** Wikidata's public endpoints have generous limits but
strict user-agent requirements. The `UserAgent` env var MUST identify
the caller per Wikimedia's UA policy. Default is a placeholder;
production sets it to a proper URL.

**Typed errors:**

```go
var (
    ErrUnreachable    = errors.New("wikidata: endpoint unreachable")
    ErrTimeout        = errors.New("wikidata: request timeout")
    ErrRateLimited    = errors.New("wikidata: rate limit hit")
    ErrEntityNotFound = errors.New("wikidata: entity not found")
    ErrSPARQLError    = errors.New("wikidata: SPARQL query error")
)
```

**Retry classification:**
- `ErrUnreachable`, `ErrTimeout`, `ErrRateLimited` → retry-eligible
- `ErrEntityNotFound`, `ErrSPARQLError` → NOT retry-eligible

**Testing:**

- Fake with registered canned entity/alias responses.
- No CI integration (external service; not worth the flake risk).

### Adapter registration in `cmd/*/main.go`

Every binary that needs adapters constructs them at startup and injects
into services. Example from `cmd/worker/main.go`:

```go
func main() {
    ctx := context.Background()
    logger := logging.New()
    cfg := config.MustLoad()

    // Migrations first (idempotent)
    if err := pg.RunMigrations(ctx, cfg.PG, migrations.FS); err != nil {
        logger.Error("migrate failed", "err", err); os.Exit(1)
    }

    // Adapters
    pgPool, err := pg.New(ctx, cfg.PG, logger); mustNoErr(err)
    defer pgPool.Close()

    s3Client, err := s3.New(cfg.S3, logger); mustNoErr(err)
    llmClient, err := llm.New(cfg.LLM, logger); mustNoErr(err)
    apiClient, err := apifootball.New(cfg.APIFootball, logger); mustNoErr(err)
    twitterClient, err := twitter.New(cfg.Twitter, logger); mustNoErr(err)
    syndClient, err := syndication.New(cfg.Syndication, logger); mustNoErr(err)
    ffmpegClient, err := ffmpeg.New(cfg.FFmpeg, logger); mustNoErr(err)
    wikiClient, err := wikidata.New(cfg.Wikidata, logger); mustNoErr(err)

    // Domain services
    fixtureService := fixture.NewService(fixture.NewPGStore(pgPool), logger)
    eventService := event.NewService(event.NewPGStore(pgPool), logger)
    aliasService := alias.NewService(alias.NewPGStore(pgPool), wikiClient, llmClient, logger)
    // ... etc.

    // Temporal wiring
    tclient, err := temporal.NewClient(ctx, cfg.Temporal, logger); mustNoErr(err)
    w := temporal.NewWorker(tclient, cfg.Temporal, logger)

    // Register workflows
    w.RegisterWorkflow(workflow.IngestWorkflow)
    w.RegisterWorkflow(workflow.MonitorWorkflow)
    w.RegisterWorkflow(workflow.DiscoveryWorkflow)
    w.RegisterWorkflow(workflow.DownloadWorkflow)
    w.RegisterWorkflow(workflow.UploadWorkflow)

    // Register activities — each imports the services it needs
    activity.RegisterFixtureActivities(w, fixtureService)
    activity.RegisterEventActivities(w, eventService)
    activity.RegisterDiscoveryActivities(w, discoveryService)
    activity.RegisterDownloadActivities(w, ...)
    // ... etc.

    logger.Info("worker started", "task_queue", cfg.Temporal.TaskQueue)
    if err := w.Run(worker.InterruptCh()); err != nil {
        logger.Error("worker failed", "err", err); os.Exit(1)
    }
}
```

Similar shape for `cmd/api/main.go` (no Temporal worker, but the same
adapter set + `internal/api` handlers registered on Chi router).

### Health check aggregation

Every binary's `/healthz` endpoint aggregates health across all adapters:

```go
func healthzHandler(deps HealthDeps) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        checks := map[string]error{
            "postgres":     deps.PG.Ping(r.Context()),
            "s3":           deps.S3.Ping(r.Context(), deps.Cfg.S3.Bucket),
            "llm":          deps.LLM.Ping(r.Context()),
            "apifootball":  deps.API.Ping(r.Context()),
            "twitter":      deps.Twitter.Ping(r.Context()),
            "ffmpeg":       deps.FFmpeg.Ping(r.Context()),
            "wikidata":     deps.Wikidata.Ping(r.Context()),
        }
        // Return 200 if all pass; 503 if any fail; body enumerates statuses
    }
}
```

Missing from the health check: `syndication` (external CDN, brittle) and
`temporal` (Temporal client has its own health protocol via its SDK).

### Observability contract

Every adapter emits structured JSON logs at each call boundary with these
fields at minimum:
- `adapter` — the package name (e.g., `"pg"`, `"llm"`)
- `action` — the method invoked (e.g., `"query"`, `"chat_completion"`)
- `duration_ms` — call duration
- `error_class` — the typed error class if failed, else absent
- Domain-specific fields (e.g., `sql_prefix` for pg, `model` for llm)

Prometheus metrics per adapter:
- `<adapter>_calls_total{action, error_class}` — counter
- `<adapter>_duration_ms{action}` — histogram

Exposed on the `/metrics` endpoint via `prometheus/client_golang`.

### Extensibility hook

Adding a new adapter follows this pattern:

1. Create `internal/infra/<name>/` with `client.go` (interface),
   `client_real.go` (implementation), `config.go` (env-var struct),
   `errors.go` (typed errors), and `*_test.go`.
2. Add fake at `internal/testutil/fakes/<name>/`.
3. Add integration test if a backend can be containerized.
4. Wire into `cmd/*/main.go` startup (adapter construction + injection
   into services that need it).
5. Add `Ping` method + wire into `/healthz`.
6. Add adapter's Prometheus metrics + Loki module label.

Zero changes to unrelated adapters or domains. New adapter = new
package, isolated.

---

## 6. Discovery pipeline

End-to-end spec for how DiscoveryWorkflow (§5) actually finds video
candidates for a stable goal event. Composes `domain/discovery`,
`domain/alias`, `domain/session`, `infra/twitter`, and the child spawn
into DownloadWorkflow. Boundary: this section ends when URLs are
validated + source-scored + handed to DownloadWorkflow. Actual
downloads live in §7.

### Pipeline flow

```
[Event debounced stable in MonitorWorkflow]
              │
              ▼
   spawn DiscoveryWorkflow  ────► id = "discovery-<event_uuid>"
              │                      REJECT_DUPLICATE (audit §2)
              ▼
   ┌─────────────────────────┐
   │ 1. Resolve team aliases │  activity.GetOrResolveTeamAliases(TeamID)
   │    (cache hit or RAG)   │  from alias domain
   └────────────┬────────────┘
                ▼
   ┌─────────────────────────┐
   │ 2. Save aliases to event│  activity.SaveEventTwitterAliases(...)
   │    (audit trail + FE)   │  from event domain
   └────────────┬────────────┘
                ▼
   ┌──────────────────────────────────────────────┐
   │ 3. Attempt loop: attempt = 1..MaxAttempts    │
   │                                              │
   │   a. CheckEventStillLive → VAR abort?        │
   │   b. CountDownloadWorkflows → target met?    │
   │   c. Adaptive early-exit check?              │
   │   d. Build query from player + aliases       │
   │   e. Pick healthy Twitter instance           │
   │   f. SearchTwitter → SearchResponse          │
   │   g. Filter by URL validation + duration     │
   │   h. Update workflow-local exclude set       │
   │   i. Score sources, pick top-5 candidates    │
   │   j. Register discovered videos in event     │
   │   k. Spawn DownloadWorkflow (ABANDON)        │
   │   l. Sleep until next attempt (adaptive)     │
   │                                              │
   └────────────┬─────────────────────────────────┘
                ▼
   ┌─────────────────────────┐
   │ 4. Return workflow      │
   │    outcome + telemetry  │
   └─────────────────────────┘
```

### Step 1: Team alias resolution

**Activity call:** `GetOrResolveTeamAliases(teamID int) ([]string, error)`
from `internal/activity/alias.go`, which wraps `alias.Service.GetOrResolve`.

**Timing:** cache hit is ~10ms; RAG miss is 2-15 seconds (Wikidata search
+ LLM narrowing).

**Behavior:**

```go
aliases, err := workflow.ExecuteActivity(
    ctx,
    activity.GetOrResolveTeamAliases,
    input.TeamID,
).Get(ctx, &aliasResult)
```

If the RAG pipeline fails and returns fallback aliases (per §4 alias
domain's `ErrRAGFailedFallback` behavior), we still proceed —
degraded aliases are better than no discovery. The fallback derives
aliases from the team name (first word, initials, first + last).

**Failure handling:**
- `alias.ErrNotFound` (team_id not resolvable to a team) → workflow
  aborts with typed error. Discovery can't work without aliases.
- `alias.ErrWikidataUnreachable` / `alias.ErrLLMUnavailable` → RAG
  path fails, fallback returned. Workflow continues with degraded
  aliases.
- `alias.ErrRAGFailedFallback` → not treated as an error; log at
  WARN level, continue.

### Step 2: Save aliases to the event

Once we have aliases, persist them to `events.telemetry` for audit
trail and frontend display:

**Activity call:** `SaveEventTwitterAliases(eventID, aliases []string) error`
from `internal/activity/event.go`, wraps
`event.Service.UpdateTelemetry` with a `TelemetryPatch` setting the
`twitter_aliases_snapshot_at` + `twitter_aliases` fields.

**Why persist:** if aliases change mid-discovery (e.g., we re-resolve
because the current set is empty), the snapshot lets us diagnose "why
did discovery pick these queries." Frontend shows them under the event
as debug info.

### Step 3: The attempt loop

The core of the workflow. Runs up to `MaxAttempts` (default 10) with
between-attempt spacing.

**Attempt-loop pseudocode (workflow-scope Go):**

```go
excludeSet := make(map[string]bool)  // tweet URLs seen this event lifetime
consecutiveEmpty := 0
downloadsSpawned := 0

for attempt := 1; attempt <= input.MaxAttempts; attempt++ {
    attemptStart := workflow.Now(ctx)

    // 3a. Event still live?
    stillLive, err := checkEventStillLive(ctx, input.EventID)
    if err != nil || !stillLive {
        return earlyExit(ctx, "event_removed", attempt-1, downloadsSpawned)
    }

    // 3b. Download target already met (10 workflows registered)?
    downloadCount, err := countDownloadWorkflowsForEvent(ctx, input.EventID)
    if err == nil && downloadCount >= downloadTargetThreshold {
        return earlyExit(ctx, "downloads_target_met", attempt-1, downloadsSpawned)
    }

    // 3c. Adaptive early-exit — 2 consecutive empty results = give up
    // (audit §8 hardening addition)
    if consecutiveEmpty >= adaptiveExitThreshold {
        return earlyExit(ctx, "consecutive_empty", attempt-1, downloadsSpawned)
    }

    // 3d. Build query
    query := buildQuery(input.PlayerName, aliases)

    // 3e-f. Search
    resp, err := searchTwitter(ctx, discovery.SearchRequest{
        Query:         query,
        ExcludeURLs:   keysOf(excludeSet),
        MaxAgeMinutes: input.MaxSearchAgeMinutes,  // 3 by default
    })
    if err != nil {
        // Log + classify; don't abort the loop unless auth-required
        if errors.Is(err, discovery.ErrTwitterAuthRequired) {
            return earlyExit(ctx, "twitter_auth_required", attempt, downloadsSpawned)
        }
        // Otherwise sleep + retry loop
        sleepUntilNextAttempt(ctx, attemptStart, input.AttemptSpacingSecs)
        continue
    }

    // 3g. Filter: URL validation happens inside SearchTwitter's caller
    // (discovery domain's Search method). Duration filter also there.
    // What's returned here is already validated + filtered.
    freshVideos := filterExcluded(resp.Videos, excludeSet)

    if len(freshVideos) == 0 {
        consecutiveEmpty++
        sleepUntilNextAttempt(ctx, attemptStart, input.AttemptSpacingSecs)
        continue
    }
    consecutiveEmpty = 0

    // 3h. Update exclude set with EVERYTHING returned (fresh + already-seen)
    for _, v := range resp.Videos {
        excludeSet[v.TweetURL] = true
    }

    // 3i. Score sources, pick top-5 candidates by (source_score DESC, duration DESC)
    scored := scoreAndRank(freshVideos, discoveryService, downloadTargetVideosPerAttempt)

    // 3j. Register discovered videos in the event (for telemetry + frontend)
    for _, sv := range scored {
        _ = registerEventDiscoveredVideo(ctx, input.EventID, sv.Video)
    }

    // 3k. Spawn DownloadWorkflow — fire-and-forget with ABANDON
    downloadWorkflowID := fmt.Sprintf("download-%02d-%s", attempt, input.EventID.String())
    childOpts := workflow.ChildWorkflowOptions{
        WorkflowID: downloadWorkflowID,
        ParentClosePolicy: enums.PARENT_CLOSE_POLICY_ABANDON,
        WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
        TaskQueue: cfg.TaskQueue,
    }
    workflow.ExecuteChildWorkflow(
        workflow.WithChildOptions(ctx, childOpts),
        workflow.DownloadWorkflow,
        workflow.DownloadWorkflowInput{
            EventID:    input.EventID,
            FixtureID:  input.FixtureID,
            Attempt:    attempt,
            Videos:     videosFromScored(scored),
            APIElapsed: input.Minute,
            APIExtra:   input.Extra,
        },
    )
    downloadsSpawned++

    // 3l. Sleep until next attempt (min 10s guard, spacing from start)
    sleepUntilNextAttempt(ctx, attemptStart, input.AttemptSpacingSecs)
}
```

### Query construction

Concrete algorithm for `buildQuery(playerName, aliases)` → Twitter search string.

**Player name normalization:**

```go
func extractPlayerSearchNames(fullName string) []string {
    // Trim, split by space, drop accents, drop empty
    // "Florian Wirtz" → ["Florian", "Wirtz"]
    // "Mohamed Salah" → ["Mohamed", "Salah"]
    // "C. Ronaldo" → ["C.", "Ronaldo"] — filter tokens < 3 chars → ["Ronaldo"]
    // "Kylian Mbappé" → ["Kylian", "Mbappe"] — accent stripped
    // Hyphens: "N'Golo Kanté" → ["Kante"]
    // ...
}
```

Rules:
1. Strip accents via NFKD normalization (`golang.org/x/text/unicode/norm`).
2. Split on whitespace and apostrophes.
3. Filter tokens < 3 chars (drops initials).
4. If more than 2 tokens remain, keep first and last only.

**Team alias handling:**

Aliases are already normalized (audit §4 alias domain: diacritics
stripped, filtered for hallucinations, LLM-selected). Direct use.

**Query template:**

```
"(<PlayerToken1> OR <PlayerToken2>) (<Alias1> OR <Alias2> OR ...)"
```

If only one player token: `"<PlayerToken> (<Alias1> OR ...)"`

Concrete examples:

| Input | Query |
|---|---|
| player="Florian Wirtz", aliases=["LFC","Liverpool"] | `"(Florian OR Wirtz) (LFC OR Liverpool)"` |
| player="C. Ronaldo", aliases=["Portugal","Selecão","Seleção"] | `"Ronaldo (Portugal OR Selecao OR Selecao)"` (dedup after normalize) |
| player="Salah", aliases=["Liverpool"] | `"Salah (Liverpool)"` |

**Escape:** Twitter's search query language doesn't have special
characters we generate here — no LIKE-style wildcards, no colons
outside operators. If a player name contains `"`, `(`, `)`, `-`, or
`OR`, the whole token is quoted: `"O'Brien"` → `"\"O'Brien\""`.

### Attempt timing and spacing

**Base spacing:** 60 seconds between attempts, measured from the START
of an attempt (not the end). This means:

- If attempt N takes 3 seconds → sleep 57 seconds until attempt N+1
- If attempt N takes 45 seconds → sleep 15 seconds until attempt N+1
- If attempt N takes 61 seconds → immediately start attempt N+1

**Minimum sleep guard:** 10 seconds. Never fire two attempts back-to-back
even if one took longer than the spacing target. Prevents runaway loops
if Twitter is returning quickly with garbage.

**Max attempts:** 10 (`input.MaxAttempts`, tunable per event via
DiscoveryWorkflowInput).

**Total budget:** ~10 minutes (10 attempts × 60s spacing).

**Adaptive early-exit spacing (extension for §11 SLO consumers):** for
high-importance fixtures (Champions League, WC), we may want to try more
frequently. Not implemented day one; the shape is:

```go
if fixture.IsHighImportance() {
    input.AttemptSpacingSecs = 30  // 2x tighter
    input.MaxAttempts = 15         // extended budget
}
```

Added via `IngestWorkflow`'s per-fixture setup when categorizing.

### URL exclusion tracking

Twitter search takes `exclude_urls: []string` in the request body
(§9 `infra/twitter.SearchRequest`). Discovery uses this to avoid
re-processing the same URLs across attempts.

**Exclude set lifetime:** the workflow-local `excludeSet` map. Not
persisted; if DiscoveryWorkflow restarts (worker crash → replay), the
set is rebuilt from `event.discovered_videos` in the events table
(populated by `RegisterEventDiscoveredVideo` in step 3j).

**Replay safety:** on workflow replay, the same activity calls happen
in the same order, so the exclude set repopulates deterministically.
Temporal replay is fine.

**What goes in the set:**
- Every URL returned by Twitter search, even ones we didn't pick as
  top-5. Prevents re-scoring the same tweets on later attempts.
- URLs that failed validation. If Twitter returns a truncated
  snowflake ID, we don't want it back on attempt N+1.

### Source-quality scoring

Discovery scores each returned video before picking the top-5 for
DownloadWorkflow. Day-one behavior is rule-based; when the
`textanalysis` domain ships, its LLM-classified scores merge in.

**Day-one rule table (`discovery.Service.ScoreSource`):**

| Signal | Score contribution |
|---|---|
| `author_verified = true` AND handle matches known broadcaster pattern (`BBCSport`, `ESPNFC`, `SkySportsPL`, etc. — pattern list in config) | +0.5 |
| `author_verified = true` AND handle matches known media outlet pattern | +0.3 |
| `author_verified = true` (any) | +0.2 |
| `author_followers > 100000` | +0.1 |
| `duration > 8 seconds` (proxy for "not just the ball hitting net") | +0.05 |
| `duration > 20 seconds` (highlight package, not raw clip) | +0.1 |
| default | 0.0 |

Signals sum. Max ~1.0. Ties broken by `duration_seconds DESC`.

**Broadcaster pattern list** lives in
`internal/config.DiscoveryConfig.BroadcasterHandles` — a `map[string][]string`
of `{country: [handles]}` for readable maintenance. Loaded from a YAML
file bundled in the binary; hot-reload not needed (broadcaster accounts
don't change often).

**When textanalysis ships:**

The scoring changes to `Score = ruleScore * 0.4 + intentScore * 0.6`
where `intentScore` comes from `textanalysis.Service.ClassifySource(handle, verified)`.
Rule-based signal stays as the fallback for cases where textanalysis
hasn't classified the source yet.

### Concurrency and rate-limiting

**Per-workflow concurrency:** 1 search at a time per DiscoveryWorkflow.
The attempt loop is sequential. No parallelism within a single event's
discovery.

**Cross-workflow concurrency:** if 4 events across 2 fixtures all spawn
DiscoveryWorkflow at once (e.g., a chaotic Champions League night),
that's 4 concurrent SearchTwitter activities. The `infra/twitter`
adapter picks a healthy instance per call; up to `MaxConcurrentActivityExecutions`
(default 30) can run in parallel per worker container.

**Twitter fleet capacity:** each instance handles 1 search at a time
serially (Firefox is single-threaded per browser). With 4 instances,
4 concurrent searches max. With 8 (peak scale), 8 concurrent. If
demand exceeds capacity, requests queue at the twitter container layer
and may exceed the 60s request timeout — activity retries with backoff.

**Between attempts within one event:** 60s spacing (min 10s) prevents
runaway single-event pressure.

### Fleet health integration

Before every SearchTwitter call, the `infra/twitter.Client` internally
consults its cached view of instance health and picks the least-loaded
healthy instance. The activity doesn't need explicit fleet-health
checks — the adapter handles it.

However, DiscoveryWorkflow does surface an operational signal: if a
search returns `ErrTwitterAuthRequired`, the workflow aborts with a
telemetry entry `"twitter_auth_required"` on that attempt, and the
activity's own logging fires the audit §8 cookie-staleness alert
(consecutive auth failures across the fleet).

If ALL instances are draining (`ErrTwitterFleetDrained`), the workflow
sleeps its normal spacing and retries — draining is temporary. If
draining persists past `MaxAttempts × AttemptSpacing`, the workflow
completes with 0 downloads spawned and telemetry captures the failure
class.

### Failure modes and telemetry

The `DiscoveryWorkflowOutput.EarlyExitReason` field is what tells
observability whether a workflow ended naturally or bailed. Values:

| Value | Meaning |
|---|---|
| `""` (empty) | Ran all `MaxAttempts` normally (didn't early-exit) |
| `"downloads_target_met"` | Hit the 10-DownloadWorkflow target before running out of attempts |
| `"consecutive_empty"` | Adaptive exit — 2 consecutive attempts returned zero fresh videos |
| `"event_removed"` | Event marked removed (VAR) mid-loop |
| `"twitter_auth_required"` | Cookies expired; alerting operator |
| `"alias_resolution_failed"` | Couldn't resolve team aliases even with fallback |

Every value gets a corresponding entry in the event's
`_telemetry.discovery_summary` field for post-fixture SLO reporting.

**Per-attempt telemetry:**

`event.Telemetry.SearchAttempts += 1` after each attempt (via
`UpdateEventTelemetry` activity). Fine-grained per-attempt latency +
result-count metrics live in Loki via structured logs, not Postgres.

### Testing shape

**Workflow test:**

```go
// internal/workflow/discovery_test.go
func TestDiscoveryWorkflow_HappyPath_SpawnsDownloadsUpToTarget(t *testing.T) {
    ts := &testsuite.WorkflowTestSuite{}
    env := ts.NewTestWorkflowEnvironment()

    // Mock alias resolution
    env.OnActivity(activity.GetOrResolveTeamAliases, mock.Anything, 40).Return([]string{"Liverpool", "LFC"}, nil)
    env.OnActivity(activity.SaveEventTwitterAliases, ...).Return(nil)

    // Mock 3 successful searches, each returning 5 fresh videos
    for i := 0; i < 3; i++ {
        env.OnActivity(activity.SearchTwitter, mock.Anything, mock.Anything).Return(
            &discovery.SearchResponse{Videos: sampleVideos(5)},
            nil,
        ).Times(1)
    }

    // Downloads spawn — mock the child workflow
    env.OnWorkflow(workflow.DownloadWorkflow, mock.Anything, mock.Anything).Return(
        &workflow.DownloadWorkflowOutput{}, nil,
    )

    env.ExecuteWorkflow(workflow.DiscoveryWorkflow, workflow.DiscoveryWorkflowInput{
        EventID:     testUUID,
        MaxAttempts: 10,
    })

    require.True(t, env.IsWorkflowCompleted())
    var out workflow.DiscoveryWorkflowOutput
    require.NoError(t, env.GetWorkflowResult(&out))
    require.Equal(t, 3, out.DownloadsSpawned)
    require.Equal(t, 15, out.VideosDiscovered)  // 3 attempts * 5 videos
}
```

**Query-builder tests** live in `internal/domain/discovery/query_test.go`
(pure function, no Temporal):

```go
func TestBuildQuery_TwoTokenPlayer(t *testing.T)
func TestBuildQuery_SingleTokenPlayerAfterFiltering(t *testing.T)
func TestBuildQuery_AccentedNameNormalized(t *testing.T)
func TestBuildQuery_ApostropheInName(t *testing.T)
func TestBuildQuery_ManyAliases_JoinedWithOR(t *testing.T)
func TestBuildQuery_EmptyAliases_ReturnsError(t *testing.T)
```

**Adaptive-exit tests:**

```go
func TestDiscoveryWorkflow_TwoConsecutiveEmpty_EarlyExits(t *testing.T)
func TestDiscoveryWorkflow_OneEmptyThenResults_ResetsCounter(t *testing.T)
func TestDiscoveryWorkflow_DownloadTargetMet_EarlyExits(t *testing.T)
func TestDiscoveryWorkflow_EventVARdMidLoop_EarlyExits(t *testing.T)
func TestDiscoveryWorkflow_AuthRequired_EarlyExitsWithAlert(t *testing.T)
```

**Query construction unit tests** for the query builder are the fastest
tests in the whole test suite. Run in microseconds.

**Fleet health integration tests** live at the `infra/twitter` layer,
not here — DiscoveryWorkflow trusts the adapter to route to healthy
instances.

### Extensibility hooks

**Adding a new discovery source (e.g., YouTube):**

1. Add `internal/infra/youtube/` adapter following the §9 pattern.
2. Extend `domain/discovery` with:
   - `SearchYouTube(ctx, req YouTubeSearchRequest) ([]DiscoveredVideo, error)`
   - Same `DiscoveredVideo` shape — YouTube's video URL is the "tweet_url"
     equivalent, `channel_handle` becomes `author_handle`, etc.
3. In DiscoveryWorkflow, add a second attempt-source path:
   ```go
   // Alternate between Twitter and YouTube per attempt for parallelism
   if attempt % 2 == 0 {
       resp = searchYouTube(...)
   } else {
       resp = searchTwitter(...)
   }
   ```
4. Update source-scoring to include a per-source-platform weight.

Zero changes to fixture, event, video, session domains.

**Adding importance-aware spacing:**

The `DiscoveryWorkflowInput.AttemptSpacingSecs` and `MaxAttempts` fields
are already input-level. Have IngestWorkflow set them per-fixture based
on `fixture.LeagueID` and known-importance mapping.

**Adding a query-language reinvention:**

The `buildQuery` function is pure — swap the implementation without
touching workflow shape. E.g., using Twitter's advanced search operators
(`filter:videos`, `since:` / `until:`) becomes an internal change.

**Adding textanalysis-based scoring:**

Wire `textanalysis.Service.ClassifySource(handle, verified)` into
`discovery.Service.ScoreSource`. Merge weights per rule above. Existing
rule-based fallback stays as the default when textanalysis hasn't
classified a source.

---

## 7. Video pipeline

The download-to-upload flow. Composes DownloadWorkflow + UploadWorkflow
(§5) with `domain/vision` + `domain/video` + `domain/event` (§4) and
`infra/syndication` + `infra/ffmpeg` + `infra/llm` + `infra/s3` (§9).

Boundary: starts where DiscoveryWorkflow hands URLs off to
DownloadWorkflow (§6). Ends when the event's SSE notification fires
and `download_complete` flag flips on the event row.

### Pipeline flow

```
[DiscoveryWorkflow spawns DownloadWorkflow with N video URLs]
              │
              ▼
   ┌──────────────────────────────────────┐
   │ DownloadWorkflow                     │
   │                                      │
   │  1. Register self (idempotency)      │  event.RegisterDownloadWorkflow
   │  2. Parallel download                │  infra/syndication.DownloadVideo
   │  3. Filter (aspect / duration / res) │  discovery.Service filters
   │  4. MD5 batch dedup                  │  video.Service.DedupBatchByMD5
   │  5. Sequential AI validation         │  vision.Service.ValidateFrames
   │     (respects LLM cap)               │  (smart 2-3 frame strategy)
   │  6. Timestamp verification           │  vision.Service.VerifyTimestamp
   │     (±3 min, stoppage OCR fix)       │
   │  7. Parallel dHash generation        │  vision.Service.ComputeDHashDense
   │  8. Signal UploadWorkflow            │  workflow.SignalWithStartWorkflow
   │  9. Update event telemetry           │  event.UpdateTelemetry
   └──────────────┬───────────────────────┘
                  │  UploadBatchSignal
                  ▼
   ┌──────────────────────────────────────┐
   │ UploadWorkflow (per-event serialized)│
   │  id = "upload-<event_uuid>"          │
   │                                      │
   │  Loop until idle-timeout (5min):     │
   │    Wait signal channel               │
   │    For each batch received:          │
   │      a. Atomic hash dedup            │  video.Service.UpsertWithHashDedup
   │      b. New? upload to S3            │  infra/s3.Upload
   │         Reused? delete local temp    │  os.Remove
   │      c. Mint share + recalc ranks    │  video.Service.MintShareAndRecalculate
   │         atomically (REPEATABLE READ) │  (partial UNIQUE INDEX enforces)
   │      d. EmitSemanticEvent            │  activity.EmitSemanticEvent
   │         "event.video_ready"          │  (event_log INSERT + nats.Publish)
   │  After all batches:                  │
   │    Try flag download_complete        │  event.TryMarkDownloadComplete
   │    If flipped: notify event log      │
   └──────────────────────────────────────┘
```

### DownloadWorkflow step-by-step

**Step 1: Register self (idempotency invariant).**

FIRST call in the workflow. Retry hard (5 attempts, 2× backoff from 2s)
because if this fails, we lose the ability to count this workflow
toward the 10-download completion threshold.

```go
workflowID := workflow.GetInfo().WorkflowExecution.ID
count, err := workflow.ExecuteActivity(
    ctx,
    activity.RegisterEventDownloadWorkflow,
    input.EventID,
    workflowID,
    (*string)(nil),  // outcome pending; will UPDATE in step 9
).Get(ctx, &count)
```

`event_download_workflows` primary key is `(event_id, workflow_id)`.
Duplicate registration is a no-op; idempotent by construction.

**Step 2: VAR liveness check.**

```go
alive, err := workflow.ExecuteActivity(
    ctx, activity.CheckEventStillLive, input.EventID,
).Get(ctx, &alive)
if !alive {
    // VAR aborted; skip to signal-empty-upload step for completion tracking
    return signalEmptyUpload(ctx, input.EventID)
}
```

**Step 3: Parallel downloads via `workflow.Go`.**

Each video in `input.Videos` (up to 5 per attempt from §6's scoring)
downloads independently. Failures don't cascade — one 403 doesn't
abort the others.

```go
type downloadOutcome struct {
    idx       int
    result    *syndication.DownloadResult
    videoMeta discovery.DiscoveredVideo
    err       error
}

var downloadFutures []workflow.Future
for i, video := range input.Videos {
    i, video := i, video  // capture per goroutine
    fut := workflow.ExecuteActivity(
        ctx,
        activity.DownloadVideo,
        video.TweetURL, i, input.EventID,
    )
    downloadFutures = append(downloadFutures, fut)
}

var outcomes []downloadOutcome
for i, fut := range downloadFutures {
    var res *syndication.DownloadResult
    err := fut.Get(ctx, &res)
    outcomes = append(outcomes, downloadOutcome{
        idx: i, result: res, videoMeta: input.Videos[i], err: err,
    })
}
```

The activity's retry policy handles transient errors. Non-retryable
classes (`ErrURLMalformed`, `VideoGeoRestrictedError`,
`VideoNotAvailableError`, `VideoDeletedError`) fail fast and get
recorded in `failure_classes` counter for telemetry.

**Step 4: MD5 batch dedup.**

Multiple discovered videos may be byte-identical (people re-tweet or
same broadcaster clip appears from multiple accounts). Drop exact
duplicates within this batch before spending LLM budget.

```go
survivors, batchDupes := deduplicateByMD5(outcomes)
```

Pure Go, no activity — it's ~20 lines of map iteration on downloaded
files' MD5s. Lives in `internal/domain/video.DedupBatchByMD5(files)`.

**Step 5: Sequential AI validation.**

This is where the LLM cap enforcement lives. Sequential — not parallel —
because `internal/infra/llm` maintains a per-worker-process semaphore of
2. Running validation in parallel would just queue at the semaphore, not
gain concurrency.

```go
type validated struct {
    outcome           downloadOutcome
    validationResult  *vision.ValidationResult
    keepBecause       string  // for telemetry
}

var validated []validated
for _, outcome := range survivors {
    // Skip if the download failed
    if outcome.err != nil {
        recordFailureClass(outcome.err)
        continue
    }

    // Frame extraction: 25%, 75% first pass
    frames, err := workflow.ExecuteActivity(
        ctx,
        activity.ExtractFramesForValidation,
        outcome.result.FilePath,
        outcome.result.Duration,
        []float64{0.25, 0.75},
    ).Get(ctx, &frames)
    if err != nil {
        recordFailureClass(err)
        continue
    }

    // Vision validation with smart 2-3 strategy
    vr, err := workflow.ExecuteActivity(
        ctx,
        activity.ValidateVideoIsSoccer,
        frames, input.APIElapsed, input.APIExtra,
    ).Get(ctx, &vr)
    if err != nil {
        recordFailureClass(err)
        continue
    }

    // Verdict handling
    switch vr.Verdict {
    case vision.VerdictAccepted:
        validated = append(validated, validated{outcome, vr, "accepted"})
    case vision.VerdictRejectedClockMismatch:
        // Store the video anyway but mark as unverified — audit §4 scoped dedup
        validated = append(validated, validated{outcome, vr, "unverified"})
    default:
        // Non-soccer, phone-screen, inconclusive → drop
        recordRejectionClass(vr.Verdict)
    }
}
```

**Smart 2-3 frame strategy inside `ValidateVideoIsSoccer` activity:**

The vision domain owns this. The activity is a thin wrapper. What
happens inside:

```go
// vision.Service.ValidateFrames — from §4
func (s *Service) ValidateFrames(ctx context.Context, frames []Frame) (*ValidationResult, error) {
    if len(frames) < 2 {
        return nil, ErrInsufficientFrames
    }

    // First LLM call: two frames
    resp1, err := s.llm.ChatCompletionMultiImage(ctx, llm.MultiImageRequest{
        Model:      s.chatModel,
        ImagesJPEG: [][]byte{frames[0].JPEGBytes, frames[1].JPEGBytes},
        Prompt:     s.validationPrompt,
        MaxTokens:  200,
        ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
    })
    if err != nil {
        return nil, err
    }

    per1, per2, err := parseValidationResponse(resp1.Content, 2)
    if err != nil {
        return nil, err
    }

    // Do the two frames agree on SOCCER/SCREEN?
    if per1.IsSoccer == per2.IsSoccer && per1.IsPhoneScreen == per2.IsPhoneScreen {
        // Agreement — 2-frame verdict is final
        return buildVerdict(per1, per2, nil), nil
    }

    // Disagreement — pull tiebreaker frame at 50% and reask on ONE image
    tieFrame, err := s.ffmpeg.ExtractFrame(ctx, frames[0].videoPath, 0.5*frames[0].videoDurationSecs, 90)
    if err != nil {
        // Can't get tiebreaker; return inconclusive
        return buildVerdict(per1, per2, nil), nil
    }

    resp2, err := s.llm.ChatCompletionMultiImage(ctx, llm.MultiImageRequest{
        Model:      s.chatModel,
        ImagesJPEG: [][]byte{tieFrame},
        Prompt:     s.validationPrompt,
        // ...
    })
    per3, _ := parseValidationResponse(resp2.Content, 1)
    return buildVerdict(per1, per2, per3), nil
}
```

Empirically saves ~33% of LLM calls on straightforward matches. When
25% and 75% both clearly show soccer+broadcast, we skip the tiebreaker.

**Structured JSON response format:**

The LLM prompt requests:

```json
{
  "frame_1": {
    "SOCCER": "yes",
    "SCREEN": "no",
    "CLOCK": "45:23",
    "ADDED": "+2",
    "STOPPAGE_CLOCK": ""
  },
  "frame_2": {
    "SOCCER": "yes",
    "SCREEN": "no",
    "CLOCK": "45:34",
    "ADDED": "+2",
    "STOPPAGE_CLOCK": ""
  }
}
```

Parsed into `[]ClockExtraction` per §4 vision domain. The `CLOCK` field
is the main broadcast timer (running clock), `ADDED` is "+N" indicator,
`STOPPAGE_CLOCK` is a separate sub-timer that appears during stoppage
(shows on-screen as e.g. "45+ 2:36" where 2:36 is `STOPPAGE_CLOCK`).

**Step 6: Timestamp verification.**

```go
tsv := s.vision.VerifyTimestamp(input.APIElapsed, input.APIExtra, vr.ClockExtractions)
if tsv.Verified {
    // Attach to validated result
    validated[i].timestampVerified = true
    validated[i].extractedMinute = *tsv.ExtractedMinute
} else {
    // Kept but marked unverified — audit §4 scoped dedup pool
    validated[i].timestampVerified = false
}
```

**Smart stoppage OCR correction** lives in `VerifyTimestamp`. When the
vision model reads "02:36" (thinking it's the stoppage sub-clock, so
it's the minute:second part after the 45+) but the actual absolute
minute should be 92 (45+47 with stoppage), the naive parse gets 2.
Retry with `api_elapsed + parsed` = 90+2 = 92 → within ±3 of API's
90+2 = 92 → verified. Documented in `vision.Service.VerifyTimestamp`
comments.

**Step 7: Parallel dHash generation.**

Runs per-video via `workflow.Go`. Each activity dense-samples at 0.25s
intervals + heartbeats every 5 frames.

```go
var hashFutures []workflow.Future
for i, v := range validated {
    fut := workflow.ExecuteActivity(
        ctx,
        activity.GenerateVideoHash,
        v.outcome.result.FilePath,
        v.outcome.result.Duration,
    )
    hashFutures = append(hashFutures, fut)
}

for i, fut := range hashFutures {
    var h vision.DenseHashSamples
    if err := fut.Get(ctx, &h); err != nil {
        recordFailureClass(err)
        continue
    }
    validated[i].hashSamples = h
}
```

The hash activity has `heartbeat_timeout: 60s`, `start_to_close: 300s`
(some very long clips can take a couple of minutes). Heartbeats
every 5 frames prove liveness.

**Step 8: Signal UploadWorkflow.**

Always — even for empty batches. Audit §7 lived problem: empty-batch
suppression was how events got stuck at `download_complete=false`
forever.

```go
batch := UploadBatchSignal{
    BatchID: uuid.New(),
    Files:   materializeUploadFiles(validated),
}

uploadWorkflowID := fmt.Sprintf("upload-%s", input.EventID.String())
_, err := workflow.SignalExternalWorkflow(
    ctx,
    uploadWorkflowID, "",  // empty runID = current run
    "add_videos",
    batch,
).Get(ctx, nil)
```

Wait — that's WRONG. The FIRST DownloadWorkflow for an event has no
UploadWorkflow to signal-to yet. Need `SignalWithStartWorkflow`:

```go
// Actual pattern
c := temporal.MustClientFromWorkflowCtx(ctx)
_, err := c.SignalWithStartWorkflow(
    ctx,
    uploadWorkflowID,       // deterministic per event
    "add_videos", batch,    // signal name + payload
    client.StartWorkflowOptions{
        TaskQueue: cfg.TaskQueue,
        WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
    },
    workflow.UploadWorkflow,
    workflow.UploadWorkflowInput{EventID: input.EventID, FixtureID: input.FixtureID},
)
```

Signals FIFO on delivery. Multiple DownloadWorkflows racing to signal
just queue their batches; UploadWorkflow processes them in order.

**Step 9: Update event telemetry + registration outcome.**

```go
// Update the registration row's outcome_class with what we observed
outcomeClass := classifyBatchOutcome(validated, len(input.Videos))
_, _ = workflow.ExecuteActivity(
    ctx,
    activity.RegisterEventDownloadWorkflow,
    input.EventID, workflowID, &outcomeClass,  // updates existing row
).Get(ctx, nil)

// Merge failure-class counters into event telemetry
_, _ = workflow.ExecuteActivity(
    ctx,
    activity.UpdateEventTelemetry,
    input.EventID,
    event.TelemetryPatch{
        SearchAttemptsInc:              0,
        VideosDiscoveredInc:            0,
        VideosDownloadedInc:            len(validated),
        DownloadFailureClassesInc:      failureCounters,
        ValidationPassRateSample:       &passRate,
    },
).Get(ctx, nil)
```

`outcomeClass` values: `"success"`, `"empty_after_filter"`,
`"empty_after_validation"`, `"all_geo_restricted"`,
`"all_downloads_failed"`, `"llm_unavailable"`, etc.

### UploadWorkflow per-batch processing

Details of what happens inside the `for` loop from §5's UploadWorkflow
spec.

**Step a: Atomic hash dedup.**

For each file in the batch:

```go
asset, wasCreated, err := workflow.ExecuteActivity(
    ctx,
    activity.UpsertVideoAssetWithHashDedup,
    input.FixtureID,
    file.PerceptualHash, file.PerceptualHashPrefix, file.MD5,
    file.Width, file.Height, file.DurationMs, file.FileSizeBytes,
).Get(ctx, &asset)
```

Under the hood: single Postgres round-trip.

```sql
INSERT INTO video_assets (
    id, fixture_id, s3_bucket, s3_key,
    perceptual_hash, perceptual_hash_prefix, md5,
    width, height, duration_ms, file_size_bytes,
    popularity, first_seen_at
) VALUES ($1, $2, $3, computed_key, $4, $5, $6, $7, $8, $9, $10, 1, NOW())
ON CONFLICT (fixture_id, perceptual_hash) DO UPDATE
    SET popularity = video_assets.popularity + 1
RETURNING id, popularity, xmax = 0 AS was_created;
```

`xmax = 0` is a Postgres idiom for "was this row newly inserted (0) or
just updated (nonzero)." Returned to the caller as `wasCreated`.

Atomic. Two concurrent DownloadWorkflows uploading byte-identical clips
both call this; one wins the INSERT, the other's ON CONFLICT bumps
popularity. Both get back the winner's UUID.

**Step b: S3 upload OR local delete.**

```go
if wasCreated {
    // Upload the local temp file to S3
    _, err := workflow.ExecuteActivity(
        ctx,
        activity.UploadFileToS3,
        asset.ID, file.LocalPath,
    ).Get(ctx, nil)
} else {
    // Asset already in S3; delete local temp file
    _, err := workflow.ExecuteActivity(
        ctx,
        activity.DeleteLocalTempFile, file.LocalPath,
    ).Get(ctx, nil)
}
```

**Step c: Mint share + recalculate ranks atomically.**

```go
var result video.MintResult
_, err := workflow.ExecuteActivity(
    ctx,
    activity.MintShareAndRecalculateRanks,
    asset.ID, input.EventID,
    file.TimestampVerified, file.ExtractedMinute,
).Get(ctx, &result)
// result.ShareID, result.ComputedRank
```

Share ID format: `s_<12-hex>` — public, forever-stable, decoupled from
S3 key (audit §4 URL-stability invariant).

Inside the activity: `video.ShareService.MintShareAndRecalculate` runs a
REPEATABLE READ transaction with retry-on-serialization-failure (from
`infra/pg.WithRetryableTx`):

1. `SELECT ... FROM video_shares WHERE event_id = ? AND state = 'active'
   ORDER BY (timestamp_verified DESC, popularity DESC, file_size_bytes DESC)
   FOR UPDATE` — locks the event's current active set.
2. Compute where the new share sorts against that set (via a live JOIN
   against `video_assets` to resolve popularity + file_size_bytes for the
   incoming asset).
3. INSERT the new `video_shares` row at its computed rank (always `>= 1`,
   so the `CHECK` constraint always passes on first try).
4. UPDATE displaced ranks in place. The partial UNIQUE INDEX
   (`ON (event_id, rank) WHERE state = 'active'`) makes the write set
   consistent by construction — any temporary rank collision during the
   UPDATE round would violate the index and roll back.
5. COMMIT. If a concurrent transaction on the same event racedd this one,
   the second COMMIT fails with `SerializationFailure`, `WithRetryableTx`
   re-executes from step 1 against the now-updated state, and either
   succeeds or gives up after 3 attempts.

The 2026-06-30 0-0-2-3 rank bug is physically unrepresentable — no path
INSERTs a rank<1 row, and no two active shares can share a rank per event.

**Why atomic Mint+Recalculate instead of two separate activities:** the
schema's `CHECK (rank >= 1)` is not deferrable, so a "mint at 0, recompute
later" flow would fail at INSERT time. Even if the check were deferrable,
splitting them means an activity failure between the two would leave a
share at a placeholder rank until the next mint on that event. Folding
them into one transaction eliminates both failure modes.

**Step d: Emit semantic event.**

```go
_, _ = workflow.ExecuteActivity(
    ctx,
    activity.EmitSemanticEvent,
    "event.video_ready",
    eventlog.Payload{
        EventID:      input.EventID.String(),
        ShareID:      result.ShareID,
        FixtureID:    input.FixtureID,
        Rank:         result.ComputedRank,
        PlayerName:   playerName,  // from event lookup
        Minute:       eventMinute,
    },
).Get(ctx, nil)
```

Inside the activity — the dual-write pattern from §9's `event` package:

```
INSERT INTO event_log (event_type, payload, ...) VALUES (...);
nats.Publish("event.video_ready", payload);
```

The `event_log` INSERT is the durable audit + SSE-reconnect backfill source.
The `nats.Publish` is the realtime fan-out — SSE handlers in `internal/api`
Subscribe to the NATS `event.>` subjects and forward to connected clients;
the webhook delivery worker (a JetStream durable consumer) receives and
POSTs to subscribers. NATS publish failure does NOT roll back the
event_log commit — SSE reconnect backfills from `event_log`, and the
`dual_write_skew_total{event_type}` counter fires to make the drop visible.

**After all files in batch:**

```go
// Try flag download_complete atomically (respects the 10-count threshold)
flipped, err := workflow.ExecuteActivity(
    ctx,
    activity.TryFlagDownloadComplete,
    input.EventID, 10,
).Get(ctx, &flipped)

if flipped {
    _, _ = workflow.ExecuteActivity(
        ctx,
        activity.NotifyEventLog,
        "event.download_complete",
        eventlog.Payload{EventID: input.EventID.String(), FixtureID: input.FixtureID},
    ).Get(ctx, nil)
}
```

### Deduplication semantics summary

Three dedup layers, all with different scopes:

| Layer | Scope | Implementation | Where |
|---|---|---|---|
| MD5 batch dedup | Within one DownloadWorkflow batch | Map by MD5 in Go | `DownloadWorkflow` step 4 |
| Fixture-wide perceptual dedup | Cross-batch, cross-event, cross-instance | `UNIQUE (fixture_id, perceptual_hash)` + INSERT...ON CONFLICT | `UpsertVideoAssetWithHashDedup` activity |
| Per-event rank ordering | Within a single event's shares | Partial UNIQUE INDEX + serializable transaction | `MintShareAndRecalculateRanks` activity |

The fixture-wide layer is what audit §4 introduced. Prevents the
storage bloat + popularity-vote dilution + cross-event miss + cross-
instance race problems from the current Mongo design.

### Concurrency + timing

**LLM cap enforcement.** `internal/infra/llm` maintains a per-worker-process
`sync.Semaphore(2)` around `ChatCompletion` and `ChatCompletionMultiImage`.
With N worker replicas, that's up to 2N concurrent LLM calls fleet-wide.
Not ideal — joi's actual cap is 2 fleet-wide. Audit §6 Track 1 workspace
LLM gateway would fix this. Deferred pending nexus timing.

Until then, worker registration uses
`MaxConcurrentActivityExecutions: 2` specifically on the
`ValidateVideoIsSoccer` activity (and `GetOrResolveTeamAliases` in
alias domain, which also calls LLM). Semaphore + registration option
together enforce the cap per-process.

**ffmpeg subprocess cap.** `infra/ffmpeg.MaxProcesses` (default 4)
prevents CPU fork-bomb. Frame extraction blocks at the semaphore when
saturated. Applies within one worker process.

**Postgres pool.** `pgxpool` size 25 per worker. Activities are short;
contention should be minimal even under peak load.

**S3 throughput.** `aws-sdk-go-v2` default transport pool. Not observed
as a bottleneck against Garage on the same docker network — sub-ms
round-trips.

### Failure classification per activity

Every download-pipeline activity that can fail records a typed
`error_class` string into `event.telemetry.download_failure_classes`
via `UpdateEventTelemetry` at step 9. The taxonomy from §5 applies:

| Activity | Common classes | Retry-eligibility |
|---|---|---|
| `DownloadVideo` | `syndication.geo_restricted`, `syndication.not_found`, `syndication.deleted`, `syndication.timeout`, `syndication.url_malformed` | timeout retries; others don't |
| `ExtractFramesForValidation` | `ffmpeg.timeout`, `ffmpeg.input_corrupted`, `ffmpeg.probe_failed` | timeout retries; others don't |
| `ValidateVideoIsSoccer` | `llm.unavailable`, `llm.timeout`, `llm.cap_exceeded`, `llm.bad_response` | first three retry; bad_response doesn't |
| `GenerateVideoHash` | `ffmpeg.timeout`, `hash.compute_failed` | timeout retries |
| `UpsertVideoAssetWithHashDedup` | `pg.transient_infra`, `pg.duplicate_key` (== hash dedup hit — not really an error, but classified for observability) | transient retries |
| `UploadFileToS3` | `s3.timeout`, `s3.unreachable`, `s3.access_denied` | timeout + unreachable retry; access_denied is a config bug |

Distribution of these across an event's 10 download workflows is the
post-fixture "why did we miss coverage" answer, direct from
`event.telemetry` without Loki archaeology.

### Testing shape

**Workflow tests:**

```go
// internal/workflow/download_test.go
func TestDownloadWorkflow_HappyPath_ThreeVideos_TwoValidated(t *testing.T) {
    ts := &testsuite.WorkflowTestSuite{}
    env := ts.NewTestWorkflowEnvironment()

    env.OnActivity(activity.RegisterEventDownloadWorkflow, mock.Anything, ...).Return(1, nil).Times(2)
    env.OnActivity(activity.CheckEventStillLive, mock.Anything, ...).Return(true, nil)
    env.OnActivity(activity.DownloadVideo, mock.Anything, "url1").Return(sampleDownload("mp4-a"), nil)
    env.OnActivity(activity.DownloadVideo, mock.Anything, "url2").Return(nil, syndication.ErrGeoRestricted)
    env.OnActivity(activity.DownloadVideo, mock.Anything, "url3").Return(sampleDownload("mp4-b"), nil)

    env.OnActivity(activity.ExtractFramesForValidation, mock.Anything, ...).Return(sampleFrames(), nil).Times(2)
    env.OnActivity(activity.ValidateVideoIsSoccer, mock.Anything, ...).Return(acceptedVerdict(), nil).Times(2)
    env.OnActivity(activity.GenerateVideoHash, mock.Anything, ...).Return(sampleHash(), nil).Times(2)
    env.OnActivity(activity.SignalUploadBatch, mock.Anything, ...).Return(nil)
    env.OnActivity(activity.UpdateEventTelemetry, mock.Anything, ...).Return(nil)

    env.ExecuteWorkflow(workflow.DownloadWorkflow, sampleInput(3))
    require.True(t, env.IsWorkflowCompleted())

    var out workflow.DownloadWorkflowOutput
    require.NoError(t, env.GetWorkflowResult(&out))
    require.Equal(t, 2, out.VideosDownloaded)
    require.Equal(t, 2, out.ValidatedSoccer)
    require.Equal(t, 1, out.FailureClasses["syndication.geo_restricted"])
}
```

**Vision domain unit tests** (colocated with domain):

```go
func TestValidateFrames_TwoFramesAgreeSoccer_NoTiebreaker(t *testing.T)
func TestValidateFrames_TwoFramesDisagree_CallsTiebreaker(t *testing.T)
func TestValidateFrames_LLMCapExceeded_ReturnsRetryableErr(t *testing.T)
func TestVerifyTimestamp_WithinTolerance_Verified(t *testing.T)
func TestVerifyTimestamp_StoppageOCRCorrection_Verifies(t *testing.T)
func TestVerifyTimestamp_OutsideAllTolerances_NotVerified(t *testing.T)
```

**Integration tests with sample video files:**

`test/integration/download_pipeline_test.go` runs actual ffmpeg
against `testdata/samples/goal_short.mp4` +
`testdata/samples/phone_recording.mp4` +
`testdata/samples/wrong_minute.mp4` to exercise the full
extract → hash → validate → verify path against a fake LLM (which
returns pre-registered responses per image hash).

### Extensibility

**Embedding-based dedup (audit §4 Track 3):**

Replace `perceptual_hash` + `perceptual_hash_prefix` columns with an
`embedding vector(768)` column. `UpsertVideoAssetWithHashDedup`
becomes `UpsertVideoAssetWithEmbeddingDedup`, using
`ORDER BY embedding <=> $1 LIMIT 1` to find the nearest neighbor and
merging if similarity is > threshold.

No workflow rewrites — the activity signature changes but the workflow
call is the same. Existing rows keep `perceptual_hash` for backward
compat; new rows populate `embedding`. Migration is a background
backfill task.

**Additional validation prompt (e.g., celebration vs replay classification):**

Extend `vision.Service.ValidateFrames` prompt to include additional
structured fields:

```json
{
  "frame_1": {
    "SOCCER": "yes",
    "SCREEN": "no",
    "CLOCK": "45:23",
    "ADDED": "+2",
    "STOPPAGE_CLOCK": "",
    "SCENE_TYPE": "goal_moment|celebration|replay"
  }
}
```

`ValidationResult` grows a `SceneType` field. Rank recalculation can
factor in "prefer goal_moment scenes." Additional field is optional
in the struct; parsing is backward-compatible with older responses
that don't include it.

**Additional video source (e.g., broadcaster CDN direct):**

The `syndication.Client.DownloadVideo` interface can be reused for any
CDN. Add `infra/broadcastercdn/` adapter implementing the same
signature; DownloadWorkflow's step 3 tries multiple adapters in order:
syndication first, broadcasterCDN fallback if syndication returns
`ErrGeoRestricted`. This connects to audit's geo-restriction-bypass
proposal.

---

## 11. Observability

Full observability discipline: what to emit, how to structure it, how
to query it, how to view it, when to alert. The current Python stack
has decent Loki + Grafana coverage but the taxonomy is inconsistent
and the viewing surfaces are noisy — the rebuild uses this section to
lock in structural discipline from day one.

### The four pillars

Every observable behavior falls into one of four categories:

| Pillar | What it captures | Backend | Consumers |
|---|---|---|---|
| **Logs** | Discrete events with structured context | Loki (via Promtail from container stdout) | Engineers debugging incidents |
| **Metrics** | Numerical time-series counters and gauges | Prometheus (scrapes `/metrics` endpoints) | Alerts, capacity planning, SLO dashboards |
| **Traces** | Causal chains of activity spans across services | Not implemented day one — see extensibility | Deep-dive perf work (audit §5 concurrency debugging) |
| **Semantic event stream** | Business events (event.detected, event.video_ready) | Postgres `event_log` table (§3) | SSE fan-out, webhook delivery, audit trail |

Pillars 1-3 are engineering-facing. Pillar 4 (the semantic event stream)
serves both observability AND product delivery (SSE + webhooks) — that
dual role means the same INSERT into `event_log` powers both the
frontend real-time updates AND the "what happened to this event"
audit query.

### Design principles

**1. Taxonomy is a compile-time contract, not a convention.** The
current Python code emits log lines with free-form `module`, `action`,
`level` strings — typos, inconsistent casing, one-off values sneak in.
In Go, every log emission uses `logging.Emit(level, module, action,
msg, fields...)` where `module` and `action` are typed enums from
package `logging/vocabulary`. Compile-time-checked. New modules and
actions are one-line additions to the vocabulary file; forgetting to
add one is a compile error, not a runtime "huh why isn't this indexed
in Loki."

**2. Every log line, every metric, every event has a canonical
schema.** Log lines have exactly one shape (defined below). Metrics
follow Prometheus naming conventions strictly. Event stream payloads
are Pydantic-equivalent Go structs, serialized consistently. No
ad-hoc key/value pairs anywhere.

**3. The log catalog is discoverable.** A generated markdown file
`docs/log-catalog.md` (regenerated on every build via `go generate`)
lists every possible (module, action) pair with its expected field
set and log-level guidance. This is the "clear view of what types of
logs exist" that's been missing.

**4. Metrics come from the SAME emissions as logs.** Every
`logging.Emit(...)` optionally also increments a Prometheus counter
labeled by (module, action, level). No parallel instrumentation
paths — one call site, both signals emerge.

**5. Multiple viewing surfaces for different consumers.** Not
everyone wants LogQL. Grafana dashboards for engineers, an
operational summary panel exposed via vedanta-systems for
at-a-glance, a CLI tool (`ff logs`) for local debugging. All read
the same underlying data.

**6. Alerts fire on SLO violations, not raw errors.** A single
ERROR-level log line doesn't page anyone. Alerts trigger on rate
thresholds (coverage rate < X% for 1h, error class Y exceeds baseline
by Z stddev, prod commit drift > 24h from main). Rules are code +
version-controlled + reviewed like everything else.

**7. Observability code is a first-class package, not scattered
imports.** All emission goes through `internal/logging`. Adapters
(§9) emit via the same package. Domain services emit via the same
package. Activities emit via the same package. One import path;
one set of test doubles.

### Log-line canonical schema

Every log line is a structured JSON object with a fixed field set:

```json
{
  "ts": "2026-07-02T14:23:45.123456Z",
  "level": "INFO",
  "module": "download",
  "action": "video_downloaded",
  "msg": "video downloaded and staged",
  "trace_id": "01hxyz...",
  "workflow_id": "download-03-e_a1b2c3d4e5f6",
  "activity_id": "act_9x8y7z",
  "event_id": "e_a1b2c3d4e5f6",
  "fixture_id": 1562345,
  "duration_ms": 3140,
  "extras": {
    "video_url": "https://x.com/user/status/...",
    "file_size_bytes": 15083919,
    "resolution": "1280x720",
    "bitrate_kbps": 2400
  }
}
```

**Base fields (present on EVERY log line):**

- `ts` — RFC3339Nano UTC timestamp
- `level` — one of `DEBUG`, `INFO`, `WARN`, `ERROR`. Enum-typed in code.
- `module` — the emitting module (see vocabulary below). Enum-typed.
- `action` — what happened within the module. Enum-typed per-module.
- `msg` — human-readable one-liner
- `trace_id` — request/workflow correlation ID (see §11 tracing subsection)
- `container` — auto-injected by Promtail from Kubernetes/Docker metadata

**Context fields (present when applicable, standardized names):**

- `workflow_id` — Temporal workflow execution ID
- `activity_id` — Temporal activity execution ID (also auto-attached to spans)
- `event_id` — UUID from §3, when the log line relates to a specific event
- `fixture_id` — bigint from §3, when related to a fixture
- `video_asset_id`, `video_share_id` — UUIDs from §3
- `duration_ms` — for operations with measurable duration
- `error_class` — typed error class name (from §5 taxonomy), when logging an error
- `error_message` — the error's `Error()` string; not searched, just for humans

**Free-form context: `extras` object.** Anything domain-specific goes
under a nested `extras` object. Everything at the top level is
standardized; nothing at the top level is free-form. This is what
makes queries reliable — you know `event_id` is always spelled
`event_id`, not `eventId` or `event.id` or `evtId`.

### The vocabulary package

`internal/logging/vocabulary/vocabulary.go` — the source of truth for
what modules and actions exist:

```go
package vocabulary

type Module string
type Action string

const (
    // Workflows
    ModuleIngestWorkflow    Module = "ingest_workflow"
    ModuleMonitorWorkflow   Module = "monitor_workflow"
    ModuleDiscoveryWorkflow Module = "discovery_workflow"
    ModuleDownloadWorkflow  Module = "download_workflow"
    ModuleUploadWorkflow    Module = "upload_workflow"

    // Domain services (matching §4)
    ModuleFixture      Module = "fixture"
    ModuleEvent        Module = "event"
    ModuleVideo        Module = "video"
    ModuleAlias        Module = "alias"
    ModuleDiscovery    Module = "discovery"
    ModuleVision       Module = "vision"
    ModuleSession      Module = "session"
    ModuleTextAnalysis Module = "text_analysis"

    // Infrastructure adapters (matching §9)
    ModuleInfraPG           Module = "infra_pg"
    ModuleInfraS3           Module = "infra_s3"
    ModuleInfraLLM          Module = "infra_llm"
    ModuleInfraTemporal     Module = "infra_temporal"
    ModuleInfraAPIFootball  Module = "infra_apifootball"
    ModuleInfraTwitter      Module = "infra_twitter"
    ModuleInfraSyndication  Module = "infra_syndication"
    ModuleInfraFFmpeg       Module = "infra_ffmpeg"
    ModuleInfraWikidata     Module = "infra_wikidata"

    // Cross-cutting
    ModuleAPI        Module = "api"
    ModuleAPI_SSE    Module = "api_sse"
    ModuleWebhookDelivery Module = "webhook_delivery"
    ModuleScaler     Module = "scaler"
    ModuleWorker     Module = "worker"       // bootstrap
    ModuleMigration  Module = "migration"    // startup
    ModuleHealthz    Module = "healthz"
    ModuleDeploy     Module = "deploy"       // deploy-marker log lines
)

// ValidModules is used at startup for self-verification.
var ValidModules = []Module{
    ModuleIngestWorkflow, ModuleMonitorWorkflow, /* ... */
}
```

Actions live per-module (or per-family) in separate files for
manageability:

```go
// vocabulary/actions_video.go
const (
    ActionVideoUploadStarted   Action = "upload_started"
    ActionVideoUploadCompleted Action = "upload_completed"
    ActionVideoUploadFailed    Action = "upload_failed"
    ActionVideoHashComputed    Action = "hash_computed"
    ActionVideoDedupHit        Action = "dedup_hit"
    ActionVideoDedupMiss       Action = "dedup_miss"
    ActionVideoRankRecalculated Action = "rank_recalculated"
    /* ... */
)
```

**Type safety.** `logging.Emit` accepts only `Module` and `Action`
types — passing a raw string is a compile error:

```go
// This compiles:
logging.Emit(logging.INFO, vocabulary.ModuleVideo, vocabulary.ActionVideoDedupHit, "hash matched existing asset", ...)

// This doesn't:
logging.Emit(logging.INFO, "video", "dedup_hit", "...", ...)
//                          ^^^^^^^ untyped literal → compile error
```

**Adding a new module or action** is a one-line addition to
`vocabulary/*.go` + regenerate the log catalog. Compile-checked
everywhere.

### The log catalog

`docs/log-catalog.md` — regenerated on every build via `go generate`
from the vocabulary package + reflection over call sites:

```markdown
# Log Catalog (auto-generated 2026-07-02 15:23 UTC)

## Module: `video`

### Action: `upload_started` (INFO)
Fields:
  - event_id (uuid) — required
  - video_asset_id (uuid) — required
  - file_size_bytes (int) — required
Call sites:
  - internal/domain/video/service.go:214

### Action: `dedup_hit` (INFO)
Fields:
  - fixture_id (int64) — required
  - perceptual_hash_prefix (int32) — required
  - existing_asset_id (uuid) — required
  - new_popularity (int) — required
Call sites:
  - internal/domain/video/service.go:167
...
```

This is the "clear view of what types of logs there are" — every
possible log line, its field contract, and where in the code it comes
from. Reviewable in PRs when call sites change.

### Level guidance

Encoded as vocabulary metadata (`ActionMeta.MinLevel`) and enforced at
emission time.

**DEBUG** — Fine-grained internal state useful for local development.
Off in prod by default (Promtail drops based on the `level` label).
Example: per-frame dHash computation progress, per-token LLM streaming.

**INFO** — Normal operation milestones. Every activity boundary
(started/completed/failed) is INFO. State transitions are INFO. High-
level "here's what the system is doing" without noise.

**WARN** — Recoverable degradation. Fallback taken, retry incoming,
best-effort operation skipped. Alerts DO NOT fire on individual WARN
lines; they fire on rate thresholds.

**ERROR** — Operation could not complete, no recovery possible in this
attempt. Not the same as "the system is broken" — a video failed to
download is an ERROR for that video but the pipeline is fine.

**FATAL** (not used). Panicking + exiting cleanly is what SIGTERM +
recover chain does. No FATAL logging level.

Vocabulary metadata enforces min-level per action:

```go
var actionLevelPolicy = map[Action]Level{
    ActionVideoDedupHit:     INFO,   // always visible
    ActionVideoDedupMiss:    INFO,
    ActionVideoUploadFailed: ERROR,  // always ERROR
    ActionLLMCallStreaming:  DEBUG,  // hidden in prod
}
```

Passing a wrong level triggers a warning at emit time (during test,
this fails the test).

### Metrics — Prometheus naming and semantics

Every metric follows the naming convention:

```
found_footy_<module>_<measure>{<labels>}
```

Standard measures:

| Measure suffix | Type | Meaning |
|---|---|---|
| `_total` | Counter | Cumulative count (never resets except on restart) |
| `_active` | Gauge | Current count (e.g. active workflows) |
| `_duration_seconds` | Histogram | Duration in seconds, standard buckets |
| `_bytes` | Histogram | Size in bytes |
| `_ratio` | Gauge | 0.0-1.0 value (SLO ratios) |

Standard labels (never varies in name):
- `module` — from the vocabulary
- `action` — from the vocabulary
- `outcome` — one of `success`, `failure`, `retry`
- `error_class` — the typed error class if failure, else empty
- `level` — for log-derived counters

**Baseline metrics every binary exposes** (via `/metrics`):

- `found_footy_calls_total{module, action, outcome, error_class}` — counter derived from log emissions
- `found_footy_log_lines_total{module, level}` — counter derived from log emissions
- Go runtime metrics (goroutines, GC, memory) — from `prometheus/client_golang`
- Process metrics (CPU, RSS, open FDs) — from `prometheus/client_golang`
- HTTP handler metrics for the `api` binary (request duration histogram, in-flight gauge, per-endpoint counter)

**Business SLO metrics:**

- `found_footy_events_detected_total{league, event_type}` — counter
- `found_footy_events_video_captured_total{league}` — counter
- `found_footy_coverage_ratio{league}` — gauge, `captured / detected` per hour
- `found_footy_time_to_first_s3_seconds{league}` — histogram, event.first_seen → first_s3_upload
- `found_footy_ingest_freshness_seconds` — gauge, "seconds since main was last built into prod image"

**Infrastructure health metrics:**

- `found_footy_pg_pool_active` — gauge
- `found_footy_pg_query_duration_seconds{action}` — histogram
- `found_footy_s3_upload_duration_seconds{bucket}` — histogram
- `found_footy_llm_call_duration_seconds{model, endpoint}` — histogram
- `found_footy_llm_concurrent_calls` — gauge (peers at joi's cap of 2)
- `found_footy_llm_cap_exceeded_total{endpoint}` — counter
- `found_footy_twitter_search_duration_seconds{instance}` — histogram
- `found_footy_twitter_fleet_healthy_instances` — gauge
- `found_footy_twitter_cookies_age_seconds` — gauge, from `twitter_sessions.updated_at`
- `found_footy_twitter_consecutive_auth_failures{instance}` — gauge

**Deploy tracking metrics** (audit §1):

- `found_footy_deploy_git_sha_info{binary, git_sha, image_tag, built_at}` — info gauge (value always 1); label carries deploy identity
- `found_footy_deploy_age_seconds` — gauge, `now - built_at`
- `found_footy_deploy_drift_commits` — gauge, main HEAD's commit count ahead of what prod is running (calculated by a small `scripts/deploy-tracker.sh` that runs on a cron and pushes to Pushgateway)

### Grafana dashboard organization

Committed as JSON in `deploy/grafana/dashboards/` and provisioned via
Grafana's file-based dashboard provisioning.

**Dashboards:**

**`found-footy-overview`** — the "at-a-glance is the system healthy"
dashboard. Owner: engineers checking in.
- Coverage ratio (last hour) per league — 6 stat panels
- Active workflows by type — timeline
- Error rate by class — timeline
- Deploy freshness — stat panel with alert-eligible threshold (red if > 24h)
- Twitter fleet health — instance count healthy/draining/unhealthy
- LLM cap-exceeded rate — timeline with joi's cap as annotation
- Top 10 events with zero videos (last 24h) — table

**`found-footy-fixture-drilldown`** — click a fixture ID, see everything
about it. Owner: engineers debugging a specific match.
- Fixture header (teams, kickoff, current status, activated_at, completed_at)
- Events table with per-event: detection time, monitor workflows count, download workflows count, download_complete flag, current s3 videos count, telemetry snippet
- Timeline of all workflow spawns for this fixture
- All log lines matching `fixture_id` — Loki panel
- All metrics filtered to this `fixture_id` where applicable

**`found-footy-fleet-health`** — the twitter + LLM fleet view.
Owner: engineers investigating discovery/validation issues.
- Twitter instance grid: per instance health, last search latency, in-flight count, cookies_version, drain flag
- Session state: current cookies_version, consecutive auth failures fleet-median, estimated expiry
- LLM endpoint: concurrent calls gauge (annotated with joi's cap), latency histogram per model, cap-exceeded counter
- FFmpeg subprocess: active count, queue depth

**`found-footy-slo`** — the "are we hitting our targets" dashboard.
Owner: product/planning.
- Weekly coverage rate per league
- P50/P95 time-from-goal-to-first-S3
- Trend over rolling 30d
- Per-error-class breakdown as stacked area
- Alert firing history

**`found-footy-deploy`** — deploy tracking + drift visibility (audit §1).
Owner: operators.
- Prod image git_sha vs main HEAD — commit distance
- Deploy history (last N restarts) — annotations timeline
- Prod deploy age (`built_at` seconds ago)
- Alert on drift > 7 days

### Canonical Loki queries

The pain-point today: "I need to write LogQL from scratch every time I
investigate an incident." Solved by canonical queries baked into the
Grafana dashboards AND documented in `docs/logging.md` (or its rebuild
equivalent).

**Common investigation shapes:**

```logql
# "What happened to this event?"
{container=~"found-footy-.*"} | json | event_id = "e_a1b2c3d4e5f6"

# "Why is this fixture stuck?"
{container=~"found-footy-.*"} | json | fixture_id = "1562345"
  | level =~ "WARN|ERROR"

# "Which matches missed coverage this week?"
{container=~"found-footy-.*", module="monitor_workflow", action="match_completed_summary"}
  | json | coverage_rate < 0.5 | line_format "{{.league_name}} {{.home_team}} v {{.away_team}} — {{.coverage_rate}}"

# "Deploy freshness — when did prod last restart?"
{container=~"found-footy-prod-.*", module="deploy", action="startup"} | json
  | line_format "{{.container}} @ {{.git_sha}} built {{.built_at}}"

# "Twitter auth failures fleet-wide"
{container=~"found-footy-.*", module="infra_twitter", action="auth_failed"} | json
  | rate[5m]
```

These are `.json` files at `deploy/grafana/loki-queries/*.json`
importable into Grafana's saved queries.

### Prometheus alerting rules

Rules live at `deploy/prometheus/rules/found-footy.yaml`. Alerts have
a `severity` label (`info`/`warn`/`critical`) and go to different
routes in Alertmanager.

**Rule examples (concrete):**

```yaml
groups:
- name: found-footy-slo
  interval: 1m
  rules:
  - alert: FoundFootyCoverageDrop
    expr: |
      sum by (league) (rate(found_footy_events_video_captured_total[1h]))
      /
      sum by (league) (rate(found_footy_events_detected_total[1h]))
      < 0.5
    for: 30m
    labels:
      severity: warn
    annotations:
      summary: "Coverage rate for {{ $labels.league }} below 50% for 30min"

  - alert: FoundFootyDeployDrift
    expr: found_footy_deploy_drift_commits > 5
    for: 24h
    labels:
      severity: warn
    annotations:
      summary: "Prod is {{ $value }} commits behind main HEAD for 24h+"

  - alert: FoundFootyLLMEndpointDown
    expr: found_footy_llm_calls_total{outcome="failure", error_class="llm.unavailable"} > 5
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "LLM endpoint unavailable for 5min+"

  - alert: FoundFootyTwitterAuthFailing
    expr: sum(found_footy_twitter_consecutive_auth_failures) > 6
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "Twitter fleet auth failures across the board — cookies stale"

  - alert: FoundFootyPGPoolExhausted
    expr: found_footy_pg_pool_active / found_footy_pg_pool_size > 0.9
    for: 2m
    labels:
      severity: warn
```

Alerts route to ntfy topics (per user's self-hosted preference —
Pushover/Slack/etc. are options, but ntfy fits the local-first stack).

### Deploy tracking (audit §1)

The load-bearing observability piece. Everyone underestimates it until
they've been bitten by the 7-week-stale-image scenario. The rebuild
bakes it in.

**Every binary emits a deploy-marker log line at startup:**

```json
{
  "ts": "2026-07-02T15:00:12Z",
  "level": "INFO",
  "module": "deploy",
  "action": "startup",
  "msg": "binary starting",
  "container": "found-footy-prod-worker-1",
  "binary": "worker",
  "git_sha": "4a68493abc123...",
  "image_tag": "2026-07-02T14:55:00Z",
  "built_at": "2026-07-02T14:55:00Z",
  "go_version": "go1.23.4",
  "compile_flags": {...}
}
```

`git_sha` and `built_at` are baked into the binary at build time via
`-ldflags "-X main.gitSHA=... -X main.builtAt=..."`. No env vars, no
runtime lookups.

**Corresponding metric:** `found_footy_deploy_git_sha_info{binary, git_sha, image_tag, built_at}` — gauge with value 1 whose labels carry the identity. Standard Prometheus pattern.

**Drift calculation:** a small `scripts/deploy-tracker.sh` cron job on
luv (every 15 min) does:

```bash
LOCAL_HEAD=$(git -C ~/workspace/dev/found-footy rev-parse main)
PROD_SHA=$(curl -s http://found-footy-prod-worker-1:8080/healthz | jq -r '.git_sha')
DRIFT=$(git -C ~/workspace/dev/found-footy rev-list --count $PROD_SHA..$LOCAL_HEAD)
push_pushgateway "found_footy_deploy_drift_commits" $DRIFT
```

Grafana + alert rules pick up the metric.

### Viewing surfaces beyond Grafana

Grafana isn't the only or even the primary interface. Three additional
surfaces:

**1. `ff` CLI tool** — `cmd/ff/main.go` — for local dev + operator use.

```bash
ff logs --event e_a1b2c3d4e5f6 --tail        # streams live Loki logs
ff logs --fixture 1562345 --last 1h          # historical
ff coverage --league 39 --last 24h            # SLO check
ff deploy status                              # prod vs main comparison
ff fleet health twitter                       # twitter fleet state
ff activity list --running                    # currently-running workflows
```

Wraps LogQL queries + Prometheus queries + Postgres queries behind an
opinionated interface. No LogQL knowledge required.

**2. vedanta-systems admin dashboard** — a React view exposed at
`/admin/found-footy` inside vedanta-systems, protected by
vedanta-systems' own auth layer.
- Real-time SLO stat panel (last hour coverage)
- List of currently-active fixtures with per-fixture progress bars
- Recent alerts + acknowledge button
- Deploy freshness indicator (green/yellow/red)
- Link to Grafana for deep-dive

**3. Loki UI (raw)** — for when you need to write ad-hoc LogQL that
isn't captured by the canonical queries. This is the escape hatch.

### The observability code contract

Every emission goes through `internal/logging`:

```go
package logging

import (
    "context"
    "log/slog"
    "found-footy/internal/logging/vocabulary"
)

type Emitter interface {
    // Emit logs a structured line and increments the corresponding metric.
    Emit(ctx context.Context, level Level, module vocabulary.Module, action vocabulary.Action, msg string, fields ...Field)

    // TimedEmit logs with a duration_ms field. Returns a defer-able function
    // that emits on scope exit. Convenient for activity boundary timing.
    TimedEmit(ctx context.Context, level Level, module vocabulary.Module, action vocabulary.Action, msg string, fields ...Field) func(...Field)
}

type Field struct { Key string; Value any }

// Standard field helpers for the canonical context fields
func EventID(id uuid.UUID) Field           { return Field{"event_id", id.String()} }
func FixtureID(id int64) Field             { return Field{"fixture_id", id} }
func WorkflowID(id string) Field           { return Field{"workflow_id", id} }
func ErrorClass(cls string) Field          { return Field{"error_class", cls} }
func ErrorObj(err error) Field             { return Field{"error_message", err.Error()} }
func Duration(d time.Duration) Field       { return Field{"duration_ms", d.Milliseconds()} }
func VideoAssetID(id uuid.UUID) Field      { return Field{"video_asset_id", id.String()} }
func VideoShareID(id string) Field         { return Field{"video_share_id", id} }
// ... etc.
```

Usage everywhere:

```go
import "found-footy/internal/logging"
import "found-footy/internal/logging/vocabulary"

emitter := logging.FromContext(ctx)
emitter.Emit(ctx, logging.INFO,
    vocabulary.ModuleVideo,
    vocabulary.ActionVideoDedupHit,
    "hash matched existing asset",
    logging.EventID(eventID),
    logging.FixtureID(fixtureID),
    logging.Field{"existing_asset_id", assetID.String()},
    logging.Field{"new_popularity", 5},
)
```

Field key names for domain-specific fields are conventional (not
enum-typed) but the log-catalog generator validates them against the
declared field-set for that action. Mismatches fire warnings during
CI, so the catalog stays truthful.

### Structural discipline in tests

- Every emitted log line in a test can be asserted via
  `logging.WithTestEmitter(t)` which captures emissions.
- Test cases that assert "an ERROR was logged with error_class=X" are
  explicit and grep-able.
- The vocabulary package has a `TestVocabularyCompletenessAndConsistency`
  test that scans all `.go` files in the module for `logging.Emit(...)`
  call sites, ensures every `(module, action)` reference resolves, and
  every action's field-set matches the log catalog. Runs in CI.

### Extensibility hooks

**Adding a new module:**
1. Add `Module<Name> Module = "<snake_name>"` to `vocabulary/vocabulary.go`.
2. Add `vocabulary/actions_<name>.go` for its actions.
3. Regenerate log catalog via `go generate ./...`.
4. Update relevant Grafana dashboards.
5. Update Loki canonical queries if applicable.

Zero-touch to unrelated modules.

**Adding tracing (OpenTelemetry):**

Deferred from day one because two of three viewing surfaces (Grafana +
CLI) don't need it and it adds config surface. When we add it:
1. Add `internal/tracing` package with OTLP exporter config.
2. Wire spans at activity boundaries (Temporal already emits activity
   attempts as its own events; OpenTelemetry adds cross-service
   causality).
3. `trace_id` field is already in the log schema, so joining logs to
   traces is free.
4. Deploy Tempo/Jaeger to the monitor stack; add "traces" data source
   to Grafana.

Structural design already accommodates it — the field exists in log
lines even when spans aren't collected.

**Adding new business metrics:**

1. Declare in `internal/observability/metrics/metrics.go`.
2. Increment at the appropriate emission call site.
3. Add to dashboards as needed.

**Rate-based alerting on a new error class:**

1. New error class lands in `internal/errors` typed error registry.
2. Alert rule added to `deploy/prometheus/rules/found-footy.yaml`
   with the new error_class label match.
3. Alert history visible in Grafana.

### Migration from the current Python stack

- Existing Loki data (Python-emitted logs) stays queryable. Same
  container label pattern. Just fewer of the canonical fields
  populated (Python code didn't enforce the schema).
- Existing Grafana dashboards can stay during the migration period
  and be gradually replaced with the rebuild's versions.
- Prometheus historical data stays; new metric names live alongside
  old ones.

Fresh discipline for new emissions; no forced retconning of history.

---

## 8. Public API + SSE + webhooks

The `cmd/api` binary. Chi + Huma serving typed HTTP endpoints,
Postgres LISTEN/NOTIFY-backed SSE stream, webhook delivery worker,
share-id redirect endpoint. Composes against `internal/domain/*`
services from §4, `internal/infra/pg` + `s3` adapters from §9, and the
semantic event stream from §11.

Boundary: this section covers the consumer-facing surface (what
vedanta-systems + og-server + future consumers see). Internal cross-
service RPC isn't the subject — there is none; everything internal
goes through Temporal workflows.

### Design principles

**1. Chi + Huma from day one.** Chi provides the HTTP router (stdlib
`net/http`-compatible); Huma provides OpenAPI generation + request/
response validation from Go struct tags. Combined they match FastAPI's
"define types, get spec + validation for free" ergonomics without
Python's runtime overhead. Rejected: Fiber (uses `fasthttp`, trades
stdlib ecosystem compat), stdlib alone (routing verbose, no spec gen).

**2. Auth lives at Caddy, not in FastAPI-equivalent.** Bearer token
check in the Caddyfile ahead of the reverse-proxy step. Simplifies
`internal/api` — it assumes "if you reached me you're authorized."
The share-id redirect endpoint is the deliberate exception
(unauthenticated public share URLs).

**3. OpenAPI is the contract.** Huma emits `/api/v1/openapi.json`
from handler signatures + struct tags. vedanta-systems CI regenerates
TS types on every found-footy build (`openapi-typescript` in the
CI pipeline). Schema drift becomes a TS build error.

**4. SSE is the live push channel; webhooks are the durable
delivery channel.** Both consume the same `event_log` table via
different patterns: SSE handlers `LISTEN` on the Postgres channel;
webhook worker polls the table for unsent rows. Consumers subscribe
to whichever fits their delivery semantics.

**5. Public URLs never break** (audit §4 URL-stability invariant).
The share-id redirect endpoint is what makes this concrete —
`/api/v1/videos/s_xyz789` resolves to the *current* canonical S3
URL, regenerated on every request via presigned URLs. Assets can be
re-uploaded, migrated, superseded — the share URL doesn't change.

**6. Versioning via URL path** (`/api/v1/…`). Breaking changes go to
`v2`. Deprecation + Sunset headers per RFC 8594 on v1 endpoints during
migration windows. 6-month minimum sunset.

**7. Everything logged and metriced through §11.** Chi middleware
wires `internal/logging` into every request. Duration histogram +
error rate + in-flight gauge per endpoint. Structured log line per
request with `trace_id` for correlation with worker logs.

### Router + middleware setup

```go
// cmd/api/main.go
r := chi.NewRouter()

// Middleware stack (order matters)
r.Use(middleware.RequestID)              // sets X-Request-Id, propagates as trace_id
r.Use(middleware.RealIP)                 // trusts X-Forwarded-For from Caddy
r.Use(logging.HTTPMiddleware(emitter))   // structured log line per request
r.Use(middleware.Recoverer)              // panic → 500 with logged stack
r.Use(middleware.Timeout(60 * time.Second))
r.Use(metricsMiddleware(metrics))        // Prometheus counters + histograms
r.Use(middleware.Compress(5))            // gzip on Accept-Encoding

// Huma API instance
config := huma.DefaultConfig("Found Footy API", "v1")
config.OpenAPI.Servers = []*huma.Server{{URL: fmt.Sprintf("https://%s", cfg.PublicHostname)}}
api := humachi.New(r, config)

// Register handlers
registerFixtureHandlers(api, deps)
registerEventHandlers(api, deps)
registerVideoHandlers(api, deps)     // includes share-id redirect
registerFeedHandlers(api, deps)
registerSSEHandlers(api, deps)
registerWebhookHandlers(api, deps)
registerHealthHandlers(api, deps)

// Serve OpenAPI spec + /healthz OUTSIDE the auth wall (Caddy config knows)
// Everything else is inside (Caddy adds Bearer check before reverse_proxy)
```

### Endpoint catalog

Full inventory. Every endpoint is a Huma-generated handler with typed
input + output structs; OpenAPI spec derives from these.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/api/v1/openapi.json` | no | OpenAPI spec for TS type gen |
| GET | `/api/v1/healthz` | no | Liveness + adapter health |
| GET | `/api/v1/readyz` | no | Ready for traffic (migrations applied, adapters up) |
| GET | `/api/v1/fixtures` | yes | List with filters `date`, `state`, `league_id`, `team_id` |
| GET | `/api/v1/fixtures/{id}` | yes | Single fixture detail + embedded event summaries |
| GET | `/api/v1/fixtures/{id}/events` | yes | Events of a fixture |
| GET | `/api/v1/events/{event_id}` | yes | Single event with video-share links |
| GET | `/api/v1/events/{event_id}/videos` | yes | Just the video-share list (lighter) |
| GET | `/api/v1/videos/{share_id}` | **no** | 302 redirect to canonical S3 URL (audit §4) |
| GET | `/api/v1/feed` | yes | Recent goal events across all fixtures, paginated |
| GET | `/api/v1/sse/events` | yes | SSE stream — live event lifecycle updates |
| POST | `/api/v1/webhooks/subscriptions` | yes | Register webhook URL + event-type filter |
| GET | `/api/v1/webhooks/subscriptions` | yes | List active subscriptions |
| DELETE | `/api/v1/webhooks/subscriptions/{id}` | yes | Unsubscribe |
| GET | `/api/v1/webhooks/subscriptions/{id}/deliveries` | yes | Delivery history for observability |

**Total: 14 endpoints, mostly reads.** The write endpoints
(webhook subscriptions) are administrative.

### Auth at Caddy edge — concrete Caddyfile fragment

Concrete hostname patterns TBD at implementation time per the
[[naming-and-caddy-particular]] preference. Shape:

```caddy
# ~/workspace/proxy/caddy/caddy.d/found-footy.caddy — indicative
http://found-footy-prod-api.{$BASE_DOMAIN} {
    # Public unauthed paths — skip auth
    @public path /api/v1/openapi.json /api/v1/healthz /api/v1/readyz /api/v1/videos/*
    handle @public {
        reverse_proxy found-footy-prod-api:8080
    }

    # Auth-protected paths — Bearer check
    @authed header Authorization "Bearer {$FOUND_FOOTY_API_TOKEN}"
    handle {
        respond @!authed 401 {
            body `{"error":"missing or invalid Authorization"}`
            close
        }
        # Strip Authorization before forwarding — Go side assumes trust
        reverse_proxy found-footy-prod-api:8080 {
            header_up -Authorization
        }
    }
}
```

`FOUND_FOOTY_API_TOKEN` lives in `.env` on luv; vedanta-systems'
backend `.env` has the same value. Rotation: change both files,
restart Caddy + consumers. The share-id endpoint (`/api/v1/videos/*`)
is in `@public` — public URLs need to work without auth headers.

### Share-id redirect endpoint — the URL stability enforcer

Public consumers hit `GET /api/v1/videos/s_xyz789` and get a 302
redirect to a presigned S3 URL. The presigned URL is regenerated per
request from the *current* canonical asset — supersession, re-encode,
storage migration all invisible to the consumer.

**Handler shape:**

```go
type GetShareVideoInput struct {
    ShareID string `path:"share_id" doc:"Share ID like s_a1b2c3d4e5f6"`
}

type GetShareVideoOutput struct {
    Status   int    `header:"-"`  // 302 or 410
    Location string `header:"Location"`
    CacheControl string `header:"Cache-Control"`
    Body     struct {  // only present on 410
        Error string  `json:"error"`
        Reason string `json:"reason"`
    }
}

func getShareVideo(ctx context.Context, in *GetShareVideoInput) (*GetShareVideoOutput, error) {
    share, err := deps.VideoShares.GetByID(ctx, in.ShareID)
    if err != nil {
        return nil, huma.Error404NotFound("share not found")
    }

    if share.State == video.ShareStateRemoved {
        // 410 Gone with reason
        return &GetShareVideoOutput{
            Status: http.StatusGone,
            Body: struct{...}{
                Error:  "share removed",
                Reason: *share.RemovedReason,  // "var" | "policy" | "asset_gone"
            },
        }, nil
    }

    asset, err := deps.VideoAssets.Get(ctx, share.AssetID)
    if err != nil { return nil, err }

    // Follow supersession chain (in case this asset was merged during dedup pass)
    for asset.SupersededBy != nil {
        asset, err = deps.VideoAssets.Get(ctx, *asset.SupersededBy)
        if err != nil { return nil, err }
    }

    presignedURL, err := deps.S3.PresignedGetURL(ctx, asset.S3Bucket, asset.S3Key, 1*time.Hour)
    if err != nil { return nil, err }

    return &GetShareVideoOutput{
        Status:       http.StatusFound,  // 302
        Location:     presignedURL,
        CacheControl: "no-store",  // must not cache the redirect itself
    }, nil
}
```

**`Cache-Control: no-store`** on the redirect response prevents
consumers from caching the presigned URL. The underlying S3 URL is
time-limited (1 hour default); if a consumer cached it, they'd get
403s after expiry. Better: re-resolve per request.

**Fallback: 410 Gone with reason** — when a share is removed (VAR,
policy, asset garbage-collected), the endpoint returns a friendly
410 with the removal reason in the body. Consumers (og-server, cached
tweet embeds) can display "this goal was reversed by VAR" instead of
a raw 404.

**og-server integration**: og-server's OpenGraph card generation loop
becomes:

1. Receive request for a share URL.
2. HEAD to `/api/v1/videos/{share_id}` (no auth needed).
3. If 302: use the `Location` value as the video URL in the OG card.
4. If 410: render a "removed" OG card with the reason.
5. If 404: 404 the OG endpoint too.

### Query endpoints — the read surface

Every query endpoint follows the pattern:

```go
type ListFixturesInput struct {
    Date     *string `query:"date" doc:"ISO date filter (YYYY-MM-DD)"`
    State    *string `query:"state" enum:"staging,active,completed" doc:"lifecycle state"`
    LeagueID *int    `query:"league_id"`
    TeamID   *int    `query:"team_id"`
    Limit    int     `query:"limit" default:"50" maximum:"200"`
    Cursor   *string `query:"cursor" doc:"opaque pagination cursor"`
}

type ListFixturesOutput struct {
    Body struct {
        Fixtures    []FixtureResponse `json:"fixtures"`
        NextCursor  *string           `json:"next_cursor,omitempty"`
    }
}

func listFixtures(ctx context.Context, in *ListFixturesInput) (*ListFixturesOutput, error) {
    // Compose filter → call fixture.Service.List → project to FixtureResponse
}
```

**Response projection.** Every domain type from §4 has a corresponding
`Response` type in `internal/api/models` that's a deliberate public
projection. Storage schema (`_prefixed` enhancement fields, JSONB
telemetry innards) doesn't leak. Consumers see clean public shapes:

```go
// internal/api/models/event.go
type EventResponse struct {
    ID                 string          `json:"event_id"`
    NaturalKey         string          `json:"event_natural_key"`
    FixtureID          int64           `json:"fixture_id"`
    Type               string          `json:"type"`
    Detail             string          `json:"detail"`
    PlayerName         *string         `json:"player_name"`
    TeamName           string          `json:"team_name"`
    Minute             int             `json:"minute"`
    Extra              *int            `json:"extra,omitempty"`
    FirstSeenAt        time.Time       `json:"first_seen_at"`
    State              string          `json:"state"`  // "pending" | "tracking" | "complete" | "removed"
    Videos             []VideoLink     `json:"videos"`
    TelemetrySummary   *TelemetryPublic `json:"telemetry,omitempty"`  // subset — not the raw JSONB
}

type VideoLink struct {
    ShareID           string  `json:"share_id"`
    URL               string  `json:"url"`  // "/api/v1/videos/{share_id}" (relative)
    Rank              int     `json:"rank"`
    TimestampVerified bool    `json:"timestamp_verified"`
    ExtractedMinute   *int    `json:"extracted_minute,omitempty"`
}

type TelemetryPublic struct {
    SearchAttempts       int     `json:"search_attempts"`
    VideosCapturedTotal  int     `json:"videos_captured_total"`
    CoverageRate         *float64 `json:"coverage_rate,omitempty"`
}
```

**`FromDomain`** classmethod on each Response type maps domain →
public. Internal telemetry (failure_class counters, time_to_first_s3_p50)
is DELIBERATELY not exposed publicly — it's engineering
observability, not consumer content. The public `TelemetryPublic` is
the deliberate summary consumers get.

### SSE stream endpoint

Long-lived HTTP response holding a connection to the client with
`Content-Type: text/event-stream`. Uses stdlib `http.Flusher` — no
library needed.

**Handler shape:**

```go
// GET /api/v1/sse/events?since=<optional_cursor>&event_type_filter=...
func sseHandler(w http.ResponseWriter, r *http.Request) {
    flusher, ok := w.(http.Flusher)
    if !ok { http.Error(w, "streaming not supported", 500); return }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-store")
    w.Header().Set("Connection", "keep-alive")
    w.WriteHeader(200)
    flusher.Flush()

    ctx := r.Context()

    // Optional backfill: replay unseen events since a cursor
    if since := r.URL.Query().Get("since"); since != "" {
        events, _ := deps.EventLog.ListSince(ctx, since, 100)
        for _, e := range events {
            writeSSE(w, flusher, e)
        }
    }

    // Live tail via Postgres LISTEN/NOTIFY
    notifyCh, err := deps.PG.Listen(ctx, "found_footy_events")
    if err != nil { return }

    for {
        select {
        case <-ctx.Done():
            return  // client disconnected
        case n := <-notifyCh:
            writeSSE(w, flusher, decodeSSE(n.Payload))
        case <-time.After(15 * time.Second):
            // Heartbeat comment to keep proxy timeouts happy
            fmt.Fprintf(w, ": keepalive\n\n")
            flusher.Flush()
        }
    }
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, e SSEEvent) {
    fmt.Fprintf(w, "id: %d\n", e.ID)
    fmt.Fprintf(w, "event: %s\n", e.EventType)
    fmt.Fprintf(w, "data: %s\n\n", e.PayloadJSON)
    flusher.Flush()
}
```

**Message format:**

```
id: 12345
event: event.video_ready
data: {"event_id":"e_a1b2c3d4e5f6","share_id":"s_xyz789","rank":1,"fixture_id":1562345,"player_name":"C. Gakpo","minute":72}

id: 12346
event: event.detected
data: {"event_id":"e_...","state":"tracking","fixture_id":1562345,"minute":18}

: keepalive

id: 12347
event: fixture.completed
data: {"fixture_id":1562345,"video_count":7}
```

**Event types emitted on SSE (matching §7 NotifyEventLog calls):**

- `event.detected` — new event first appears
- `event.stable` — event passed 3-poll debounce
- `event.video_ready` — a new share_id is available (rank included)
- `event.rank_recalculated` — ranks changed (rare; usually paired with video_ready)
- `event.removed` — VAR removed
- `event.download_complete` — 10 download workflows fired for this event
- `fixture.activated` — fixture moved to active
- `fixture.completed` — fixture terminated, all telemetry finalized

**Reconnect semantics.** SSE clients get the `id:` field which is the
`event_log.id` bigserial. On reconnect, the client sends
`Last-Event-Id: <id>` header (browsers do this automatically), and the
handler backfills from that cursor via the `since` query param path.

**Filtering.** `event_type_filter` query param accepts a
comma-separated list. Server-side filtering; only matching events get
written to the stream.

### Webhook subscription + delivery

Webhooks solve the "SSE loses messages on consumer restart" problem
from audit §11. Subscription lives in Postgres (§3 `webhook_subscriptions`).
Delivery is out-of-band from HTTP request handling — a separate worker
loop in the `api` binary polls `webhook_deliveries` for pending rows
and POSTs to subscribers.

**Subscription endpoints:**

```go
// POST /api/v1/webhooks/subscriptions
type CreateSubscriptionInput struct {
    Body struct {
        ConsumerName string   `json:"consumer_name" required:"true"`
        URL          string   `json:"url" required:"true" format:"uri"`
        EventTypes   []string `json:"event_types,omitempty"`  // empty = all
    }
}

type CreateSubscriptionOutput struct {
    Body struct {
        ID          string   `json:"id"`
        HMACSecret  string   `json:"hmac_secret"`  // returned once at creation; caller stores it
    }
}
```

`HMACSecret` is returned once at creation (never again). The
subscriber stores it and validates the `X-FF-Signature` header on
incoming deliveries.

**Delivery worker loop (separate from HTTP handling):**

```go
// runs in cmd/api/main.go as a goroutine
func webhookDeliveryLoop(ctx context.Context, deps Deps) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            batch, err := deps.WebhookDeliveries.ClaimPending(ctx, 50)
            if err != nil { continue }

            for _, delivery := range batch {
                deliverOne(ctx, deps, delivery)
            }
        }
    }
}

func deliverOne(ctx context.Context, deps Deps, d webhookdelivery.Pending) {
    subscription := d.Subscription
    payload, _ := json.Marshal(d.Event)
    sig := hmacSign(payload, subscription.HMACSecret)

    req, _ := http.NewRequestWithContext(ctx, "POST", subscription.URL, bytes.NewReader(payload))
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-FF-Event", d.Event.EventType)
    req.Header.Set("X-FF-Delivery-Id", d.ID.String())
    req.Header.Set("X-FF-Signature", "hmac-sha256="+sig)
    req.Header.Set("X-FF-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))

    resp, err := deps.HTTPClient.Do(req)
    if err != nil {
        deps.WebhookDeliveries.RecordFailure(ctx, d.ID, 0, err.Error())
        return
    }
    defer resp.Body.Close()

    if resp.StatusCode >= 200 && resp.StatusCode < 300 {
        deps.WebhookDeliveries.RecordSuccess(ctx, d.ID)
    } else {
        body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
        deps.WebhookDeliveries.RecordFailure(ctx, d.ID, resp.StatusCode, string(body))
    }
}
```

**Retry semantics** — encoded in the schema from §3:
- Pending = `succeeded_at IS NULL AND give_up_at IS NULL`
- `RecordFailure` bumps `attempt_count`, updates `last_attempt_at`, and:
  - If `attempt_count < 5`: schedule next attempt via exponential backoff
    (2^n minutes: 1min, 2min, 4min, 8min, 16min)
  - If `attempt_count >= 5`: set `give_up_at = NOW()`, alert operator
- Delivery worker's `ClaimPending` query filters:

```sql
SELECT * FROM webhook_deliveries
WHERE succeeded_at IS NULL
  AND give_up_at IS NULL
  AND (last_attempt_at IS NULL OR
       last_attempt_at + INTERVAL '1 minute' * pow(2, attempt_count) < NOW())
ORDER BY last_attempt_at NULLS FIRST
LIMIT 50
FOR UPDATE SKIP LOCKED
```

`FOR UPDATE SKIP LOCKED` handles multi-worker delivery safely — if we
ever scale to N API replicas, they don't step on each other.

**Consumer idempotency** — the `X-FF-Delivery-Id` UUID is unique per
delivery attempt. Consumers dedupe by this ID.

**Subscription lifecycle:**

- vedanta-systems' backend on startup: `POST /api/v1/webhooks/subscriptions`
  with `consumer_name="vedanta-systems", event_types=["event.video_ready", "event.download_complete"]`.
- On response, store the returned `hmac_secret` in its own local
  storage (env var / Postgres row).
- Idempotency of subscription creation: schema enforces
  `UNIQUE (consumer_name, url)`. Re-creating with same tuple returns
  the existing subscription's ID + a fresh HMAC secret (rotates on
  re-registration).

### OpenAPI spec + TS generation

Huma generates the spec from handler signatures at binary startup.
Served at `/api/v1/openapi.json` unauthenticated.

**vedanta-systems CI integration** (indicative — actual pipeline details are vedanta-systems' concern):

```yaml
# vedanta-systems/.github/workflows/regen-types.yml
- run: |
    curl -sf https://found-footy-prod-api.<base>/api/v1/openapi.json \
      -o openapi.json
    npx openapi-typescript openapi.json \
      -o src/types/found-footy.ts
    # If diff, commit + PR
    git diff --exit-code src/types/found-footy.ts || {
        git add src/types/found-footy.ts
        git commit -m "chore: regen found-footy types"
        gh pr create ...
    }
```

Alternative: run on every found-footy deploy (post-restart, hit the
new spec URL, regenerate). Either way, the human loop is "review the
TS diff PR" — schema drift can't ship silently.

### Versioning policy

Concrete rules for when to break vs when to extend:

**Non-breaking (adds to `v1`):**
- Adding new endpoints
- Adding new optional fields to response bodies
- Adding new optional query parameters
- Adding new event types to SSE
- Adding new webhook event types (subscribers filter)

**Breaking (requires `v2`):**
- Removing endpoints
- Removing or renaming response fields
- Changing response field types (e.g., int → string)
- Changing default values that consumers might rely on

**When `v2` ships:**
1. `/api/v2/…` endpoints live alongside `/api/v1/…`.
2. All `v1` responses include:
   - `Deprecation: <date>` header (announce date)
   - `Sunset: <date>` header (removal date; 6 months out minimum)
3. Grafana panel shows `v1` request count over time — the "are consumers migrating" tracker.
4. When `v1` sunset date arrives:
   - `v1` endpoints return `410 Gone` with `Link: <v2-endpoint>; rel="successor-version"`.
   - Log at ERROR level with `consumer=<caller-ip>` for post-sunset stragglers.

Policy lives in `docs/api-contract.md` (per audit §11 recommendation).

### Cross-cutting: logging + metrics per request

Every HTTP request emits a single INFO log line via
`internal/logging` — `module=api`, `action=http_request_handled`,
with fields:

```json
{
  "module": "api",
  "action": "http_request_handled",
  "method": "GET",
  "path": "/api/v1/events/e_a1b2c3d4e5f6",
  "status": 200,
  "duration_ms": 42,
  "request_id": "req_9x8y7z",
  "trace_id": "req_9x8y7z",  // same as request_id for HTTP
  "consumer_ip": "10.0.5.12"
}
```

Prometheus metrics per §11's naming convention:

- `found_footy_api_calls_total{path_template, status_class, method}` — counter
- `found_footy_api_duration_seconds{path_template, method}` — histogram
- `found_footy_api_in_flight{path_template}` — gauge
- `found_footy_api_sse_active_streams` — gauge
- `found_footy_api_webhook_deliveries_total{outcome}` — counter
  (outcome: `success` | `failure` | `gave_up`)

### Testing shape

**Handler tests** — Huma provides a test harness that hits handlers
with typed input and asserts typed output:

```go
func TestGetEvent_Found_Returns200WithVideos(t *testing.T) {
    api, deps := newTestAPI(t)
    // seed fixture + event + share in the test Postgres
    eventID := seedEvent(t, deps.PG)
    seedShare(t, deps.PG, eventID, /*rank=*/1)

    resp := api.Get(t, "/api/v1/events/" + eventID.String())
    require.Equal(t, 200, resp.Code)

    var body EventResponse
    require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
    require.Equal(t, eventID.String(), body.ID)
    require.Len(t, body.Videos, 1)
    require.Equal(t, 1, body.Videos[0].Rank)
}

func TestGetShareVideo_ActiveShare_Returns302(t *testing.T)
func TestGetShareVideo_RemovedShare_Returns410WithReason(t *testing.T)
func TestGetShareVideo_MissingShare_Returns404(t *testing.T)
func TestGetShareVideo_SupersededAsset_FollowsChain(t *testing.T)
```

**SSE tests:**

```go
func TestSSEStream_ReceivesLiveEvent(t *testing.T) {
    api, deps := newTestAPI(t)

    // Start SSE in goroutine
    ch := make(chan string, 10)
    go func() {
        // ... consume /api/v1/sse/events, push to ch
    }()

    // Insert into event_log — should trigger NOTIFY
    deps.PG.Exec(ctx, "INSERT INTO event_log (event_type, payload) VALUES ($1, $2)",
        "event.video_ready", `{"event_id":"e_test"}`)
    deps.PG.Notify(ctx, "found_footy_events", `{"event_type":"event.video_ready","event_id":"e_test"}`)

    // Assert we received it within 1s
    select {
    case msg := <-ch:
        require.Contains(t, msg, "e_test")
    case <-time.After(1 * time.Second):
        t.Fatal("no SSE event received")
    }
}
```

**Webhook delivery tests** with an in-process HTTP test server acting
as the subscriber:

```go
func TestWebhookDelivery_HappyPath_MarksSucceeded(t *testing.T)
func TestWebhookDelivery_500Response_RetryWithBackoff(t *testing.T)
func TestWebhookDelivery_5Failures_GivesUp(t *testing.T)
func TestWebhookDelivery_HMACSignature_ValidatesAgainstSecret(t *testing.T)
```

**Integration tests** at `test/integration/api_test.go` spin up the
full stack (Postgres + Garage + api binary) and hit real HTTP
endpoints.

### Extensibility hooks

**New endpoint** — declare input/output structs, register with Huma,
Chi routing free. OpenAPI regenerates automatically. Zero touch to
other endpoints.

**New SSE event type** — add to §7 `NotifyEventLog` call sites in
whatever workflow emits it; SSE handler forwards anything on the
Postgres channel. Consumers subscribe to the new type via their
`event_type_filter` param.

**New webhook event type** — same as SSE; the delivery worker forwards
any `event_log` row whose `event_type` matches a subscription's
`event_types` array (empty = all).

**Rate limiting** — Caddy can enforce per-IP rate limits at the edge
via the `rate_limit` handler. Not day-one (single trusted consumer),
but the hook is there.

**Endpoint deprecation without breaking** — add `Deprecation: <date>`
header to responses; keep the endpoint working; consumers see the
header in devtools + CI type-gen picks it up as `@deprecated` in TS.

---

## 10. Deployment

Per-project docker-compose owns its full data plane. Shared workspace
infra (Caddy proxy, monitor stack, Tailscale) sits outside. Everything
follows [`~/workspace/proxy/CONVENTIONS.md`](../../proxy/CONVENTIONS.md)
as the canonical naming + networking authority — this section applies
those conventions to found-footy and only surfaces choices where the
convention leaves room.

### The workspace convention (quick recap)

Cited from `~/workspace/proxy/CONVENTIONS.md`. Five rules that this
section defers to:

1. **Container name == URL.** `<project>-<env>-<role>.<BASE_DOMAIN>`
   reverse-proxies to `<project>-<env>-<role>:<internal-port>` over
   the `proxy` docker network.
2. **Roles come from a constrained vocabulary.** No `webserver`,
   `backend`, `ui`, `app`, `service` — always tool-prefixed or
   functional-role-named.
3. **Each project ships an `api`; only vedanta-systems ships a
   `frontend`.** found-footy has an API for vedanta-systems to
   consume.
4. **No host HTTP ports.** Everything attaches to the shared `proxy`
   docker network and is fronted by Caddy.
5. **Three shared networks:** `proxy` (HTTP ingress), `luv-prod` and
   `luv-dev` (cross-project data plane by env).

The rebuild inherits this. Where I show container names below, they
follow the pattern without alternatives — that's already decided at
the workspace level. Where naming is genuinely underdetermined (a
tool that doesn't map cleanly to the role vocabulary), I surface
options for you to pick.

### Container inventory

**Prod compose** (`docker-compose.yml`):

| Container | Role | Image | Ports (internal) | Purpose |
|---|---|---|---|---|
| `found-footy-prod-postgres` | app data | `postgres:16-alpine` | 5432 | Fixtures, events, videos, aliases, telemetry, event_log |
| `found-footy-prod-garage` | blob store | `dxflrs/garage:latest` | 3900 (S3 API), 3902 (admin) | Video files, S3-compatible |
| `found-footy-prod-temporal` | workflow engine | `temporalio/auto-setup:1.24` | 7233 | Temporal server (gRPC) |
| `found-footy-prod-temporal-postgres` | temporal metadata | `postgres:16-alpine` | 5432 | Temporal's own metadata store (separate from app postgres) |
| `found-footy-prod-temporal-ui` | Temporal UI | `temporalio/ui:latest` | 8080 | Workflow observability |
| `found-footy-prod-worker` | Temporal worker | (built from `deploy/Dockerfile.worker`) | — | Runs `cmd/worker` binary; scaled 2-8 |
| `found-footy-prod-api` | HTTP API | (built from `deploy/Dockerfile.api`) | 8080 | Runs `cmd/api` binary; single replica |
| `found-footy-prod-scaler` | auto-scale | (built from `deploy/Dockerfile.scaler`) | — | Runs `cmd/scaler` binary; single replica |
| `found-footy-prod-twitter` | Playwright fleet | (built from `deploy/Dockerfile.twitter`) | 8888 (search HTTP), 6080 (noVNC, `vnc` profile only) | Runs `cmd/twitter` binary; scaled 2-8 |

**Dev compose** (`docker-compose.dev.yml`) mirrors the same set with
`-dev-` in container names. Dev additions:

- `found-footy-dev-twitter` runs noVNC always (not gated behind `vnc`
  profile); makes cookie re-auth painless during dev work.

**Total: 9 unique container roles**, down from Python's 11 (removed:
`mongo`, `mongo-express`/`mongoku`, `minio` — replaced by
`postgres` and `garage`).

### Container naming — decisions to surface

Most names are decided by workspace convention. Three genuinely open
questions:

**Decision 1: Garage admin UI hostname.** Garage exposes a web admin on
port 3902 (metrics dashboard, bucket status). Options:

| Option | Rationale |
|---|---|
| `found-footy-prod-garage-web` | Follows the `<tool>-<purpose>` pattern from CONVENTIONS.md role vocabulary. Concise. |
| `found-footy-prod-garage-admin` | Slightly more explicit about what's on 3902. |
| Don't expose it (internal only) | Garage's on-disk format is `ls`-able; do we actually need the admin UI? |

Recommendation: `found-footy-prod-garage-web` per the `<tool>-<purpose>`
convention. If we never actually visit the admin, we can drop the
Caddy entry later — the container stays either way.

**Decision 2: Postgres admin UI at all.** For MinIO we had a web
console (accessible via `found-footy-prod-minio.<base>`). For Postgres,
options:

| Option | Rationale |
|---|---|
| Add `found-footy-prod-adminer` (image: `adminer:latest`) | Tool-specific naming per convention. Handy for ad-hoc queries. |
| Add `found-footy-prod-pgadmin` (image: `dpage/pgadmin4`) | Heavier but more featureful. |
| Don't add one; use `psql` via `docker exec` | Simplest. Matches the "if you need it, you know how to `docker exec`" philosophy. |

Recommendation: **don't add one day one**. `docker exec` into
`found-footy-prod-postgres` and run `psql` is the simple answer. Add
adminer later if it turns out we're doing enough ad-hoc queries to
justify the extra container.

**Decision 3: Twitter VNC gating.** Current found-footy prod gates
noVNC behind a docker compose `profiles: [vnc]` — only started when
cookie re-auth is needed. Dev runs it always. Options:

| Option | Rationale |
|---|---|
| Keep prod behind `vnc` profile (current pattern) | Prod avoids running an idle VNC server all the time. |
| Always run in prod too | Simpler ops; one less thing to remember. Cost: ~50 MB idle memory. |

Recommendation: **keep the profile gate**. Current pattern works,
one-time `docker compose up -d twitter-vnc` when re-auth is needed
isn't burdensome.

### Compose file structure

Two compose files per convention:

- `docker-compose.yml` — prod stack (containers named `found-footy-prod-*`, on the `found-footy-prod` internal bridge network + attached to `proxy` for HTTP ingress + attached to `luv-prod` for cross-project data plane)
- `docker-compose.dev.yml` — dev stack (mirrors prod but with `-dev-` names and dev-friendly overrides like verbose logging, no restart-unless-stopped, twitter VNC always running)

**Shape of `docker-compose.yml`:**

```yaml
name: found-footy-prod

services:
  postgres:
    image: postgres:16-alpine
    container_name: found-footy-prod-postgres
    environment:
      POSTGRES_USER: ${PG_USER}
      POSTGRES_PASSWORD: ${PG_PASSWORD}
      POSTGRES_DB: found_footy
    volumes:
      - ${DATA_DIR:-~/workspace/data/found-footy}/postgres:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${PG_USER} -d found_footy"]
      interval: 10s
      timeout: 5s
      retries: 5
    networks:
      - found-footy-prod
    restart: unless-stopped

  garage:
    image: dxflrs/garage:latest
    container_name: found-footy-prod-garage
    volumes:
      - ${DATA_DIR:-~/workspace/data/found-footy}/garage/data:/var/lib/garage/data
      - ${DATA_DIR:-~/workspace/data/found-footy}/garage/meta:/var/lib/garage/meta
      - ./deploy/garage.toml:/etc/garage.toml:ro
    networks:
      - found-footy-prod
      - proxy
    restart: unless-stopped

  garage-web:
    # Optional admin UI — see Decision 1. Remove this service block if
    # we ultimately decide not to expose the admin.
    image: dxflrs/garage:latest
    container_name: found-footy-prod-garage-web
    command: ["garage", "admin", "web"]  # illustrative — real command TBD
    networks:
      - found-footy-prod
      - proxy
    restart: unless-stopped
    depends_on:
      - garage

  temporal:
    image: temporalio/auto-setup:1.24
    container_name: found-footy-prod-temporal
    environment:
      DB: postgres12
      DB_PORT: 5432
      POSTGRES_USER: ${TEMPORAL_PG_USER}
      POSTGRES_PWD: ${TEMPORAL_PG_PASSWORD}
      POSTGRES_SEEDS: temporal-postgres
    networks:
      - found-footy-prod
    depends_on:
      temporal-postgres:
        condition: service_healthy
    restart: unless-stopped

  temporal-postgres:
    image: postgres:16-alpine
    container_name: found-footy-prod-temporal-postgres
    environment:
      POSTGRES_USER: ${TEMPORAL_PG_USER}
      POSTGRES_PASSWORD: ${TEMPORAL_PG_PASSWORD}
      POSTGRES_DB: temporal
    volumes:
      - ${DATA_DIR:-~/workspace/data/found-footy}/temporal-postgres:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${TEMPORAL_PG_USER} -d temporal"]
    networks:
      - found-footy-prod
    restart: unless-stopped

  temporal-ui:
    image: temporalio/ui:latest
    container_name: found-footy-prod-temporal-ui
    environment:
      TEMPORAL_ADDRESS: temporal:7233
      TEMPORAL_CORS_ORIGINS: "*"
    networks:
      - found-footy-prod
      - proxy
    depends_on:
      - temporal
    restart: unless-stopped

  worker:
    build:
      context: .
      dockerfile: deploy/Dockerfile.worker
    image: found-footy-worker:latest  # tagged locally; no registry today
    container_name: found-footy-prod-worker
    env_file: .env
    depends_on:
      postgres:
        condition: service_healthy
      temporal:
        condition: service_started
      garage:
        condition: service_started
    networks:
      - found-footy-prod
      - luv-prod
    deploy:
      replicas: 2  # scaler manages between 2 and 8
    restart: unless-stopped

  api:
    build:
      context: .
      dockerfile: deploy/Dockerfile.api
    image: found-footy-api:latest
    container_name: found-footy-prod-api
    env_file: .env
    depends_on:
      postgres:
        condition: service_healthy
    networks:
      - found-footy-prod
      - proxy
      - luv-prod  # for vedanta-systems to reach
    restart: unless-stopped

  scaler:
    build:
      context: .
      dockerfile: deploy/Dockerfile.scaler
    image: found-footy-scaler:latest
    container_name: found-footy-prod-scaler
    env_file: .env
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro  # to scale worker + twitter
    networks:
      - found-footy-prod
    restart: unless-stopped

  twitter:
    build:
      context: .
      dockerfile: deploy/Dockerfile.twitter
    image: found-footy-twitter:latest
    container_name: found-footy-prod-twitter
    env_file: .env
    volumes:
      - ${DATA_DIR:-~/workspace/data/found-footy}/twitter/profiles:/data/firefox_profiles
      - ~/.config/found-footy/twitter_cookies.json:/config/twitter_cookies.json
    networks:
      - found-footy-prod
    deploy:
      replicas: 2  # scaler manages between 2 and 8
    restart: unless-stopped

  twitter-vnc:
    build:
      context: .
      dockerfile: deploy/Dockerfile.twitter
      args:
        WITH_VNC: "true"
    image: found-footy-twitter-vnc:latest
    container_name: found-footy-prod-twitter-vnc
    profiles: [vnc]  # only started when cookie re-auth is needed
    env_file: .env
    volumes:
      - ${DATA_DIR:-~/workspace/data/found-footy}/twitter/profiles:/data/firefox_profiles
      - ~/.config/found-footy/twitter_cookies.json:/config/twitter_cookies.json
    networks:
      - found-footy-prod
      - proxy
    restart: "no"

networks:
  found-footy-prod:
    driver: bridge
  proxy:
    external: true
  luv-prod:
    external: true
```

Dev compose is structurally identical with `-dev-` names, `dev` env
vars, no `vnc` profile gate on `twitter-vnc`, and volume mounts under
`~/workspace/data/found-footy-dev/`.

### Dockerfiles

One per binary. All follow the same pattern: multi-stage build,
static Go binary, minimal runtime layer.

**`deploy/Dockerfile.worker`** (representative — api, scaler are near-identical):

```dockerfile
# ────── build stage ──────
FROM golang:1.23-alpine AS build

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .

# Build args for baking git_sha + built_at into the binary per §11 deploy tracking
ARG GIT_SHA=unknown
ARG BUILT_AT=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.gitSHA=${GIT_SHA} -X main.builtAt=${BUILT_AT}" \
    -o /out/worker \
    ./cmd/worker

# ────── runtime stage ──────
FROM alpine:3.20

# ffmpeg for the video pipeline (§7)
RUN apk add --no-cache ffmpeg tzdata ca-certificates

# Non-root user
RUN adduser -D -H -u 1000 app
USER app

COPY --from=build /out/worker /usr/local/bin/worker

ENTRYPOINT ["/usr/local/bin/worker"]
```

**`deploy/Dockerfile.twitter`** is the exception — needs Firefox +
`geckodriver` for Playwright-Go:

```dockerfile
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG GIT_SHA=unknown
ARG BUILT_AT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.gitSHA=${GIT_SHA} -X main.builtAt=${BUILT_AT}" \
    -o /out/twitter \
    ./cmd/twitter

FROM debian:bookworm-slim

# Firefox + geckodriver + Xvfb (for headless) + noVNC (behind ARG)
RUN apt-get update && apt-get install -y --no-install-recommends \
        firefox-esr xvfb ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

ARG WITH_VNC=false
RUN if [ "$WITH_VNC" = "true" ]; then \
        apt-get update && apt-get install -y \
            x11vnc novnc websockify && \
        rm -rf /var/lib/apt/lists/*; \
    fi

# Playwright will download browsers via `playwright install` at first run;
# alternative is to bake them into the image via the build stage.

RUN adduser --disabled-password --gecos "" --uid 1000 app
USER app

COPY --from=build /out/twitter /usr/local/bin/twitter

EXPOSE 8888 6080
ENTRYPOINT ["/usr/local/bin/twitter"]
```

Base image size targets (approximate):
- `Dockerfile.worker` / `.api` / `.scaler`: ~15-25 MB (alpine + Go static binary + ffmpeg where needed)
- `Dockerfile.twitter`: ~800 MB (Debian + Firefox is unavoidable)

### Volumes and bind mounts

Data on the host lives at `~/workspace/data/found-footy/` (or
`-dev/`) per the CLAUDE.md `data/<project>/` convention:

```
~/workspace/data/found-footy/
├── postgres/                    # app Postgres data dir
├── temporal-postgres/           # Temporal's metadata Postgres
├── garage/
│   ├── data/                    # content-addressed blob storage
│   └── meta/                    # bucket metadata
└── twitter/
    └── profiles/                # Firefox profile dirs, keyed by instance ID
```

Backup story: `rsync ~/workspace/data/found-footy/` — no proprietary
formats, everything is filesystem-native.

Bind mount rationale over named volumes:
- Explicit host location; easy to `ls` and `du -sh` for capacity checks
- Backup with regular filesystem tools
- Survive `docker compose down` unambiguously (named volumes do too,
  but bind mounts remove the "wait where is that data" question)

The Twitter cookie backup file at
`~/.config/found-footy/twitter_cookies.json` is bind-mounted read/write
into `twitter` and `twitter-vnc` containers. This is the safety-net
copy of `twitter_sessions.canonical.cookies` from §4 — even if
Postgres has issues, VNC re-auth still writes here and containers can
bootstrap from it.

### Networks

Three networks per the workspace convention:

- **`found-footy-prod`** (internal bridge; declared in this
  docker-compose): all found-footy containers talk to each other on
  this network. Postgres, Garage, Temporal are only reachable here.
- **`proxy`** (external; created once at workspace setup): Caddy
  fronts all HTTP-exposed services. Only containers that need HTTP
  ingress attach: `api`, `temporal-ui`, `garage-web` (if enabled),
  `twitter-vnc` (when profile active).
- **`luv-prod`** (external; created once at workspace setup):
  cross-project data plane. `api` attaches so vedanta-systems can
  reach it. `worker` attaches if it needs to call out to vedanta-systems'
  API (for now: no). Postgres/Garage do NOT attach — data plane
  isolation.

Dev mirrors with `luv-dev`.

### Environment variables

Structure: `.env` (gitignored) + `.env.example` (git-tracked template).

**Categories** (one `.env` per environment; prod and dev have separate
files):

```bash
# .env.example — canonical template

# ─── App storage ───────────────────────────────
PG_USER=ffuser
PG_PASSWORD=CHANGE_ME
PG_DSN=postgres://ffuser:CHANGE_ME@postgres:5432/found_footy?sslmode=disable

# ─── Blob storage ──────────────────────────────
S3_ENDPOINT=http://garage:3900
S3_BUCKET=found-footy
S3_ACCESS_KEY_ID=CHANGE_ME
S3_SECRET_ACCESS_KEY=CHANGE_ME
S3_USE_PATH_STYLE=true

# ─── Temporal ──────────────────────────────────
TEMPORAL_HOSTPORT=temporal:7233
TEMPORAL_NAMESPACE=default
TEMPORAL_TASK_QUEUE=found-footy
TEMPORAL_PG_USER=temporal
TEMPORAL_PG_PASSWORD=CHANGE_ME

# ─── LLM (per decisions.md 2026-07-01 abstraction) ────
LLM_ENDPOINT_URL=http://llama-small.joi
LLM_API_KEY=not-required
LLM_CHAT_MODEL=       # empty = discover from /v1/models
LLM_EMBEDDING_MODEL=

# ─── External APIs ─────────────────────────────
API_FOOTBALL_KEY=CHANGE_ME
API_FOOTBALL_TRACKED_LEAGUES=39,140,78,135,61,1

# ─── Twitter fleet ────────────────────────────
TWITTER_INSTANCE_URLS=http://found-footy-prod-twitter:8888

# ─── Auth ─────────────────────────────────────
FOUND_FOOTY_API_TOKEN=CHANGE_ME    # Caddy-side Bearer check

# ─── Observability ────────────────────────────
LOG_LEVEL=INFO
LOKI_ENABLED=true

# ─── Deploy tracking (baked in by ldflags at build; runtime override is unusual)
DEPLOY_ENV=prod

# ─── Host paths ───────────────────────────────
DATA_DIR=~/workspace/data/found-footy
BASE_DOMAIN=luv
```

**Env var URL format:** internal service URLs use compose service
names (`postgres`, `garage`, `temporal`), not container names
(`found-footy-prod-postgres`). Both would resolve within the compose
network, but service names are:
- Shorter and readable
- Same value works for both prod and dev without templating (each
  compose project is its own network)
- Matches existing found-footy convention (Python code uses
  `mongodb://mongo:27017`, not `mongodb://found-footy-prod-mongo:27017`)

The exceptions where full container names matter:
- Cross-project references (vedanta-systems reaching found-footy's
  api → uses `found-footy-prod-api`, because it's addressing
  across compose projects on the shared `luv-prod` network)
- Caddy hostnames (always the full container name pattern per
  workspace convention)

### Caddy integration

The per-project Caddyfile at
`~/workspace/proxy/caddy/caddy.d/found-footy.caddy` gets updated to
match the rebuild's inventory. Auth-gated paths on the `api`
hostname; unauthenticated public paths for openapi.json, healthz,
readyz, and the share-id endpoint (§8).

**Proposed Caddyfile after rebuild** (naming decisions from above
applied; alternatives noted):

```caddy
# found-footy — Twitter scraper + Temporal + API stack (Go rebuild)
# Tailnet-only; no public Cloudflare hostname.

# ─── prod ───────────────────────────────────────────────────────────────────

# Public API — split auth (public paths for spec/health/share redirect,
# authed for everything else).
http://found-footy-prod-api.{$BASE_DOMAIN} {
    @public path /api/v1/openapi.json /api/v1/healthz /api/v1/readyz /api/v1/videos/*
    handle @public {
        reverse_proxy found-footy-prod-api:8080
    }

    @authed header Authorization "Bearer {$FOUND_FOOTY_API_TOKEN}"
    handle {
        respond @!authed 401 {
            body `{"error":"missing or invalid Authorization"}`
            close
        }
        reverse_proxy found-footy-prod-api:8080 {
            header_up -Authorization
        }
    }
}

# Temporal UI — workflow observability, tailnet-only, no auth needed
# (assumption: tailnet is the auth boundary).
http://found-footy-prod-temporal-ui.{$BASE_DOMAIN} {
    reverse_proxy found-footy-prod-temporal-ui:8080
}

# Garage admin — Decision 1 above. Delete this block if we decide
# not to expose the admin.
http://found-footy-prod-garage-web.{$BASE_DOMAIN} {
    reverse_proxy found-footy-prod-garage:3902
}

# Twitter VNC — only reachable when started with the `vnc` profile.
# noVNC is websocket; Caddy handles upgrades natively.
http://found-footy-prod-twitter-vnc.{$BASE_DOMAIN} {
    reverse_proxy found-footy-prod-twitter-vnc:6080
}

# ─── dev ────────────────────────────────────────────────────────────────────

http://found-footy-dev-api.{$BASE_DOMAIN} {
    # Same auth pattern as prod. Dev token is different value.
    @public path /api/v1/openapi.json /api/v1/healthz /api/v1/readyz /api/v1/videos/*
    handle @public {
        reverse_proxy found-footy-dev-api:8080
    }

    @authed header Authorization "Bearer {$FOUND_FOOTY_DEV_API_TOKEN}"
    handle {
        respond @!authed 401
        reverse_proxy found-footy-dev-api:8080 {
            header_up -Authorization
        }
    }
}

http://found-footy-dev-temporal-ui.{$BASE_DOMAIN} {
    reverse_proxy found-footy-dev-temporal-ui:8080
}

http://found-footy-dev-garage-web.{$BASE_DOMAIN} {
    reverse_proxy found-footy-dev-garage:3902
}

http://found-footy-dev-twitter-vnc.{$BASE_DOMAIN} {
    reverse_proxy found-footy-dev-twitter-vnc:6080
}
```

**What's dropped vs current Python found-footy Caddyfile:**
- `found-footy-*-mongo-express` / `mongoku` — no Mongo anymore
- `found-footy-*-minio` — no MinIO anymore

**What's added:**
- `found-footy-*-api` — the new Go API surface (with split-auth pattern)
- `found-footy-*-garage-web` — pending Decision 1

**Auth for Temporal UI, Garage admin, Twitter VNC — surfaced choice.**
Current Python found-footy exposes these unauthenticated on the
tailnet (implicit assumption: tailnet is the auth boundary). Options
for the rebuild:

| Option | Rationale |
|---|---|
| Keep unauthenticated on tailnet (current pattern) | Simpler; assumes tailnet = trusted network. Consistent with monitor stack's Grafana/Portainer exposure. |
| Add Bearer auth on Temporal UI + Garage admin too | Extra defense-in-depth; costs an env var + Caddy block. Twitter VNC is inherently a "when in use" thing so auth matters less. |
| Add Caddy Basic Auth (user/password) instead of Bearer | HTTPBasic is browser-friendly for admin UIs; Bearer better for programmatic access. |

Recommendation: **stay unauthenticated on tailnet for the admin UIs**;
tailnet auth is the workspace convention already. Bearer stays on the
public `api` endpoint since that IS the programmatic surface. Revisit
if we ever add non-tailnet access.

### Health checks

Every long-lived container ships a `HEALTHCHECK` directive. Docker's
health status feeds into `docker compose up -d --wait` and the
`depends_on: { service_healthy }` condition.

- **`postgres` / `temporal-postgres`**: `pg_isready -U ... -d ...`
  every 10s
- **`garage`**: HTTP GET on `/v0/status` internal endpoint every 10s
- **`temporal`**: `tctl --address temporal:7233 workflow list --namespace default --workflow_id doesnotexist` (any command that hits the server) every 30s
- **`worker`**: `wget -qO- http://localhost:8080/healthz` — worker's
  built-in `/healthz` calls `pg.Ping` + `s3.Ping` + `llm.Ping` +
  `apifootball.Ping` + `twitter.Ping` (§9 aggregation)
- **`api`**: same pattern, wget on `/api/v1/healthz`
- **`scaler`**: `docker` CLI probe — `docker version` to confirm the
  socket is reachable
- **`twitter`**: `wget -qO- http://localhost:8888/health` — twitter
  service's own health check (audit §8 rich protocol)

### Graceful shutdown

Every Go binary registers a SIGTERM handler that:

1. Sets a "draining" flag (twitter container exposes it in `/health`
   payload for the scaler)
2. Stops accepting new work (workers stop polling Temporal task queues;
   API returns 503 on new SSE connections; twitter service refuses new
   `/search` calls)
3. Waits for in-flight work to complete with a bounded grace period
   (30s default)
4. Cleanly closes pg pool, s3 client, temporal client
5. Exits

Docker Compose sends SIGTERM by default; if the process doesn't exit
within `stop_grace_period` (default 10s), it escalates to SIGKILL.
Override to 60s in the compose file for services with in-flight
long-running work (`worker`, `twitter`).

### Scaling — the scaler binary

Runs continuously. Every 30 seconds:

1. Query Postgres for active-goal count and workflow queue backlog:
   ```sql
   SELECT count(*) FROM events e
   JOIN fixtures f ON f.id = e.fixture_id
   WHERE f.state = 'active'
     AND NOT e.download_complete AND NOT e.removed;
   ```
2. Query Temporal for pending task count on `found-footy` queue.
3. Compute desired replica count for `worker` and `twitter` services
   using thresholds:
   - worker: base 2, +1 per 5 pending tasks, max 8
   - twitter: base 2, +1 per 2 active goals, max 8
4. Cross-check against the scaler cooldown window (60s minimum
   between scaling actions per service).
5. Invoke `docker compose --project-name found-footy-prod up -d --scale worker=N --scale twitter=M` via the Docker socket.

`PROJECT_NAME` config option: `found-footy-prod` in prod's `.env`,
`found-footy-dev` in dev. Prevents scaler-in-dev from touching prod
(audit's May 2026 "Scaler hardcoded PROJECT_NAME" carry-over from
`docs/audit.md` §2).

### Build automation — Makefile

```makefile
# Makefile at repo root

GIT_SHA := $(shell git rev-parse --short=12 HEAD)
BUILT_AT := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: build test test-integration lint fmt \
        docker-build deploy-dev deploy-prod migrate

build:
	go build -ldflags="-X main.gitSHA=${GIT_SHA} -X main.builtAt=${BUILT_AT}" ./cmd/...

test:
	go test -short ./...

test-integration:
	go test -tags=integration ./...

lint:
	golangci-lint run

fmt:
	gofmt -w .
	goimports -w .

docker-build:
	@for bin in worker api scaler twitter; do \
		docker compose -f docker-compose.yml build \
			--build-arg GIT_SHA=${GIT_SHA} \
			--build-arg BUILT_AT=${BUILT_AT} \
			$$bin; \
	done

migrate:
	docker compose -f docker-compose.yml exec worker migrate up

deploy-dev:
	@bin/deploy dev

deploy-prod:
	@bin/deploy prod
```

### Deploy automation — `bin/deploy`

The load-bearing piece from audit §1. The 7-week-stale-image
disaster becomes a non-event because deploy is automated + tracked.

**`bin/deploy`** — the script both `make deploy-*` and the future
CI webhook invoke:

```bash
#!/usr/bin/env bash
# bin/deploy — rebuild found-footy images and restart containers.
# Usage: bin/deploy [prod|dev]
set -euo pipefail

ENV="${1:?usage: bin/deploy [prod|dev]}"
case "$ENV" in
    prod) COMPOSE_FILE="docker-compose.yml"; PROJECT="found-footy-prod" ;;
    dev)  COMPOSE_FILE="docker-compose.dev.yml"; PROJECT="found-footy-dev" ;;
    *)    echo "usage: bin/deploy [prod|dev]" >&2; exit 2 ;;
esac

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

echo ">> pulling latest from main"
git fetch origin main
git checkout main
git pull --ff-only

GIT_SHA=$(git rev-parse --short=12 HEAD)
BUILT_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)

echo ">> building images (GIT_SHA=${GIT_SHA})"
docker compose -f "$COMPOSE_FILE" build \
    --build-arg GIT_SHA="${GIT_SHA}" \
    --build-arg BUILT_AT="${BUILT_AT}" \
    worker api scaler twitter

echo ">> recreating containers"
docker compose -f "$COMPOSE_FILE" up -d --no-deps --no-build \
    worker api scaler twitter

echo ">> waiting for health"
for svc in api; do
    docker compose -f "$COMPOSE_FILE" exec -T "$svc" \
        wget -qO- http://localhost:8080/healthz || {
            echo "!! $svc failed health check"; exit 1;
        }
done

echo ">> verifying deploy tracking metric"
sleep 5
GIT_SHA_IN_METRIC=$(
    docker compose -f "$COMPOSE_FILE" exec -T api \
        wget -qO- http://localhost:8080/metrics \
        | grep '^found_footy_deploy_git_sha_info' \
        | grep -oP 'git_sha="\K[^"]+' | head -1
)

if [[ "$GIT_SHA_IN_METRIC" != "$GIT_SHA" ]]; then
    echo "!! deploy tracker metric shows ${GIT_SHA_IN_METRIC} but expected ${GIT_SHA}"
    exit 1
fi

echo ">> deploy complete: ${PROJECT} @ ${GIT_SHA}"
```

**What this fixes vs 2026-06-30:** rebuild + restart happens in one
command. Deploy tracking metric verified against actual git SHA
before returning success. If commit → prod gap grows, the Grafana
`found_footy_deploy_drift_commits` alert (audit §11) fires within
15 minutes.

### Deploy pipeline — CI trigger

**Deferred but shape:** GitHub Actions on push to `main`:

1. Build images in the Actions runner (or a self-hosted runner on luv).
2. POST to a webhook on luv that runs `bin/deploy prod`.
3. Report deploy outcome back to the Actions run.

Webhook target: a small `deploy-hook` HTTP endpoint that:
- Verifies the HMAC signature from GitHub Actions
- Enqueues the deploy (serializes to avoid concurrent deploys)
- Runs `bin/deploy prod` in a subshell
- Returns 200 on success or 500 on failure

Alternative if we go self-hosted Forgejo/Gitea later per the global
CLAUDE.md migration plan: same shape, different runner platform.

Both are deferred — day-one is manual `bin/deploy prod` invocation
after merging to main. CI automation is the follow-up.

### Development workflow

**`docker-compose.dev.yml`** overrides for hot-reload-friendly dev:

- Mount `./` into `worker`/`api` containers, and use `air` or
  `reflex` for auto-restart on file save (via `command:` override in
  the compose file, not baked into the Dockerfile).
- Postgres logs at `log_min_duration_statement = 0` for query-level
  visibility.
- Twitter runs with noVNC always accessible.
- `LOG_LEVEL=DEBUG` by default.

**Local iteration loop:**

```bash
# One-time setup
cp .env.example .env
# edit .env with local credentials

# Bring up the stack
make deploy-dev

# Watch worker logs
docker compose -f docker-compose.dev.yml logs -f worker

# Run tests inside the worker container
docker compose -f docker-compose.dev.yml exec worker make test

# Run a specific integration test
docker compose -f docker-compose.dev.yml exec worker \
    go test -tags=integration ./internal/domain/event/...
```

### Extensibility hooks

**Adding a new binary** (e.g., a batch-analytics service):

1. `cmd/analytics/main.go` — new binary main.
2. `deploy/Dockerfile.analytics` — new Dockerfile.
3. Add `analytics` service block to `docker-compose.yml` +
   `docker-compose.dev.yml`.
4. Container name: `found-footy-prod-analytics` per convention.
5. If HTTP-fronted: add Caddyfile entry
   `found-footy-prod-analytics.{$BASE_DOMAIN}`.
6. Update Makefile `docker-build` loop to include the new binary.
7. Update `bin/deploy` to rebuild + recreate the new service.

**Adding a new sidecar tool** (e.g., a dedicated observability
exporter):

- If it has an admin UI worth exposing: name it
  `found-footy-prod-<tool>-web` or
  `found-footy-prod-<tool>-ui` per the `<tool>-<purpose>` role
  vocabulary. Add Caddyfile entry.
- If internal-only: skip the Caddyfile entry, just declare the
  service in docker-compose.

**Moving to a self-hosted registry** (per global CLAUDE.md long-term
plan):

- Currently: `image: found-footy-worker:latest` (local tag, no push).
- Later: `image: gitea.luv/found-footy/worker:${GIT_SHA}` (or
  Forgejo equivalent). Pushed in `bin/deploy` after build, pulled on
  `docker compose up`.
- Enables blue-green deploys and rollback via previous image tag.
- Deferred; not blocking day-one deploy.

---

## 12. Testing

Concrete realization of audit §12's three-tier pyramid against the Go
stack. Every test is a deliverable of the code it exercises — the
Phase 3 pattern from the Python audit ("we'll add tests later") does
not repeat. Tests land in the same PR as their subject code, and CI
enforces coverage floors.

### Testing philosophy

**One principle over everything: services are the unit-test boundary;
stores are the integration-test boundary; workflows are the temporal-
test-suite boundary; end-to-end flow is the synthetic-harness
boundary.** The layering from §4 makes each tier trivially placed —
you already know what to mock because the interface tells you.

**Test speed is a feature.** Fast tests get run constantly; slow
tests get skipped. Targets:
- Tier 1 (unit): sub-second per file, total wall-clock < 30s
- Tier 2 (integration): ~2 min total wall-clock via parallelization
- Tier 3 (synthetic): ~5 min for the full scenario suite

**Every test asserts one thing.** No mega-tests that check ten
outcomes. If the test name has "and" in it, split it.

**Recorded over generated for realistic shapes.** Domain values are
generated via factories with sensible defaults. External-service
responses (API-Football JSON, Twitter search results, joi validation
outputs) are RECORDED from real incidents and replayed via fakes.
This keeps scenarios valid against real-world response shapes.

**Tests are documentation.** A well-named `TestValidateFrames_TwoFramesDisagree_CallsTiebreaker`
tells the reader what the behavior is, more reliably than a comment
that might rot.

### The three tiers

| Tier | What | Runs against | Speed | Coverage responsibility |
|---|---|---|---|---|
| **Tier 1** — Unit | Domain services + pure helpers | Mocked stores + fakes for adapters | Milliseconds | ≥ 70% for `service.go`, ≥ 80% for `lifecycle.go` per §4 |
| **Tier 2** — Integration | Stores + use-cases + adapters | Real Postgres + Garage via testcontainers-go; fakes for LLM + Twitter | 100ms-1s per test | ≥ 50% for `store.go`, ≥ 80% for `internal/usecases/` |
| **Tier 3** — Synthetic | Full pipeline against dev-mode stack | Real Postgres + Garage + Temporal; drivers mock external services | 30-60s per scenario | 7+ named scenarios covering real incidents |
| **Workflow tests** (Tier 1.5) | Temporal workflow logic | `testsuite.WorkflowTestSuite` with mocked activities | Milliseconds | 100% of workflow branches |

### Tier 1: unit tests

**Factories** live in `internal/testutil/factories/`. Functional-options
pattern gives Pydantic-equivalent ergonomics in Go — sensible defaults
that individual tests can override for their specific case.

**Example — `internal/testutil/factories/event.go`:**

```go
package factories

import (
    "time"
    "github.com/google/uuid"
    "found-footy/internal/domain/event"
)

type EventOption func(*event.Event)

func WithFixtureID(id int64) EventOption      { return func(e *event.Event) { e.FixtureID = id } }
func WithNaturalKey(k string) EventOption      { return func(e *event.Event) { e.NaturalKey = k } }
func WithType(t event.Type) EventOption        { return func(e *event.Event) { e.Type = t } }
func WithPlayer(id int, name string) EventOption {
    return func(e *event.Event) { e.PlayerID = &id; pn := name; e.PlayerName = &pn }
}
func WithMinute(m int) EventOption             { return func(e *event.Event) { e.Minute = m } }
func WithMonitorWorkflowsCount(n int) EventOption {
    // Note: doesn't actually populate the tracking table; that's a store concern.
    // For pure-unit tests where the service is asking "how many monitor workflows",
    // pair with WithMockedStore(...) that returns this count.
    return func(e *event.Event) { /* used with mocks */ }
}
func Removed(reason string) EventOption {
    return func(e *event.Event) { e.Removed = true; e.RemovedReason = &reason }
}
func NoPlayer() EventOption { return func(e *event.Event) { e.PlayerID = nil; e.PlayerName = nil } }

// NewTestEvent constructs a realistic Event with defaults; opts override.
func NewTestEvent(opts ...EventOption) *event.Event {
    id, _ := uuid.NewRandom()
    e := &event.Event{
        ID:            id,
        FixtureID:     999_999,
        NaturalKey:    "1_2_Goal_1",
        Type:          event.TypeGoal,
        Detail:        "Normal Goal",
        TeamID:        1,
        TeamName:      "Test FC",
        PlayerID:      intPtr(2),
        PlayerName:    strPtr("Test Player"),
        Minute:        23,
        FirstSeenAt:   time.Now().UTC(),
        MonitorComplete: false,
        DownloadComplete: false,
        Removed:       false,
    }
    for _, opt := range opts {
        opt(e)
    }
    return e
}
```

**Mocked stores** — one hand-rolled minimal implementation per store
interface. Not `mockery`-generated (too much magic) — plain Go structs
implementing the interface with fields for "what was called" and
"what to return."

**Example — `internal/testutil/mocks/event_store.go`:**

```go
package mocks

import (
    "context"
    "github.com/google/uuid"
    "found-footy/internal/domain/event"
)

type EventStore struct {
    // What was called
    GetByIDCalls  []uuid.UUID
    UpsertCalls   []*event.Event
    RegisterMonitorCalls []struct{ EventID uuid.UUID; WorkflowID string }

    // What to return
    GetByIDReturns map[uuid.UUID]*event.Event   // keyed by ID → returned Event
    UpsertReturns  func(*event.Event) (*event.Event, bool, error)  // functor for flexible per-call
    RegisterMonitorReturns func(uuid.UUID, string) (int, error)
    // etc.
}

func (s *EventStore) GetByID(ctx context.Context, id uuid.UUID) (*event.Event, error) {
    s.GetByIDCalls = append(s.GetByIDCalls, id)
    if e, ok := s.GetByIDReturns[id]; ok {
        return e, nil
    }
    return nil, event.ErrNotFound
}

func (s *EventStore) Upsert(ctx context.Context, e *event.Event) (*event.Event, bool, error) {
    s.UpsertCalls = append(s.UpsertCalls, e)
    if s.UpsertReturns != nil {
        return s.UpsertReturns(e)
    }
    return e, true, nil  // default: created
}

// ... etc for the rest of the interface
```

**Example unit test — `internal/domain/event/service_test.go`:**

```go
package event_test

import (
    "context"
    "testing"
    "github.com/stretchr/testify/require"
    "found-footy/internal/domain/event"
    "found-footy/internal/testutil/factories"
    "found-footy/internal/testutil/mocks"
)

func TestRegisterMonitorAndCheckStable_ThreeWorkflowsPlayerKnown_ReturnsStable(t *testing.T) {
    t.Parallel()

    store := &mocks.EventStore{
        GetByIDReturns: map[uuid.UUID]*event.Event{
            testEventID: factories.NewTestEvent(factories.WithPlayer(2, "Real Player")),
        },
        RegisterMonitorReturns: func(_ uuid.UUID, _ string) (int, error) { return 3, nil },
    }
    svc := event.NewService(store, event.Config{MonitorThreshold: 3})

    stable, err := svc.RegisterMonitorAndCheckStable(context.Background(), testEventID, "wf-A")

    require.NoError(t, err)
    require.True(t, stable)
    require.Len(t, store.RegisterMonitorCalls, 1)
}

func TestRegisterMonitorAndCheckStable_ThreeWorkflowsPlayerUnknown_ReturnsNotStable(t *testing.T) {
    t.Parallel()

    store := &mocks.EventStore{
        GetByIDReturns: map[uuid.UUID]*event.Event{
            testEventID: factories.NewTestEvent(factories.NoPlayer()),
        },
        RegisterMonitorReturns: func(_ uuid.UUID, _ string) (int, error) { return 3, nil },
    }
    svc := event.NewService(store, event.Config{MonitorThreshold: 3})

    stable, err := svc.RegisterMonitorAndCheckStable(context.Background(), testEventID, "wf-A")

    require.NoError(t, err)
    require.False(t, stable)
}

func TestRegisterMonitorAndCheckStable_TwoWorkflowsPlayerKnown_ReturnsNotStable(t *testing.T) { /* … */ }
func TestRegisterMonitorAndCheckStable_MonitorAlreadyComplete_ReturnsNotStable(t *testing.T) { /* … */ }
func TestRegisterMonitorAndCheckStable_StoreError_PropagatesErr(t *testing.T) { /* … */ }
```

**`t.Parallel()` on every unit test.** Fast unit tests should
parallelize. Nothing shared; failures are isolated.

**Coverage target: ≥ 70% on `service.go`, ≥ 80% on `lifecycle.go`.**
Enforced in CI (see below).

### Tier 2: integration tests

Real Postgres + Garage via `testcontainers-go`. Slow enough to not
run on every save, fast enough to run on every PR.

**Test container setup — `internal/testutil/tc/postgres.go`:**

```go
package tc

import (
    "context"
    "testing"
    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/modules/postgres"
    "github.com/testcontainers/testcontainers-go/wait"
    "found-footy/internal/infra/pg"
)

func NewPostgres(t *testing.T) (pg.Pool, func()) {
    t.Helper()
    ctx := context.Background()

    container, err := postgres.RunContainer(ctx,
        testcontainers.WithImage("postgres:16-alpine"),
        postgres.WithDatabase("found_footy_test"),
        postgres.WithUsername("test"),
        postgres.WithPassword("test"),
        testcontainers.WithWaitStrategy(
            wait.ForLog("database system is ready to accept connections").
                WithOccurrence(2).
                WithStartupTimeout(30*time.Second),
        ),
    )
    if err != nil { t.Fatal(err) }

    dsn, _ := container.ConnectionString(ctx, "sslmode=disable")

    // Run migrations
    if err := pg.RunMigrationsFromDSN(ctx, dsn, migrations.FS); err != nil {
        t.Fatal(err)
    }

    pool, err := pg.New(ctx, pg.Config{DSN: dsn}, slog.Default())
    if err != nil { t.Fatal(err) }

    cleanup := func() {
        pool.Close()
        _ = container.Terminate(ctx)
    }
    return pool, cleanup
}
```

Test-container reuse — `testcontainers.WithReuse(true)` keeps a
Postgres container alive across `go test` invocations. Startup cost
paid once per developer session, not per test run. In CI, containers
are fresh per job.

**Example integration test — `internal/domain/event/store_test.go`:**

```go
//go:build integration
// +build integration

package event_test

import (
    "context"
    "testing"
    "github.com/stretchr/testify/require"
    "found-footy/internal/domain/event"
    "found-footy/internal/testutil/tc"
    "found-footy/internal/testutil/factories"
)

func TestStore_Upsert_ConcurrentNaturalKeyRace_OneWinsOtherReturnsExisting(t *testing.T) {
    pool, cleanup := tc.NewPostgres(t)
    defer cleanup()

    store := event.NewPGStore(pool)
    ctx := context.Background()

    // Two concurrent goroutines try to upsert the SAME natural_key
    // Simulates the race MonitorWorkflow could hit if two Temporal instances
    // both detected "Goal by player 234, first seen"
    ev1 := factories.NewTestEvent(factories.WithFixtureID(999), factories.WithNaturalKey("1_2_Goal_1"))
    ev2 := factories.NewTestEvent(factories.WithFixtureID(999), factories.WithNaturalKey("1_2_Goal_1"))
    // ev1 and ev2 have DIFFERENT ID (UUIDs) but SAME (fixture_id, natural_key)

    var results [2]struct{ Event *event.Event; Created bool; Err error }
    var wg sync.WaitGroup
    wg.Add(2)
    go func() {
        defer wg.Done()
        results[0].Event, results[0].Created, results[0].Err = store.Upsert(ctx, ev1)
    }()
    go func() {
        defer wg.Done()
        results[1].Event, results[1].Created, results[1].Err = store.Upsert(ctx, ev2)
    }()
    wg.Wait()

    // Exactly one won the race — Created=true; the other got Created=false with the winner's Event returned
    var createdCount int
    for _, r := range results {
        require.NoError(t, r.Err)
        if r.Created { createdCount++ }
    }
    require.Equal(t, 1, createdCount, "exactly one Upsert should have created")

    // Both got back the SAME ID — the winning row's UUID
    require.Equal(t, results[0].Event.ID, results[1].Event.ID)
}

func TestStore_RegisterMonitorWorkflow_DuplicateWorkflowID_Idempotent(t *testing.T) {
    pool, cleanup := tc.NewPostgres(t)
    defer cleanup()

    store := event.NewPGStore(pool)
    seed := factories.NewTestEvent()
    _, _, _ = store.Upsert(ctx, seed)

    count1, _ := store.RegisterMonitorWorkflow(ctx, seed.ID, "wf-A")
    count2, _ := store.RegisterMonitorWorkflow(ctx, seed.ID, "wf-A")  // same workflow, retry

    require.Equal(t, 1, count1)
    require.Equal(t, 1, count2, "duplicate registration must be idempotent")
}

func TestStore_TryMarkDownloadComplete_TenWorkflowsRegistered_Flips(t *testing.T)
func TestStore_TryMarkDownloadComplete_NineWorkflowsRegistered_NoFlip(t *testing.T)
```

**`//go:build integration`** tag: integration tests only run when
`go test -tags=integration` is invoked. `make test` runs unit tests
only (fast); `make test-integration` runs both.

**Garage integration** uses a similar `tc.NewGarage(t)` helper that
spins up `dxflrs/garage:latest` in a container.

**Fake LLM + Fake Twitter** live in `internal/testutil/fakes/`:

```go
package fakes

// FakeLLM implements llm.Client with programmable responses.
type FakeLLM struct {
    ChatResponses      map[string]*llm.ChatResponse  // key: hash of request
    MultiImageResponses map[string]*llm.ChatResponse
    EmbeddingResponses  map[string]*llm.EmbeddingResponse

    // If set, called before returning to give tests fine control
    OnChatCall      func(llm.ChatRequest) (*llm.ChatResponse, error)
    OnMultiImageCall func(llm.MultiImageRequest) (*llm.ChatResponse, error)

    Calls []interface{}  // record everything for assertion
}

func (f *FakeLLM) ChatCompletion(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
    f.Calls = append(f.Calls, req)
    if f.OnChatCall != nil {
        return f.OnChatCall(req)
    }
    if resp, ok := f.ChatResponses[hashRequest(req)]; ok {
        return resp, nil
    }
    return nil, llm.ErrUnavailable
}
// ... etc.
```

Tests either register canned responses OR provide an `OnCall` functor
for maximum flexibility. Both patterns supported.

### Tier 3: synthetic harness

The end-to-end scenario replays that make regression testing tractable.
Lives at `test/synthetic/`.

**Directory structure:**

```
test/synthetic/
├── scenarios/                      # YAML scenarios
│   ├── happy_path_one_goal.yaml
│   ├── var_disallowed_goal.yaml
│   ├── twitter_returns_zero.yaml
│   ├── mostly_portrait_phone_cams.yaml    # NL-Morocco replay
│   ├── scorer_reattribution.yaml
│   ├── suspension_and_resume.yaml
│   ├── truncated_snowflake_urls.yaml
│   └── ...
├── fixtures/
│   ├── api_football/               # canned API-Football responses (JSON)
│   ├── twitter_searches/           # canned twitter service /search responses
│   ├── joi_vision/                 # canned vision LLM responses per (image_hash, prompt_hash)
│   └── sample_videos/              # small real .mp4 files for download path
├── drivers/
│   ├── api_football_driver.go      # in-process HTTP server serving canned responses
│   ├── twitter_service_driver.go   # ditto for twitter container's HTTP surface
│   └── joi_driver.go               # ditto for the LLM endpoint
├── runner/
│   ├── runner.go                   # orchestrates: bring up compose, load scenario, execute, assert
│   └── assertions.go               # helpers for asserting on Postgres/Garage end state
└── main_test.go                    # go test entrypoint that walks scenarios
```

**Scenario YAML shape:**

```yaml
# test/synthetic/scenarios/happy_path_one_goal.yaml
name: happy_path_one_goal
description: |
  One fixture, one goal at minute 23. Twitter returns 5 clips; 3 pass
  aspect/duration filter; 2 are validated as soccer with matching clocks;
  both upload to S3. End state: 1 event with 2 video_shares ranked 1 and 2.

setup:
  fixtures:
    - id: 1234567
      state: active
      # ... (full APIFootball response shape)

  drivers:
    api_football:
      - request: "GET /fixtures?id=1234567"
        response_file: fixtures/api_football/happy_path/one_goal_active.json
      - request: "GET /fixtures?id=1234567"
        response_file: fixtures/api_football/happy_path/one_goal_with_goal.json
        after_seconds: 60  # after the debounce cycle triggers
    twitter:
      - request: "POST /search with query matching Salah|Liverpool"
        response_file: fixtures/twitter_searches/happy_path/5_videos.json
    joi:
      - request: "POST /v1/chat/completions with image hash sha256:abc..."
        response_file: fixtures/joi_vision/happy_path/soccer_yes_clock_23.json

execute:
  workflow: IngestWorkflow
  input:
    manual_fixture_ids: [1234567]

wait_for:
  condition: "fixture_1234567.state == 'completed'"
  timeout_seconds: 120

expected_end_state:
  postgres:
    fixtures:
      - id: 1234567
        state: completed
        events_count: 1
    events:
      - fixture_id: 1234567
        event_natural_key: "1_2_Goal_1"
        download_complete: true
        removed: false
        shares_count: 2
        shares_ranks: [1, 2]
        shares_all_verified: true
    video_assets:
      count: 2  # both new; no dedup collisions
    video_shares:
      count: 2
      all_active: true

  garage:
    bucket: found-footy-test
    key_prefix: "1234567/"
    objects_count: 2  # one per canonical asset

  telemetry:
    coverage_rate: 1.0
    goals_with_zero_videos: 0
```

**Runner** — `test/synthetic/runner/runner.go`:

```go
package runner

import (
    "context"
    "testing"
    "gopkg.in/yaml.v3"
)

type Scenario struct {
    Name        string          `yaml:"name"`
    Description string          `yaml:"description"`
    Setup       SetupSpec       `yaml:"setup"`
    Execute     ExecuteSpec     `yaml:"execute"`
    WaitFor     WaitSpec        `yaml:"wait_for"`
    ExpectedEndState EndState   `yaml:"expected_end_state"`
}

// Run executes a scenario against a running test stack.
// Assumes: dev docker-compose is up, test databases are drop-recreated per scenario.
func Run(t *testing.T, ctx context.Context, scenarioPath string) {
    scenario := loadScenario(t, scenarioPath)

    // 1. Drop + recreate test schemas
    dropAndRecreateTestSchemas(t)

    // 2. Register canned responses with the drivers
    apiFootballDriver.LoadScenario(scenario.Setup.Drivers.APIFootball)
    twitterDriver.LoadScenario(scenario.Setup.Drivers.Twitter)
    joiDriver.LoadScenario(scenario.Setup.Drivers.Joi)

    // 3. Seed initial fixtures
    seedFixtures(t, scenario.Setup.Fixtures)

    // 4. Trigger the workflow via Temporal client
    triggerWorkflow(t, ctx, scenario.Execute)

    // 5. Wait for the condition
    waitForCondition(t, ctx, scenario.WaitFor)

    // 6. Assert end state
    assertPostgresState(t, ctx, scenario.ExpectedEndState.Postgres)
    assertGarageState(t, ctx, scenario.ExpectedEndState.Garage)
    assertTelemetry(t, ctx, scenario.ExpectedEndState.Telemetry)
}
```

**`main_test.go`** walks the `scenarios/` directory and runs each as a
sub-test:

```go
func TestSynthetic(t *testing.T) {
    ctx := context.Background()

    scenariosDir := filepath.Join("scenarios")
    files, err := os.ReadDir(scenariosDir)
    require.NoError(t, err)

    for _, f := range files {
        if !strings.HasSuffix(f.Name(), ".yaml") { continue }
        t.Run(f.Name(), func(t *testing.T) {
            runner.Run(t, ctx, filepath.Join(scenariosDir, f.Name()))
        })
    }
}
```

Runs against a docker-compose stack pointed at test-only Postgres +
Garage instances (test data plane is separate from dev). Wall-clock:
~30-60s per scenario, ~5 minutes for the seven-scenario baseline
suite. Runs on every PR via CI.

**Adding a scenario from a real prod incident:**

`scripts/capture_scenario.sh` records real HTTP traffic during an
incident and produces a scenario YAML template. Workflow:

1. Notice an incident in prod
2. `scripts/capture_scenario.sh --fixture <id> --window '30min'` — grabs API-Football responses, Twitter search results, joi calls from Loki + Postgres over the window
3. Fills in a scenario YAML with the captured request/response pairs
4. Edit the `expected_end_state` block to describe what SHOULD have happened
5. Run it: `go test -tags=synthetic ./test/synthetic/... -run <scenario_name>`
6. If it fails (as it should — the prod code failed), you have a
   regression test for the fix
7. Land the fix, the test passes, commit both

The NL-Morocco `mostly_portrait_phone_cams` scenario is exactly this
workflow. It would have caught the aspect-ratio filter regression
before it shipped.

### Workflow tests

Temporal's `testsuite.WorkflowTestSuite` runs workflows in-process
with mocked activities. Runs in milliseconds.

**Example — `internal/workflow/discovery_test.go`:**

```go
package workflow_test

import (
    "testing"
    "github.com/stretchr/testify/require"
    "github.com/stretchr/testify/mock"
    "go.temporal.io/sdk/testsuite"
    "found-footy/internal/activity"
    "found-footy/internal/workflow"
    "found-footy/internal/domain/discovery"
)

func TestDiscoveryWorkflow_HappyPath_SpawnsTenDownloads(t *testing.T) {
    ts := &testsuite.WorkflowTestSuite{}
    env := ts.NewTestWorkflowEnvironment()

    // Mock activities in call order
    env.OnActivity(activity.GetOrResolveTeamAliases, mock.Anything, 40).
        Return([]string{"Liverpool", "LFC"}, nil).Once()
    env.OnActivity(activity.SaveEventTwitterAliases, mock.Anything, ...).
        Return(nil).Once()

    // 10 iterations of the attempt loop — each returns some videos
    for i := 0; i < 10; i++ {
        env.OnActivity(activity.CheckEventStillLive, mock.Anything, mock.Anything).
            Return(true, nil).Once()
        env.OnActivity(activity.CountDownloadWorkflowsForEvent, mock.Anything, mock.Anything).
            Return(i, nil).Once()  // 0..9 workflows registered so far
        env.OnActivity(activity.SearchTwitter, mock.Anything, mock.Anything).
            Return(&discovery.SearchResponse{Videos: sampleVideos(5)}, nil).Once()
        env.OnActivity(activity.RegisterEventDiscoveredVideo, mock.Anything, mock.Anything, mock.Anything).
            Return(nil).Times(5)
    }

    // Downloads spawn as child workflows — mock them
    env.OnWorkflow(workflow.DownloadWorkflow, mock.Anything, mock.Anything).
        Return(&workflow.DownloadWorkflowOutput{}, nil)

    env.ExecuteWorkflow(workflow.DiscoveryWorkflow, workflow.DiscoveryWorkflowInput{
        EventID:     testUUID,
        MaxAttempts: 10,
        // ...
    })

    require.True(t, env.IsWorkflowCompleted())
    require.NoError(t, env.GetWorkflowError())

    var out workflow.DiscoveryWorkflowOutput
    require.NoError(t, env.GetWorkflowResult(&out))
    require.Equal(t, 10, out.DownloadsSpawned)
    require.Equal(t, "", out.EarlyExitReason)  // completed normally
}

func TestDiscoveryWorkflow_TwoConsecutiveEmpty_EarlyExits(t *testing.T) {
    // ... asserts EarlyExitReason == "consecutive_empty"
}

func TestDiscoveryWorkflow_EventVARdMidLoop_EarlyExitsRemoved(t *testing.T) {
    // ... asserts EarlyExitReason == "event_removed"
}
```

Workflow tests should cover:
- Happy path (all activities succeed, workflow returns expected output)
- Every early-exit branch
- Every retry policy (activity fails once, then succeeds)
- Every signal handler (for signal-driven workflows like UploadWorkflow)
- Every child-workflow spawn path

100% workflow branch coverage is the target. Cheap because these run
in milliseconds.

### Assertion helpers

**Logging assertions** — `internal/logging.WithTestEmitter(t)`:

```go
func TestSomeService_OnFailure_LogsCorrectly(t *testing.T) {
    emitter := logging.NewTestEmitter(t)
    ctx := logging.IntoContext(context.Background(), emitter)

    svc := someService{...}
    svc.DoThing(ctx)

    // Assert a specific line was emitted
    emitter.AssertEmitted(t,
        logging.WARN,
        vocabulary.ModuleSome,
        vocabulary.ActionSomethingFailed,
    )

    // Assert field values
    line := emitter.MustFind(vocabulary.ActionSomethingFailed)
    require.Equal(t, "reason_x", line.Fields["reason"])
}
```

**Metrics assertions** — `internal/metrics.WithTestRegistry(t)`
returns a Prometheus registry that captures metric emissions:

```go
reg := metrics.WithTestRegistry(t)
svc.DoWork(...)
require.Equal(t, 1, reg.Counter("found_footy_video_dedup_hit_total").Get())
```

**Time control** — `internal/testutil/clock.Fake` for deterministic
tests of time-based logic:

```go
clock := clock.NewFake(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC))
svc := fixture.NewService(store, fixture.Config{Now: clock.Now})
// clock.Advance(5 * time.Minute) etc.
```

### Coverage enforcement

`.github/workflows/ci.yml` runs tests + enforces per-package coverage
floors:

```yaml
- name: Unit tests
  run: go test -race -short -coverprofile=coverage.unit.out ./...

- name: Integration tests
  run: go test -race -tags=integration -coverprofile=coverage.integration.out ./...

- name: Enforce coverage floors
  run: |
    ./scripts/check-coverage.sh coverage.unit.out
    # Checks:
    #   internal/domain/*/service.go       ≥ 70%
    #   internal/domain/*/lifecycle.go     ≥ 80%
    #   internal/domain/*/store.go         ≥ 50% (unit coverage; store tests exercise more via integration)
    #   internal/usecases/*                ≥ 70%
    #   internal/workflow/*                ≥ 90%
    #   internal/api/handlers/*            ≥ 60%
```

`scripts/check-coverage.sh` parses the profile, groups by directory,
compares each against a target from a config file
(`.coverage-targets.yaml`). Failure blocks the PR merge.

**Coverage targets rationale:**
- Services need high coverage — business logic
- Lifecycle needs highest — state machine correctness
- Stores need less unit coverage because integration tests
  cover the SQL paths
- Workflows need highest — every branch must be exercised because
  workflow bugs are hard to notice in prod

### Running tests

**Local:**

```bash
# Fast — unit tests only
make test

# All — unit + integration
make test-integration

# Specific package
go test -race ./internal/domain/event/...

# With coverage
go test -race -cover ./internal/domain/event/...

# Synthetic scenarios (requires dev stack running)
make test-synthetic
# or: go test -tags=synthetic ./test/synthetic/...

# Watch mode (via reflex or similar)
reflex -r '\.go$' -- go test -race -short ./...
```

**CI:**

```yaml
# .github/workflows/ci.yml (indicative)
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - name: Cache Go modules
        uses: actions/cache@v4
        with:
          path: ~/go/pkg/mod
          key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}

      - name: Unit tests
        run: make test

      - name: Lint
        run: golangci-lint run

      - name: Integration tests (spins up testcontainers)
        run: make test-integration

      - name: Coverage check
        run: ./scripts/check-coverage.sh coverage.unit.out coverage.integration.out

      - name: Upload coverage
        uses: codecov/codecov-action@v4  # or self-hosted alt
        with:
          files: coverage.unit.out,coverage.integration.out
```

Synthetic tests run in a separate workflow because they need Postgres +
Garage + Temporal — heavier setup. Triggered on PRs but on a slower
cadence.

### Test data lifecycle

**When to use which:**

- **Factory** — happy path, non-error case, "here's what a typical
  Event looks like." Fast, tunable via functional options.
- **Recorded fixture** — when the shape of an external response
  matters (API-Football's specific JSON structure, Twitter's specific
  tweet URL format, joi's specific model output). Captured from real
  data.
- **Scenario** — end-to-end regression test for a real prod
  incident. Long-lived; documents "this failure mode is prevented."

**Fixture rot handling.** External APIs change shapes. When
API-Football changes their fixture endpoint response, our recorded
fixtures go stale. Two safeguards:

1. `scripts/refresh-fixtures.sh` re-records against the live API for
   a known set of fixture IDs; commit the diff.
2. `scripts/check-fixture-schemas.sh` runs a schema validator against
   the recorded fixtures on every CI run and warns if the schema they
   were captured against is drifting.

### Extensibility hooks

**Adding a new domain's tests** — the pattern from §4 says every
domain ships `service_test.go` + `store_test.go` + `lifecycle_test.go`
in the same PR. Coverage floors kick in automatically once the code
lands.

**Adding a new tier-3 scenario** — YAML file in `scenarios/`,
recorded fixtures under `fixtures/`. Runner picks it up
automatically via directory walk.

**Adding a new assertion helper** — extend
`internal/testutil/assertions/` with `AssertEventInState(t, pool,
eventID, expectedState)` style helpers as patterns emerge across
tests. Shared across all test tiers.

**Property-based tests** — deferred but not blocked. `pgregory.net/rapid`
integrates fine with the factory pattern. Good candidates: query
normalization in `discovery.BuildQuery`, hash Hamming distance
symmetry in `vision.ComputeDHash`.

**Fuzz tests** — Go 1.18+ built-in fuzzing. Good candidates: URL
validation in `discovery.ValidateURL`, JSON parsing in
`vision.parseValidationResponse`, snowflake extraction in
`syndication.ValidateStatusURL`.

---

## 13. Migration

**Scope note: prod-only.** Dev is decoupled by convention (`found-footy-prod-*` containers on `found-footy-prod` network vs `found-footy-dev-*` on `found-footy-dev`) — the new dev stack lives at different container names, on a different network, with different data volumes. No migration between old and new dev is needed: bring up the new dev stack from scratch, delete the old dev volumes when convenient, done. Everything below is about how **prod** transitions without losing user-visible state.

### What has to survive

Not everything in current prod needs to make the jump. Honest inventory:

| Current prod state | What it is | Preserve? | Why |
|---|---|---|---|
| `fixtures_completed` documents (14-day retention window) | ~178 goals + ~475 videos of accumulated match history | **Yes** — video URLs are shared externally + og-server serves OG cards for them | Public URL stability from audit §4 = an invariant, not an aspiration |
| Recent `fixtures_active` documents | In-flight fixtures at cutover moment | **Yes if any live at cutover** | Losing an in-progress fixture = coverage gap during the cutover window |
| `fixtures_live` overwrite buffer | Raw API snapshots | **No** — it's a Mongo-specific workaround (§3 principle 4). Discarded on cutover. |
| `team_aliases` (RAG cache) | ~150 team → aliases mappings | **Optional** — RAG re-runs on cache miss. Preserving saves ~30s per team on first use post-cutover. |
| `fixtures_staging` documents | Upcoming fixtures already fetched from API-Football | **No** — IngestWorkflow re-fetches on new stack; API-Football is source of truth |
| MinIO video files | The actual `.mp4` blobs | **Yes** — same reason as `fixtures_completed`: URLs are shared |
| Twitter cookies (twitter container profile dir) | Firefox cookies + login state | **Yes** — losing them = manual VNC re-auth during cutover, which is annoying and observable to the frontend |
| Loki historical logs | Structured JSON logs from Python runtime | **Yes, read-only** — kept for forensics; not part of the new stack's active telemetry |
| Prometheus historical metrics | Time-series from the Python stack | **Yes, read-only** — same reason |
| Temporal workflow histories | Completed workflow execution records | **No** — expired workflows are noise; a fresh Temporal cluster is fine |

Three preservation categories → three migration strategies below.

### Category A: high-preservation state (fixtures + videos)

The `fixtures_completed` documents + their MinIO blobs are the load-bearing case. Public share URLs pointing at MinIO objects need to keep resolving indefinitely, and og-server has to keep generating OG cards.

Two approaches, with tradeoffs.

**A.1: Frozen legacy read-only service** (recommended day-one).

The current Python found-footy stack keeps running post-cutover in **read-only** mode as `found-footy-legacy-*` containers. New writes go exclusively to the new Postgres/Garage stack. Old share URLs continue to hit the legacy MinIO. A thin compatibility layer inside the new API surfaces legacy content when the new Postgres doesn't have a match.

Container renames on cutover day:
- `found-footy-prod-mongo` → `found-footy-legacy-mongo` (compose rename, no data touched)
- `found-footy-prod-minio` → `found-footy-legacy-minio`
- Old worker, twitter, scaler containers: stopped, not renamed. They're not needed.

Legacy Caddyfile keeps the old share URLs alive:

```caddy
# ~/workspace/proxy/caddy/caddy.d/found-footy-legacy.caddy
http://found-footy-legacy-minio.{$BASE_DOMAIN} {
    # Read-only; presigned URLs generated by legacy MinIO still resolve here
    reverse_proxy found-footy-legacy-minio:9000
}
```

Compatibility layer in the new API — `internal/api/handlers/videos.go` share-id endpoint:

```go
func getShareVideo(ctx context.Context, in *GetShareVideoInput) (*GetShareVideoOutput, error) {
    // Try new Postgres first
    share, err := deps.VideoShares.GetByID(ctx, in.ShareID)
    if err == nil {
        return handleNewShare(ctx, deps, share)
    }
    if !errors.Is(err, video.ErrShareNotFound) {
        return nil, err
    }

    // Fallback: legacy compat lookup
    legacyURL, err := deps.LegacyCompat.LookupLegacyShare(ctx, in.ShareID)
    if err == nil {
        // Redirect to the legacy MinIO URL (unchanged since old prod)
        return &GetShareVideoOutput{
            Status:       http.StatusFound,
            Location:     legacyURL,
            CacheControl: "no-store",
        }, nil
    }

    return nil, huma.Error404NotFound("share not found")
}
```

`LegacyCompat` is a small read-only client to the frozen Mongo:

```go
// internal/infra/legacycompat/client.go
type Client interface {
    LookupLegacyShare(ctx context.Context, shareID string) (legacyURL string, err error)
}
```

Under the hood: `db.fixtures_completed.find({"events._s3_videos.share_id": shareID})` returning the old S3 URL. Note that current Python found-footy might not have `share_id` on legacy documents — it uses raw `_s3_key` and `s3_url`. Migration path needs to handle this — see A.2 below OR extend the compat layer to accept both share_id lookups AND raw legacy URLs (og-server may embed raw URLs from before share IDs existed).

**Pros of A.1:**
- Zero data-migration work up front — cut over immediately, backfill never (or later, at leisure)
- Legacy data can be pruned via the current 14-day retention (all pre-cutover documents will naturally expire within 14 days of cutover, at which point the legacy stack can be turned off entirely)
- No risk of migration errors corrupting either stack

**Cons of A.1:**
- Two Mongo instances running side-by-side (small; legacy read-only has minimal footprint)
- Legacy stack lives 14 days minimum post-cutover before it can be dropped
- Compat layer adds one lookup step to share-id resolution for legacy content (negligible latency; hits Mongo once, then cached)

**A.2: Full data migration** (deferred until "we want to keep some pre-cutover content beyond 14 days").

Only worth doing if we decide certain legacy content should persist indefinitely (e.g., "highlight reel of great WC goals from 2026" that we don't want disappearing after 14 days).

Migration script `scripts/migrate-legacy-data.go` (one-off):

```
1. Read each fixtures_completed document from legacy Mongo
2. Compute Postgres INSERT statements for:
   - fixtures (with state='completed')
   - events (UUID minted new; natural_key derived from legacy _event_id)
   - event_monitor_workflows, event_download_workflows (with the workflow_ids
     from _monitor_workflows / _download_workflows arrays — retention for audit)
3. For each _s3_video in the event:
   - Download the MP4 from legacy MinIO
   - Re-upload to new Garage
   - Compute perceptual_hash + md5 (may skip if already stored in legacy)
   - INSERT into video_assets
   - Mint a new video_share pointing at the asset AND at the legacy share_id
     (dual-key lookup: new share ID + preserved legacy share ID)
4. Write a migration log with the (legacy_share_id → new_share_id) mapping
```

Blast radius: high. Not day-one. Documented here for completeness because it's the escape hatch if we ever want it.

**Recommendation: A.1 for day-one cutover, A.2 only if lived experience warrants it.**

### Category B: opportunistic preservation (team_aliases, twitter cookies)

Small state, cheap to preserve.

**Team aliases:**

```go
// scripts/copy-team-aliases.go (one-off)
func main() {
    legacy := connectLegacyMongo()
    new := connectNewPostgres()

    cursor, _ := legacy.Collection("team_aliases").Find(ctx, bson.M{})
    for cursor.Next(ctx) {
        var doc struct {
            TeamID int      `bson:"_id"`
            TeamName string `bson:"team_name"`
            Aliases []string `bson:"twitter_aliases"`
            // ...
        }
        cursor.Decode(&doc)

        // Insert into new schema
        new.Exec(ctx, `
            INSERT INTO team_aliases (team_id, team_name, twitter_aliases, is_national, ...)
            VALUES ($1, $2, $3, ...)
            ON CONFLICT (team_id) DO UPDATE SET twitter_aliases = EXCLUDED.twitter_aliases
        `, doc.TeamID, doc.TeamName, doc.Aliases, /* ... */)
    }
}
```

Run once at cutover. If it fails or is skipped, the RAG pipeline just re-runs on first cache miss — no functional loss, just some LLM budget the first time each team is encountered.

**Twitter cookies:**

The trickier bit. Current prod stores cookies in the twitter container's Firefox profile at `/data/firefox_profile_<hash>/`. New prod stores them in the Postgres `twitter_sessions` table.

Migration approach:

```
1. During cutover window, stop scaler on legacy stack (freezes twitter replicas)
2. Read cookies from ANY healthy legacy twitter container's profile dir:
   docker exec found-footy-legacy-twitter-1 cat /data/firefox_profile_*/cookies.sqlite \
     | sqlite3 - .dump
   (or read the JSON export from ~/.config/found-footy/twitter_cookies.json which
   is already the safety-net format)
3. Import into new stack's twitter_sessions table:
   INSERT INTO twitter_sessions (id, cookies, cookies_version, authenticated, last_refresh_at)
   VALUES ('canonical', $1, 1, true, NOW())
4. Bring up new stack's twitter container; it reads cookies from Postgres per audit §8 fleet methodology
5. Verify authenticated by hitting new twitter container's /health
```

If this fails, fallback is manual VNC re-auth on the new stack (5-minute human operation). Acceptable but ideally avoided during cutover.

### Category C: read-only historical preservation (Loki logs, Prometheus metrics)

Nothing to do. Both are workspace-level stacks (`~/workspace/monitor/`) shared across all projects. Legacy log lines emitted by the Python found-footy stack stay queryable in Loki forever (subject to retention config, currently unbounded). Prometheus historical time-series stay queryable in Prometheus.

Post-cutover, log queries can distinguish Python vs Go emissions via the `container` label:

```logql
# All logs from Python era
{container=~"found-footy-prod-worker-[0-9]+"} |= "MODULE"

# All logs from Go era (new naming convention — MODULE field name unchanged)
{container=~"found-footy-prod-worker"} | json | module != ""
```

(The container name pattern differs slightly: Python replicas are `worker-1`, `worker-2` via docker-compose scale; Go replicas are `worker` with an underlying replica index. Both are grep-able.)

The one operational rule: **the Loki module/action vocabulary from §11 is a fresh discipline; historical Python logs use the older less-consistent vocabulary.** Queries that filter by the new vocabulary (`module="download_workflow", action="video_failed"`) match Python logs where they coincidentally used the same string. Where Python used something different (e.g., `module=download` singular vs new `module=download_workflow`), the query only matches post-cutover.

Fine to accept the discontinuity. Historical forensics on Python-era incidents uses Python-era vocabulary; new forensics on Go-era incidents uses Go-era vocabulary. The date of the container startup log line (`module=deploy action=startup`) is the boundary.

### The cutover window — timeline

**Day -7 (one week before cutover):** New prod stack stands up but doesn't take traffic. Legacy prod still fully serves. New stack runs against test data (dev-mode API-Football key, synthetic scenarios). Coverage dashboards go green in the new Grafana.

**Day -3:** Team aliases copied from legacy Mongo (opportunistic — cheap).

**Day -1:** No new features shipped to either stack. Freeze.

**Day 0, T-0 (cutover moment):**
1. Stop legacy scaler → prevents new workflow spawns
2. Wait for in-flight workflows to complete (max 20 min for a discovery loop; monitor Temporal UI for empty queue)
3. Copy twitter cookies from legacy twitter container to new Postgres `twitter_sessions`
4. Rename legacy containers to `found-footy-legacy-*`
5. `docker compose -f ~/workspace/dev/found-footy/docker-compose.yml up -d` — new prod stack starts
6. Wait for `/healthz` green on new API
7. Update Caddy: swap the `found-footy-prod-*` hostnames to point at the new containers. Add the legacy Caddyfile entries for MinIO backward-compat.
8. Verify: share-id endpoint hits legacy for old share URLs; hits new for freshly-minted ones

**Day 0, T+1h:** Manual verification checklist run through. Fixture ingest triggered manually if kickoff time is near.

**Day 0, T+24h:** First full day complete. Coverage rate compared to legacy baseline; new stack should match or exceed.

**Day +14:** Legacy `fixtures_completed` documents naturally expire (14-day retention). Legacy MinIO objects also expire on the same schedule. Legacy stack can be shut down.

**Day +30:** Legacy stack containers deleted. Legacy Caddyfile entry removed. Post-mortem review of cutover.

### Rollback plan

If new stack fails badly within Day 0:
1. Stop new stack (`docker compose down`)
2. Rename legacy containers back to `found-footy-prod-*`
3. Restart legacy scaler
4. Old prod is back within 5 minutes

The rollback is possible because we never destructively touched legacy data during cutover. Team aliases we copied → both stacks have them. Twitter cookies we copied → both stacks have them (legacy's are the source; copy went to new; nothing was overwritten).

The window when rollback becomes IRREVERSIBLE:
- Once new stack has processed a new fixture that legacy DIDN'T process, rolling back loses that fixture's data
- Practical: rollback window is ~24 hours. After a day of live traffic on new stack, we're committed.

### What the frontend sees during cutover

vedanta-systems doesn't need to know the cutover happened. Its API base URL is `found-footy-prod-api.<base-domain>` — Caddy simply reroutes the hostname to the new container.

SSE stream: `LISTEN`s on `found_footy_events` at the new Postgres. When vedanta-systems' SSE consumer reconnects post-cutover (Chrome auto-reconnects on disconnect), it hits the new stack. Any messages missed during the reconnect window are backfilled from `event_log` if the client sends `Last-Event-Id` — for pre-cutover events, the ID space is different, so `Last-Event-Id` won't backfill. Frontend sees a small blip; not user-visible unless they had the tab open during cutover.

Webhook subscribers: existing subscriptions live in legacy Mongo (Python didn't have webhooks per audit §11); vedanta-systems' backend re-registers via `POST /api/v1/webhooks/subscriptions` on its next startup post-cutover. Idempotent on `(consumer_name, url)`.

### Migration testing

Before the actual cutover, we test the migration itself:

1. Copy prod's `fixtures_completed` documents to a dev Mongo instance (`docker run mongodump ... > /tmp/backup && docker exec dev-mongo mongorestore /tmp/backup`)
2. Copy prod's MinIO blobs to a dev MinIO (`rclone sync minio1:/found-footy minio-dev:/found-footy-copy`)
3. Copy prod's twitter cookies to a dev twitter Firefox profile
4. Follow the cutover procedure against the dev copies
5. Verify all share URLs from the copy resolve correctly on the new dev stack (partly via legacy compat, partly via A.2 backfill if we chose to run it)

Rehearsed cutover run is part of the deploy checklist. Time-box: 90 minutes.

### Extensibility hooks

**Adding a new state category to preserve:**

1. Add to the inventory table above with preserve/discard rationale
2. Choose a strategy (frozen legacy read-only compat, full migration, opportunistic)
3. Write a migration script under `scripts/migrate-*.go` if applicable
4. Add to the cutover timeline at the appropriate T-relative day

**Post-cutover cleanup automation:**

`scripts/cleanup-legacy.sh` — runs 30 days post-cutover, removes legacy containers + Caddy entries + volumes. Safeguard: refuses to run if any share-id endpoint call has hit the legacy compat layer in the last 7 days (checks a Prometheus counter). This prevents accidentally cutting off share URLs that are still being resolved.

**Multi-stage cutover for very-high-risk scenarios:**

If we ever get nervous about full cutover, we can do it in stages:
- Stage 1: New stack handles new fixtures only; legacy handles everything else
- Stage 2: New stack + new API endpoints active; frontend still consumes legacy for reads
- Stage 3: Frontend switches over endpoint-by-endpoint (this is really §14 cutover territory)

Not day-one — a full cutover is preferable for simplicity. But the hooks exist if we want to be more cautious.

---

## 14. Cutover

§13 covered data continuity — how the *bytes* survive the transition.
§14 covers **traffic** — how consumers (vedanta-systems, og-server) migrate
from consuming the legacy Python stack's interface to the new Go stack's API,
and when the legacy stack finally gets turned off.

Two distinct migrations live inside "cutover":

1. **Stack cutover** — the moment the new Go stack becomes the writer of new
   data. Covered in §13's Day 0 timeline.
2. **Consumer cutover** — vedanta-systems + og-server migrating from
   direct-Mongo reads to the new API. This is what §14 is about.

The two are independent. Stack cutover happens once, atomically, on Day 0.
Consumer cutover happens **endpoint-by-endpoint**, over the Day 0 → Day +30
window, with individual rollback per endpoint.

### Current consumer topology (what needs to change)

Today's arrangement (Python stack + vedanta-systems as they run in prod):

```
Browser
   │  (via cloudflared tunnel + Caddy)
   ▼
vedanta-systems-prod (React + Express BFF)
   │
   ├── Express reads MongoDB directly       ──► found-footy-prod-mongo
   │   (found-footy-prod docker network + luv-prod cross-project net)
   │
   └── Express SSE endpoint                  ◄── found-footy-prod-worker
       (worker calls notify_frontend_refresh)      (backwards direction!)

og-server (separate small service in vedanta-systems)
   │
   └── Reads MongoDB directly for OG cards  ──► found-footy-prod-mongo
```

Three coupling paths, all direct-to-storage:
- **BFF → Mongo (queries)** — powers the fixtures/events/videos UI
- **Worker → vedanta-systems API (SSE trigger)** — the inverted-dependency SSE
- **og-server → Mongo (queries)** — powers OG card generation for shared links

Target topology (post-cutover):

```
Browser
   ▼
vedanta-systems-prod (React + Express BFF)
   │
   ├── Express calls found-footy API        ──► found-footy-prod-api
   │   (HTTP over luv-prod, Bearer auth)         (§8 endpoints)
   │
   └── Express SSE forwards               ◄── found-footy-prod-api
       (LISTEN/NOTIFY-backed stream)              (§8 SSE)

og-server
   │
   └── Calls share-id endpoint (unauthed)  ──► found-footy-prod-api
                                               (§4/§8 /api/v1/videos/{share_id})
```

Three replacements needed. Each is its own migration.

### Migration path A: BFF → Mongo replaced by BFF → API

**Current implementation in vedanta-systems** (per `AGENTS.md` in vedanta-systems: React + shadcn frontend + Express BFF that surfaces UIs for other projects):

The BFF has some subset of endpoints like `/api/found-footy/fixtures`,
`/api/found-footy/events/:id`, `/api/found-footy/videos`. Each reads Mongo
via `mongodb` npm package + hardcoded schema knowledge.

**Migration approach: endpoint-by-endpoint via feature flag.**

For each BFF endpoint:

1. Add a feature flag: `FOUND_FOOTY_API_ENABLED_FIXTURES`, `_EVENTS`,
   `_VIDEOS` etc. in vedanta-systems' env config.
2. Rewrite the BFF endpoint handler to check the flag:
   ```javascript
   // vedanta-systems/src/server/routes/found-footy.ts (indicative)
   app.get('/api/found-footy/fixtures', async (req, res) => {
     if (process.env.FOUND_FOOTY_API_ENABLED_FIXTURES === 'true') {
       // New: proxy to found-footy API
       const resp = await fetch(
         'http://found-footy-prod-api:8080/api/v1/fixtures?' + qs.stringify(req.query),
         { headers: { 'Authorization': `Bearer ${process.env.FOUND_FOOTY_API_TOKEN}` } }
       );
       return res.status(resp.status).send(await resp.text());
     }
     // Old: direct Mongo query
     const rows = await mongoColl.find(mapQueryToMongo(req.query)).toArray();
     return res.json({ fixtures: rows.map(mapMongoToResponseShape) });
   });
   ```
3. Deploy vedanta-systems with the new code + flag defaulted OFF (= old path).
4. Enable the flag in prod's `.env` — traffic flips to the new API path.
5. Monitor for anomalies (see success/rollback criteria below).
6. If clean for 48h: remove the feature flag + the old Mongo code
   entirely in a follow-up PR.
7. If problems: flip the flag off — instant rollback to old Mongo path.

**Migration order** (from lowest-blast-radius to highest):

| Order | Endpoint | Why this order |
|---|---|---|
| 1 | `GET /api/found-footy/videos/{share_id}` | Simplest — a 302 redirect. Failure mode: broken video links, but no data corruption. |
| 2 | `GET /api/found-footy/fixtures` | Read-heavy but static-shaped. Easiest to canary. |
| 3 | `GET /api/found-footy/events/{id}` | Adds complexity: joined videos array. Response projection has to match old shape. |
| 4 | `GET /api/found-footy/feed` | Highest-traffic; save for last when confidence is high. |
| 5 | (any admin endpoints) | If they exist in the BFF; probably don't. |

Each endpoint migration is a separate PR. Estimated: one PR per week
during the Day 0 → Day +30 window.

**Response-shape compatibility.** vedanta-systems' frontend expects specific
JSON shapes. The new API's response shapes from §8 (EventResponse, VideoLink,
etc.) may not match exactly. Two approaches:

- **A.1: BFF adapts.** The BFF fetches from the new API and reshapes the
  response to what the frontend expects. Old and new API can differ; BFF
  hides it. Simpler for now; a future PR can refactor the frontend to
  consume the new shape directly.
- **A.2: Frontend migrates too.** Frontend gets updated to consume the new
  shape. Requires coordinating a frontend PR with the BFF PR. More work,
  but cleaner end state.

**Recommendation: A.1 for the cutover window** — reshape at the BFF —
because it lets us cut over without frontend PRs holding things up. A.2
happens later at leisure, once the flag is confirmed good.

### Migration path B: SSE — invert the dependency direction

Current: worker calls `notify_frontend_refresh` on vedanta-systems' Express.
That's an inverted dependency: the writer (worker) has to know the reader's
(vedanta-systems') URL, keep an HTTP client, handle vedanta-systems being
down.

Target: vedanta-systems opens an SSE stream to the found-footy API. Data
flows normally: consumer → producer HTTP call, producer sends events on
the stream. Worker no longer needs to know vedanta-systems exists.

**Migration:**

1. Deploy new found-footy API with SSE endpoint live (§8). Not connected to
   vedanta-systems yet.
2. Add a new SSE-consumer path in vedanta-systems' Express — opens a
   connection to `found-footy-prod-api:/api/v1/sse/events` on startup,
   forwards messages to its own downstream SSE consumers (the browsers).
3. Feature flag: `FOUND_FOOTY_SSE_ENABLED`. When ON, both paths run in
   parallel:
   - New: vedanta-systems consumes the found-footy SSE stream
   - Old: worker still calls `notify_frontend_refresh`
4. Verify duplicate delivery is idempotent from the frontend's POV
   (browser components handle "same event twice" cleanly — check).
5. Once new path is stable for 48h: **worker stops calling
   `notify_frontend_refresh`**. In new found-footy code, this activity
   just doesn't exist. In the old Python code, the flag on
   vedanta-systems side ignores the call.
6. Remove the `notify_frontend_refresh` endpoint from vedanta-systems'
   BFF entirely in a follow-up PR.

**Rollback if the new path breaks:** flip `FOUND_FOOTY_SSE_ENABLED` off.
The old `notify_frontend_refresh` path takes over. If we've already
removed the endpoint (step 6), rollback requires re-adding it — hence
step 6 waits until we're highly confident.

**Backfill on reconnect** — audit §11 already noted that pre-cutover
`Last-Event-Id` values don't backfill because ID space differs.
Post-cutover reconnects backfill cleanly from `event_log.id` (bigserial
that started at 1 on the new stack).

### Migration path C: og-server → share-id endpoint

og-server is a small service in vedanta-systems. Its job: given a shared
video link, generate an OpenGraph card (`<meta name="og:image" ...>`,
`<meta name="og:video" ...>` etc.) that social platforms use for previews.

Current: reads Mongo directly, extracts the video URL + fixture metadata,
generates OG card with the raw MinIO URL embedded.

Target: hits the found-footy share-id endpoint to get the current
canonical URL, generates OG card with that URL. If the share is removed
(410 Gone), renders a "reversed by VAR" OG card.

**Migration:**

1. Update og-server to hit `GET /api/v1/videos/{share_id}` (unauthed).
2. Handle three response paths:
   - **302**: use `Location` header as the video URL in the OG card
   - **410**: use the response body's `reason` field to render an
     appropriate "removed" OG card
   - **404**: render a "content not available" card
3. Ship as a single PR (og-server is small; feature flag would be
   overkill).
4. Ship after the BFF share-id migration (Migration A, step 1) is
   stable, since og-server benefits from the same infrastructure.

**Compatibility with legacy share URLs**: og-server may receive share
URLs from tweets embedded BEFORE the rebuild. Those URLs won't have
share_ids per the new format — they'll be raw MinIO URLs like
`http://found-footy-prod-minio.<base>/footy/<fixture>/<event>/<hash>.mp4`.
Two approaches:

- og-server keeps a "legacy URL" code path that reads legacy Mongo
  directly for those URLs. Turns off Day +14 when retention purges.
- og-server relies on the new API's LegacyCompat layer from §13 —
  hitting `/api/v1/videos/legacy?url=<url>` returns the current
  canonical + metadata.

**Recommendation:** the new API exposes a small `/api/v1/videos/legacy`
helper endpoint (post-cutover) that lets og-server go through one unified
lookup path. Keeps og-server simple: hit one endpoint for both new and
legacy shares.

### Monitoring during cutover

Two Grafana dashboards get promoted during cutover (per §11):

**`found-footy-cutover`** — a temporary dashboard live from Day -1
through Day +30. Panels:

- **Traffic split per endpoint**: `%` of requests going to the new API
  vs the old Mongo path, per endpoint. Derived from BFF's feature flag
  logs. Manual toggle inspection possible from Grafana.
- **Response-time comparison**: p50/p95 for old-path vs new-path per
  endpoint. Alert if new-path degrades by > 2× old-path baseline.
- **Error rate per endpoint**: 4xx/5xx counts split by path. Alert on
  >1% error rate on new path for any endpoint.
- **Legacy compat hits**: count of share-id lookups falling through to
  the legacy compat layer per hour. Expected to trend down as legacy
  content ages out over Day 0 → Day +14. If it stays flat or increases,
  something's wrong (maybe pre-cutover URLs are being re-shared).
- **`notify_frontend_refresh` inbound rate**: expected to trend to zero
  as SSE migration completes. If it stays non-zero after step 5, worker
  is still calling — investigate.
- **Deploy drift**: still important (§11) — makes sure both stacks are
  running expected code.

**`found-footy-slo-comparison`** — new-stack coverage vs pre-cutover
baseline. Panel per league: coverage rate this week vs same week
pre-cutover. Alert if any league's coverage drops by > 10 percentage
points post-cutover — signals a regression that's not caught by other
metrics.

### Success criteria per migration step

Each of the endpoint migrations (A.1 through A.5, plus B and C) has
per-step success criteria. Doesn't advance to the next step until met.

**Stability window**: 48 hours between "flag flipped" and "advance to
next step." Long enough to catch overnight batch effects, short enough
to keep the cutover moving.

**Metrics thresholds** (green = advance, yellow = investigate, red =
rollback):

| Metric | Green | Yellow | Red |
|---|---|---|---|
| New-path error rate | < 0.1% | 0.1 – 1% | > 1% |
| New-path p95 vs old baseline | < 1.5× | 1.5 – 3× | > 3× |
| Coverage rate delta | > baseline − 5pp | baseline − 5pp to − 10pp | < baseline − 10pp |
| Log ERROR rate on new API | < 10/hr | 10 – 50/hr | > 50/hr |

Yellow = investigate but don't rollback yet. Red = rollback the flag
immediately + retrospective.

### Rollback triggers (post-cutover, whole-stack)

Beyond individual-endpoint rollbacks via feature flag, there are two
scenarios where the whole cutover gets reverted:

1. **Data loss.** If new stack loses or corrupts any user-visible data
   in the first 24 hours post-cutover, rollback (§13 rollback plan).
2. **Coverage regression > 30 percentage points** for any tracked
   league within 48h. Signals a functional bug in the new pipeline
   that isn't caught by tests. Rollback + root-cause + re-attempt.

Beyond 24h, rollback becomes irreversible (§13). After Day +1, we're
committed to the new stack; failures require forward-fix instead of
revert.

### Legacy stack turnoff — Day +30 decision

The final cutover step. Legacy stack has been serving read-only compat
for share URLs during Day 0 → Day +30. When does it actually turn off?

**Preconditions for turnoff** (all must be true):

1. **Legacy compat hit rate < 10/day for 7 consecutive days.** If old share
   URLs are still being resolved daily, keep the legacy stack up. This is
   the safeguard from §13's extensibility hooks.
2. **All BFF endpoints have migrated** (Migration A complete).
3. **SSE migration complete** (Migration B complete; `notify_frontend_refresh`
   endpoint deleted).
4. **og-server migration complete** (Migration C complete).
5. **No pending prod incident** blocked on legacy access.

**Turnoff sequence:**

```bash
# ~/workspace/dev/found-footy/scripts/turnoff-legacy.sh
# Run at Day +30 (approximate) once preconditions are met.

set -euo pipefail

echo ">> verifying preconditions"
LEGACY_HITS=$(curl -s http://monitor-prometheus:9090/api/v1/query?query='sum(rate(found_footy_legacy_compat_hits_total[7d]))' | jq -r '.data.result[0].value[1]')
if (( $(echo "$LEGACY_HITS > 0.0012" | bc -l) )); then  # ~10/day = 0.0012/sec
    echo "!! legacy compat still hit >10/day; not turning off"
    exit 1
fi

echo ">> stopping legacy containers"
docker compose -f docker-compose.legacy.yml down

echo ">> removing legacy caddy entries"
rm ~/workspace/proxy/caddy/caddy.d/found-footy-legacy.caddy
docker exec proxy-caddy caddy reload --config /etc/caddy/Caddyfile

echo ">> archiving legacy volumes (rsync to cold-storage location)"
rsync -a ~/workspace/data/found-footy-legacy/ ~/workspace/data/_archive/found-footy-legacy-$(date +%Y%m%d)/

echo ">> removing legacy volumes"
rm -rf ~/workspace/data/found-footy-legacy/

echo ">> removing legacy compat client from new API"
# Emit a maintenance PR — no runtime action needed here, just a followup task
echo "  → open a PR that removes internal/infra/legacycompat + its lookup in getShareVideo"

echo ">> legacy turnoff complete"
```

**Post-turnoff followup PR** — removes the `LegacyCompat` client from
`internal/infra/legacycompat/` and the lookup path from
`internal/api/handlers/videos.go`. Clean code, no dead paths.

### Post-cutover retrospective

Day +45 (two weeks after legacy turnoff), do a retrospective:

- What broke that we didn't expect?
- What worked that we did expect (and can build on)?
- Where did the timeline underrun / overrun? Update future planning.
- Anything in the audit or rebuild plan that turned out to be wrong?
  Update the docs.
- Anything the new stack is failing at that legacy did better? Address
  as follow-up work.

Retrospective results land in `docs/decisions.md` as a dated entry —
"2026-XX-YY — found-footy rebuild cutover retrospective" — so future
projects can learn from it.

### Extensibility hooks

**Adding a new consumer** (e.g., a mobile app, a Discord bot, an
external partner):

1. Register a webhook subscription via `POST /api/v1/webhooks/subscriptions`
   OR open an SSE stream — whichever fits their delivery semantics.
2. Get an API token (add to `FOUND_FOOTY_API_TOKEN` rotation).
3. Consume via the existing typed contract. No new server-side work.

**Multi-stage cutover for future high-risk migrations** — the pattern
above (feature-flag per endpoint, 48h stability window per step, four-
color metrics thresholds) is reusable. Document as a runbook if we do
another rebuild.

**Cutover automation** — the current design is manual (`bin/deploy`,
manual flag toggles in `.env`, human-monitored dashboards). A more
automated version would:

- Automated flag flipping on schedule with automatic rollback on threshold breach
- Prometheus AlertManager routing rollbacks to a runbook automation
- Blue-green deployment via a self-hosted registry (post-migration to Gitea/Forgejo)

Deferred; not day-one. Documented here for completeness.

---

## 15. Documentation and process

The rebuild adopts a **documentation-driven agentic development** style —
the docs are load-bearing, not aspirational. Every design decision, every
subsystem contract, every operational procedure lives in a specific file
at a specific path, and every code change that touches those contracts
updates the corresponding doc in the same commit.

### 15.0 What "documentation" means here

Documentation in this project is **three layered persistence mechanisms**,
each with a different scope, lifespan, and audience:

| Layer | Path | Scope | Audience | Lifespan |
|---|---|---|---|---|
| **Repo `docs/`** | `docs/*.md` | Cross-file architecture, design decisions, operational runbook | Any contributor (human or agent) at any point in the future | Permanent (append-only for decisions) |
| **Code docstrings** | Godoc comments on packages/types/functions | Behavior and invariants of a specific unit of code | Anyone touching that specific file | Same lifespan as the code itself |
| **Per-agent auto-memory** | `~/.claude/projects/<project>/memory/` (agent-specific paths) | User preferences and collaboration tone | The agent, on future sessions with the same user | Refreshed as the user's habits change |

The **repo `docs/`** is the canonical layer. If a fact belongs anywhere,
it belongs there — the other two layers are supplements. Everything a
new contributor needs to onboard should be reachable from `docs/README.md`
in ≤ 3 hops.

**Docstrings** carry behavior-of-this-specific-file knowledge that's
inseparable from the code. They answer "how does this function behave"
in a way that survives file renames but not architectural rewrites.

**Per-agent auto-memory** is deliberately narrow — user preferences
("this user wants terse responses", "this user is anal about naming")
and nothing else. Project facts do NOT go there because they drift
across agents and are invisible to humans reading the repo.

The old system's `docs/` grew organically to ~15 files across
`architecture.md` / `orchestration.md` / `temporal.md` / `logging.md` /
etc. This shape mostly worked — the rebuild keeps the general
structure and tightens the intake rules.

### 15.1 The docs/ hierarchy after rebuild

After cutover, `docs/` looks like this:

```
docs/
├── README.md                      # Routing index + intake rules + after-session checklist
│
├── architecture.md                # Domain model, module dependencies, workflow shape
├── orchestration.md               # Event lifecycle state machine, debouncing, VAR handling
├── temporal.md                    # Per-workflow / per-activity retry & timeout policies
├── observability.md               # Log/metric/trace catalog + Grafana query cookbook
├── api-contract.md                # HTTP + SSE + webhook contract (source of truth for FE)
├── logging.md                     # Structured JSON emission convention + query cookbook
├── operations.md                  # Runbook: bring-up, scaling, common issues, debugging
├── deployment.md                  # docker-compose reference + Caddy routes + host layout
├── testing.md                     # Three-tier pyramid, factory conventions, CI shape
│
├── decisions.md                   # APPEND-ONLY architectural decisions log (newest at top)
├── todo.md                        # Active work + open bugs (newest above older)
├── audit.md                       # (Historical) May 2026 code audit of the old system
├── design-audit.md                # (Historical) The audit that led to the rebuild
├── rebuild-plan.md                # (Historical) This document
├── roadmap.md                     # Post-rebuild strategic direction (if any)
├── sprints.md                     # (Historical) Sprint board for the pre-rebuild cleanup
│
└── proposals/                     # Design docs for pre-implementation feature work
    ├── README.md                  # Proposal template + status conventions
    └── <topic>.md                 # One proposal per topic
```

**Every file has a specific reader-writer contract:**

- **`README.md`** — the front door. Directs readers to the right file for
  any task. Contains the intake table ("if you learned X, put it in Y").
  Contains the after-session checklist. Never contains substantive
  content — only routing.

- **`architecture.md`** — how the system is shaped. Domain model,
  package layout, module dependency graph, workflow hierarchy.
  Updated when structure changes. NOT a place for chronological
  history (that's `decisions.md`).

- **`orchestration.md`** — how events flow. State machines for the
  fixture and event lifecycles. Debouncing rules. VAR / re-attribution
  handling. Updated when the state machine changes.

- **`temporal.md`** — how workflows are wired. Per-activity timeout /
  retry / heartbeat policies. Workflow ID naming conventions. Signal
  contracts. Updated when workflow shape changes.

- **`observability.md`** — the full log + metric + trace vocabulary.
  Auto-derived where possible (see §15.3); the hand-maintained
  content is dashboard descriptions and query cookbook entries.

- **`api-contract.md`** — the OpenAPI spec + SSE schema + webhook
  event contract. This is what vedanta-systems reads. Auto-derived
  from Huma struct tags (see §15.3) with hand-maintained prose for
  intent + versioning policy.

- **`logging.md`** — how to emit a log line, what the vocabulary is,
  how to query it in Loki. The vocabulary catalog is auto-derived;
  the query cookbook is hand-maintained.

- **`operations.md`** — runbook. Bring-up steps. Common failure modes
  and their diagnostic paths. Scaler tuning guide. Twitter cookie
  reauth flow. Updated when procedures change.

- **`deployment.md`** — deployment concerns. `docker-compose.yml`
  reference (what each service does, dependencies). Caddy route
  ownership. Cross-project network setup. Volume layout.

- **`testing.md`** — the three-tier pyramid (§12), test factory
  conventions, CI job shape, coverage floor policy.

- **`decisions.md`** — APPEND-ONLY architectural decisions log. Every
  entry is dated + rationale-carrying. Newest at top. When a decision
  is superseded, the new entry links back to the old ("see 2026-XX-YY
  decision on X"). NEVER edit an old entry — even if it's now wrong,
  add a new entry that says "this replaces the 2026-XX-YY entry."

- **`todo.md`** — active work + open bugs + deferred items. Newest
  above older. Paste-ready start-of-session block at the top for
  agents to bootstrap on. Items get moved OUT of `todo.md` (to
  `decisions.md` if resolved, deleted if abandoned) — this file
  does not grow forever.

- **`proposals/`** — design docs for in-flight feature work.
  Header note flags status (draft / accepted / rejected / superseded).
  When a proposal ships, the doc moves to a "shipped" section OR
  gets summarized into a decisions.md entry + deleted.

- **`audit.md`, `design-audit.md`, `rebuild-plan.md`, `sprints.md`** —
  historical artifacts (see §15.7). They stay in the repo because
  they show *why* the current architecture is what it is, but they
  don't get maintained. New readers should know they describe past
  state.

**What does NOT live in docs/:**

- Static counts ("33 activities", "5M donors") — these drift the moment
  code changes. Either derive at query time or omit.
- Project structure walks that duplicate what `ls` / `tree` shows.
- Line-by-line explanations of what code does. That's what docstrings
  are for.
- Copy-pasted code snippets from the repo. Reference the file with
  a markdown link; the snippet drifts.

### 15.2 Docstring policy — Go's conventions applied

The old Python codebase had a project-level docstring policy ("every
`.py` file gets a module-level docstring; every function, method, and
class gets a docstring"). The rebuild keeps the same *spirit* (docs are
load-bearing) with Go-idiomatic execution.

**Go's convention:** godoc extracts documentation from comments that
immediately precede exported declarations. `go doc <package>` renders
them. This makes docstring-driven documentation a first-class output.

**Coverage policy:**

- **Every package** gets a package comment (`Package foo does X.`) at the
  top of one file, conventionally `doc.go` for larger packages.
- **Every exported type** gets a doc comment explaining what it
  represents and any invariants callers must honor.
- **Every exported function or method** gets a doc comment explaining
  behavior. Include the parameter meaning + return semantics + error
  cases when they're non-obvious. Single-line for trivial helpers.
- **Every exported constant or variable** gets a doc comment if the
  name doesn't fully explain the value.
- **Unexported symbols** get comments only when the *why* is non-obvious.
  Don't paraphrase the function name; add what the signature can't.

**Style:**

- Doc comments start with the symbol name and read as a complete
  sentence: `// Emit publishes a semantic event...`
- Never wrap at arbitrary column widths; godoc rewraps for its output.
- Use `Args:` / `Returns:` / `Raises:` (or the Go idiom "returns X and Y")
  when parameter roles or return semantics are non-obvious.
- For hidden invariants, prior bugs, or replay-safety constraints,
  document them in the comment. This is where the *why* lives.

**Example:**

```go
// Package event publishes semantic events to the durable event_log
// (via Postgres) and to NATS (for realtime fan-out to SSE and webhook
// consumers).
//
// The dual-write invariant is load-bearing: every semantic event MUST
// be inserted into event_log before NATS publish. On NATS publish
// failure the event_log insert stays committed and the fan-out backfill
// path (§8) covers the drop. Callers should never publish to NATS
// without an accompanying event_log write.
package event

// Emit publishes a semantic event.
//
// Insert into event_log happens first; on success, the event is
// published to the NATS subject "event.<Kind>". NATS publish failure
// is logged but does not roll back the event_log insert (see package
// doc for the fan-out backfill contract).
//
// Returns ErrEventLogWriteFailed if the durable insert fails. NATS
// publish errors are surfaced via structured logs but not returned.
func Emit(ctx context.Context, ev SemanticEvent) error { ... }
```

**Anti-patterns:**

- `// GetTeamName returns the team name.` — noise. The signature
  already says this. Either add what the signature can't or delete.
- Multi-paragraph docstrings on trivial helpers. If the function is
  three lines and named clearly, one sentence is fine.
- Doc comments that reference the current PR / issue / task
  ("added for the Y flow", "handles issue #123"). That belongs in
  the commit message or decisions.md; it rots as the code moves.

**When you change a function, update the doc comment in the same
commit.** The after-session checklist in `docs/README.md` includes this
as a step, but linter-enforce where possible (see §15.6).

### 15.3 Auto-derived documentation (never hand-maintain)

The old Python system hand-maintained the log catalog in `docs/logging.md`
(1285 lines). Every new `log.info(..., "some_action", ...)` call
needed a matching entry there, and the entries drifted from reality
constantly.

**The rebuild's rule:** if documentation can be derived from code, it
IS derived from code. Hand-maintenance is reserved for prose that
explains *intent* — not tables that mirror code.

**What gets auto-derived:**

| Document | Derived from | Mechanism |
|---|---|---|
| `docs/logging.md` — action catalog | `internal/observability/vocabulary/actions.go` enum | `go generate ./...` runs a small tool that emits `docs/generated/actions.md`; `logging.md` links to it |
| `docs/observability.md` — metrics catalog | `internal/observability/metrics/metrics.go` (Prometheus registry) | Same pattern; `metrics.md` includes `docs/generated/metrics.md` |
| `docs/api-contract.md` — HTTP endpoints | Huma struct tags on request/response types | `huma reflect` emits OpenAPI JSON to `docs/generated/openapi.yaml`; `api-contract.md` documents intent, links to the spec |
| `docs/api-contract.md` — SSE events | `internal/api/sse/events.go` type definitions | Same generator emits `docs/generated/sse-events.md` |
| `docs/temporal.md` — workflow/activity list | `internal/worker/registration.go` registration calls | `go generate` walks the registration list; emits `docs/generated/workflows.md` |
| `docs/deployment.md` — env var reference | `internal/config/*.go` `envconfig` struct tags | Same generator emits `docs/generated/env-vars.md` |
| Migration list | `db/migrations/*.sql` files (goose) | Generator lists filenames + top-of-file summary comment |

**`docs/generated/`** is committed to the repo (not gitignored). The
generator runs as a git pre-commit hook AND in CI, and CI fails if the
generated files are out of sync with source. This is the "single source
of truth is the Go code" invariant, enforced.

**What CAN'T be auto-derived and stays hand-maintained:**

- The *why* of a decision (that's `decisions.md`).
- The *how to use this* prose that surrounds a table.
- Grafana query cookbook entries (LogQL / PromQL snippets).
- Runbook procedures.
- Test conventions and factory patterns.
- Cross-doc navigation ("if X, see Y").

Hand-maintained docs LINK TO the auto-derived docs; they never inline
copies of what the generator emits.

**Vocabulary drift is a first-class CI failure.** If a code change adds
a new `vocabulary.Action` constant but doesn't regenerate the catalog,
CI blocks the merge. Same for a new metric, a new API endpoint, or a
new env var. The generator is idempotent — running it twice on unchanged
input produces zero-diff output — so this is a safe check.

### 15.4 Cross-doc linking

The convention is **markdown links, no wiki-links.** This was decided
2026-06-30 (see `decisions.md`); rationale: `[[wiki-link]]` renders as
literal broken brackets on GitHub, and it's ambiguous when filenames
collide across projects.

**Syntax:**

- Within a project: `[descriptive text](./relative-path.md)` from the
  linking file. `decisions.md` linking architecture is
  `[architecture](./architecture.md)`; `proposals/foo.md` linking up is
  `[architecture](../architecture.md)`.
- Across projects: prefer routing through `vedanta-dhobley/docs/` as
  the cross-project knowledge hub. Direct project-to-project links
  are allowed but rare; they should mention the target project so a
  reader can navigate manually if the file has moved.
- Deep links: `[testing conventions](./testing.md#three-tier-pyramid)`
  when a section anchor exists.

**Link text:**

- Descriptive. "See [the dedup design doc](./proposals/dedup-unification.md)"
  beats "See [this doc](...)" or bare `./proposals/dedup-unification.md`.
- Read out of context: if a reader clicks the link and the surrounding
  sentence is invisible, the link text should still tell them where they're
  going.

**Density:**

- Link liberally — most cross-doc references should be one click away,
  not a re-grep.
- But don't double-link the same target in the same paragraph. First
  mention links; later mentions are plain text.

**Broken links:**

- CI runs a link checker (`lychee` or `markdown-link-check`) on every
  `docs/**/*.md` file. Broken links block the merge.
- When you move or rename a doc, grep for inbound references
  (`grep -rn "old-name.md" docs/`) and update them.
- The after-session checklist includes "did we move a doc? Update
  inbound references."

### 15.5 The `decisions.md` append-only log

**The load-bearing rule:** `decisions.md` is append-only. Old entries
never get edited in place, even when they're wrong.

**Why append-only:** the file is a chronological record of what was
decided *when* and *why*. If a 2025 decision turned out to be wrong and
we changed direction in 2026, both entries are informative — the 2025
one explains the *original* reasoning; the 2026 one explains why we
changed our minds. Editing the 2025 entry destroys the historical record
and makes the change invisible.

**Format:**

```markdown
## YYYY-MM-DD — Short imperative title of the decision

**Decision:** one paragraph stating what was decided.

**Why:** one paragraph explaining the reasoning. Cite constraints,
lived problems, alternatives considered.

**Consequences:** what changes in the code / process / operational
posture. What other decisions this enables or blocks.

**Supersedes (optional):** link back to the entry this replaces,
if any. `See 2025-XX-YY entry on X.`
```

**What qualifies for a decisions.md entry:**

- A choice between competing approaches (Postgres vs. Mongo, NATS vs.
  Postgres LISTEN/NOTIFY, worktree isolation vs. shared workspace).
- A load-bearing invariant nobody would guess from reading the code
  ("URL stability is non-negotiable", "per-event serialization is the
  correctness anchor").
- A commitment we've made to external systems (URL formats, API
  contracts, webhook payloads).
- A policy change (docstring coverage floor, CI failure conditions).

**What does NOT qualify:**

- Code-level design details (goes in docstrings or `architecture.md`).
- Task-level decisions ("we'll build feature X after feature Y") —
  goes in `todo.md` or `roadmap.md`.
- Ephemeral choices that will be re-evaluated soon.

**Cross-linking:**

- New entries can reference old ones: "See 2026-06-30 entry on X."
- Old entries can be linked from other docs: `docs/architecture.md`
  linking a rationale is `see [decisions.md 2026-07-01](./decisions.md#2026-07-01--fresh-rebuild-in-parallel-not-incremental-refactor)`.

**Superseding an old decision:**

```markdown
## 2027-XX-YY — Reverting to Postgres LISTEN/NOTIFY (supersedes 2026-07-01)

**Decision:** roll back the workspace NATS bus. Semantic events flow
through Postgres LISTEN/NOTIFY.

**Why:** ...

**Consequences:** ...

**Supersedes:** [2026-07-01 — Workspace NATS as event bus](#2026-07-01--workspace-nats-as-event-bus-replaces-postgres-listen-notify).
The 2026-07-01 entry stays in place.
```

The 2026-07-01 entry does NOT get edited to say "superseded" — the 2027
entry links back, and forward-in-time readers reach the 2027 entry
first (newest at top) so they see the current state.

### 15.6 Documentation in CI

CI runs a **documentation validation suite** on every PR. The suite is
fast (< 30s) and produces actionable output.

**Checks:**

1. **Link checker** — every markdown link resolves. Broken links block
   merge.
2. **Auto-derived freshness** — running `go generate ./...` produces
   zero diff against `docs/generated/`. If a PR adds a new
   `vocabulary.Action` without regenerating the catalog, this fails.
3. **Docstring coverage** — every exported symbol in `internal/domain/`
   has a doc comment. Enforced by `golint` (or a custom linter that
   understands exceptions like `String()`). Missing docstrings block
   merge.
4. **OpenAPI schema validation** — the generated `docs/generated/openapi.yaml`
   is a valid OpenAPI 3.1 document.
5. **Markdown lint** — `markdownlint` catches common issues (missing
   heading levels, trailing whitespace, inconsistent list markers).
   Non-blocking warnings; blocking errors for structural issues.
6. **Decisions.md format check** — every top-level `## YYYY-MM-DD` entry
   has valid ISO date + a rationale section. Enforced by a small script
   that parses the file.
7. **Docs referenced from code are still valid** — comments like
   `// See docs/architecture.md#foo` are checked; the target section
   must exist. Prevents dead references in comments.
8. **No wiki-links** — any `[[...]]` in a `.md` file fails the check.
   Legacy documentation from other tooling doesn't leak in.

**What CI does NOT check:**

- Whether docs are *correct* — that's the writer's judgment.
- Whether every code change updates a related doc. Too broad to be
  reliable; the after-session checklist covers this instead.
- Prose style, tone, or clarity.

**CI produces a doc coverage report** on every merge to `main`: how many
docstrings, how many linked docs, what's missing. The report is a
`.github/artifacts/` file, not a merge blocker; it feeds the "docs
health" section of the internal monitoring dashboard.

### 15.7 The rebuild plan and design audit as historical artifacts

The `docs/rebuild-plan.md` (this document) and `docs/design-audit.md`
are **historical artifacts**. They stay in the repo because they
explain *why* the current architecture is what it is, but they don't
get maintained after cutover.

**When rebuild-plan.md becomes historical:** at cutover completion.
The plan describes what we WILL build; once the build is done, the plan
is a snapshot of the design intent. Subsequent changes to the
architecture go in `decisions.md` (for the choice) and
`architecture.md` (for the resulting structure), NOT as edits to this
plan.

**Marker at the top:**

```markdown
> **Historical.** This document describes the design intent for the
> found-footy Go rebuild, drafted 2026-06 through 2026-07 and shipped
> 2026-QX. It is preserved as a snapshot of the architectural reasoning
> at that time. For current architecture, see [architecture.md](./architecture.md).
> For post-rebuild decisions, see [decisions.md](./decisions.md).
```

**What happens if a §-section turns out to be wrong:**

- Do NOT edit the section in place.
- Add a `decisions.md` entry that supersedes it.
- Add a note at the top of the section: "This section is historical;
  see [decisions.md YYYY-MM-DD entry](./decisions.md#...)."

**Same for `design-audit.md`:** it was a snapshot of the pre-rebuild
audit. Preserved for the reasoning about what was wrong; not updated
as the codebase evolves.

**`audit.md`** (the May 2026 audit of the pre-rebuild Python code) is
frozen the moment the Python code is deleted from the repo. Preserved as
context for why certain design choices were made in the rebuild.

**`sprints.md`** (the pre-rebuild sprint board) is frozen at the same
point. Deleted after the retrospective (§14) — no reason to keep it.

**`todo.md`** — active list — gets a "pre-rebuild historical" section
appended when the rebuild lands, capturing what remained open under
the old system. The Go rebuild starts with a fresh top.

### 15.8 Commit messages and PR descriptions

**Commit message format** (per user-global CLAUDE.md, project-specific
overrides):

```
<type>(<scope>): <short imperative summary under 70 chars>

<Optional body: paragraphs explaining the WHY, not the WHAT. Wrap at
72 columns.>

<Optional footer: refs to issues, PRs, or decisions.md entries.>
```

**Types:**

- `feat` — new user-visible feature or new capability
- `fix` — bug fix
- `perf` — performance improvement without behavior change
- `refactor` — internal restructuring, no behavior change
- `chore` — dependency updates, tooling changes, non-code hygiene
- `docs` — documentation-only changes
- `test` — test-only changes
- `build` — build system / CI / docker changes

**Scopes** for this project: `discovery`, `download`, `upload`, `vision`,
`api`, `sse`, `webhook`, `nats`, `pg`, `s3`, `temporal`, `worker`,
`scaler`, `twitter`, `deploy`, `docs`.

**The WHY, not the WHAT:**

- Bad: `fix(upload): change s3_key to _s3_key in MD5 dedup lookup`
- Good: `fix(upload): MD5 dedup was silently missing existing S3 videos`
  (body explains: the lookup key was `s3_key` but the field is stored
  as `_s3_key`, so the batch dedup pass never matched anything, causing
  duplicate uploads until the perceptual pass caught them.)

**No `Co-Authored-By:` trailer.** Per project + user-global CLAUDE.md,
agent attribution via git committer name is sufficient; the
`Co-Authored-By: <Agent Name>` trailer is not used.

**Never `--amend` a published commit.** Always create a new commit.
`--amend` on a pushed commit forces a force-push, which is destructive
to anyone who's fetched the previous SHA.

**Never `--no-verify` or skip hooks** unless explicitly requested.
Pre-commit hooks (link check, docstring coverage, generated-docs
freshness) exist for reasons. If a hook fails, investigate and fix the
underlying issue.

**PR descriptions** follow a fixed template:

```markdown
## Summary

<1-3 bullets: what changed, why>

## Context

<What led to this change — link to decisions.md, issue, or prior PR
if applicable. What was the alternative approach considered.>

## Testing

<How this was verified. Unit test names touched, integration test
scenarios covered, manual verification steps.>

## Docs impact

<Which docs/*.md files were updated. If none, why not.>

## Rollout notes

<Any deployment-specific concerns: env var additions, migration
requirements, rollback procedure.>
```

**When a PR embodies a decision worth preserving,** add a `decisions.md`
entry in the same PR. The PR description links to the entry; the entry
links back to the PR (via commit SHA). This creates the audit trail
without duplicating the reasoning.

### 15.9 Per-agent auto-memory boundary

Per-agent auto-memory paths (Claude Code:
`~/.claude/projects/<project>/memory/`; other agents have analogous
paths) are **reserved for user preferences and collaboration tone.**

**What lives there:**

- "This user prefers terse responses; skip end-of-turn summaries."
- "This user is anal about naming and Caddy exposure decisions —
  surface options rather than picking silently."
- "This user reads Loki for past-match forensics because scaled-down
  workers are gone from `docker logs`."
- "This user is precise about live-vs-designed distinctions."

**What does NOT live there:**

- Project facts (goes in `docs/`).
- Architecture / schema / port assignments (goes in `docs/`).
- Bug diagnoses or fix recipes (the fix is in the code + commit).
- Task state (goes in `todo.md` or the session task list).
- Anything that would be useful to another agent or human — that's
  a signal it belongs in `docs/`, not in per-agent memory.

**Why the boundary matters:** auto-memory is invisible to other agents
and to humans reading the repo. If a project fact lives there, it
becomes an unwritten rule that changes based on which agent is being
used, which is exactly the failure mode we're avoiding.

**The load-bearing invariant:** any project fact worth remembering
should be discoverable by reading `docs/README.md` and following its
routing. If a fact is only in auto-memory, it doesn't exist for
onboarding purposes.

The rebuild's project CLAUDE.md restates this rule at the bottom
("Auto-memory note — per `~/.claude/CLAUDE.md`, project facts belong
here or in `docs/`, not in per-agent memory. Keep that auto-memory
directory essentially empty.").

### 15.10 Handling the current docs/ during cutover

The current `docs/` describes the Python system. When the Go rebuild
lands, most of it needs to change. The transition happens in a specific
order — not "delete everything and rewrite" — so that during cutover
(§14), both systems can be documented.

**Cutover phases for docs/:**

**Phase A: parallel-truth (during rebuild).**

- Current `docs/*.md` files stay as-is; they describe the Python
  system that's still in prod.
- New Go-side documentation lives in `docs/rebuild/` — a sibling
  directory that gets `docs/rebuild/architecture.md`,
  `docs/rebuild/operations.md`, etc. Same file names, different
  parent.
- `docs/README.md` gets a header: "Two directories: `docs/*.md`
  describes the current Python system; `docs/rebuild/*.md` describes
  the incoming Go system. During cutover, both are current."

**Phase B: rebuild is prod (post-cutover).**

- `docs/rebuild/` becomes the canonical location.
- Old `docs/*.md` files get an "Archived — this describes the pre-2026-Q3
  Python system" header at the top.
- Old files are MOVED (`git mv`) to `docs/legacy/` — clearly separated.
- `docs/README.md` gets updated to route to the new docs.

**Phase C: legacy turnoff (per §14).**

- `docs/legacy/*.md` stays in the repo forever for historical context,
  but gets a bulk header: "The Python system these files describe was
  turned off YYYY-MM-DD. See legacy-turnoff runbook in §14."

**Alternative considered — hard cutover.** Delete old docs, write new
docs, one big commit. Rejected because during cutover (§14), operators
need to be able to read *both* runbooks. The parallel-truth phase costs
a few weeks of `docs/rebuild/` prefix noise but preserves operational
usability.

**Anti-pattern:** editing `docs/architecture.md` in place mid-rebuild
to describe half the old system and half the new. The doc becomes
useless for BOTH audiences.

### 15.11 The "docs stay honest" invariants

Documentation drift is the failure mode this whole system is trying to
prevent. The specific invariants that keep it honest:

1. **Auto-derive where possible.** If a table can come from code, it
   does. Hand-maintenance is for intent, not for facts.
2. **CI validates generated content freshness.** Adding a code fact
   without updating the derived doc fails the merge.
3. **The after-session checklist** in `docs/README.md` prompts writers
   to consider six categories (decisions / open work / discovered
   facts / operational changes / stale entries / docstrings) at
   session end. Not every session touches all six, but every session
   *considers* all six.
4. **Broken cross-doc links fail CI.** Renames don't silently orphan
   references.
5. **Historical docs are marked.** Readers know when they're reading
   a snapshot vs. current state.
6. **`decisions.md` is append-only.** History is preserved.
7. **Per-agent memory is bounded.** Project facts don't hide in
   agent-specific paths.
8. **Every code change PR states its docs impact.** Zero-doc-impact is
   a valid answer; unstated is not.

**When drift is discovered anyway** (a reader finds a stale claim in
`architecture.md` that no longer matches the code): fix in the same
commit as the code change that revealed it, if possible. If discovered
separately, open a `docs:` scoped fix commit. Log the incident briefly
in `decisions.md` if it reveals a process gap.

---

## 16. Implementation sequencing

Everything above describes *what* gets built. This section is about
*when* and *in what order*. The rebuild is a substantial piece of work
happening in parallel with a live prod system, so ordering matters:
we need working code at every merge point, no long-lived branches, and
the ability to stop between milestones without leaving the repo in a
half-built state.

### 16.0 Framing

**The unit of planning is a milestone, not a sprint.**

- A milestone is a coherent deliverable that could ship independently.
- Each milestone lands as a series of small PRs to `main` (or a
  long-lived rebuild branch — see below).
- Milestones have explicit prerequisites (what must ship first) and
  explicit exit criteria (how we know it's done).
- Between milestones is a checkpoint: "are we still on track,
  should we adjust scope, is the next milestone still the right one?"

**Calendar vs milestone.** This plan lists milestones in dependency
order and gives rough size estimates ("large / medium / small"). It
does NOT commit to specific calendar dates because:

- The user is doing this alongside a day job; effective velocity varies.
- Discovery during implementation surfaces work that wasn't in the
  plan; better to adjust milestones than to slip a calendar.
- The World Cup timeline (~mid-June 2026, per the 2026-Q2 roadmap) is
  the hard constraint on when the CUTOVER can happen — the
  intermediate milestones can slide.

**Sizes:**

- **Small** — a focused session or two. One-to-a-few PRs.
- **Medium** — several sessions across a week or two. Multiple PRs
  landing as a coherent slice.
- **Large** — extended work with intermediate checkpoints. Would
  span multiple weeks of focused work; likely gets broken into
  sub-milestones during execution.

**Branch strategy.**

The rebuild happens on a long-lived `rebuild/go` branch, NOT directly on
`main`. Rationale:

- `main` continues to host the running Python system. Bugs found in
  prod during rebuild get fixed on `main` and cherry-picked to
  `rebuild/go` if the fix applies conceptually.
- `rebuild/go` accumulates all the new code without disrupting prod
  releases.
- Individual PRs merge from `feature/rebuild-<milestone>` branches
  → `rebuild/go`, keeping the rebuild PR history clean.
- At cutover, `rebuild/go` merges to `main` in one large but
  well-reviewed PR (per §14 cutover plan).

**Alternative considered — trunk-based on `main` with feature flags
everywhere.** Rejected because the Python → Go boundary is too broad;
gating every new file behind a flag adds noise without value. The
long-lived branch cost is real (rebases from `main` are needed) but
manageable.

### 16.1 Phases overview

Reading left-to-right, milestones roughly stack; reading top-to-bottom,
they're the parallel tracks within a phase.

```
Phase Foundation          → Phase Substrate         → Phase Domain          → Phase Orchestration
   ↓ enables                  ↓ enables                 ↓ enables               ↓ enables
Phase API                 ← Phase Video pipeline    ← Phase Testing        ← Phase Cutover
   ↓ enables
Phase Cutover
```

Nine phases total. Foundation and Substrate are strictly sequential.
Domain and Testing overlap heavily. Orchestration depends on Domain.
API and Video Pipeline can proceed in parallel once Substrate is done.
Cutover comes last.

| Phase | Size | Prerequisite | Delivers |
|---|---|---|---|
| **F. Foundation** | Small | None | Repo scaffolding, CI, docs skeleton |
| **S. Substrate** | Medium | F | 10 infra adapters, docker-compose stack |
| **D. Domain** | Large | S (partial) | 8 domain packages with stores + services |
| **O. Orchestration** | Large | D | 5 workflows + ~30 activities in Temporal |
| **V. Video pipeline** | Medium | S (partial) | Download + AI validation + hashing |
| **A. API surface** | Medium | S + D | Chi+Huma HTTP + SSE + webhook |
| **T. Testing hardening** | Medium | Throughout | Unit + integration + synthetic e2e |
| **M. Migration** | Small | D | Legacy → new data migration scripts |
| **C. Cutover** | Medium | Everything above | Feature-flag flip, frontend migration, legacy turnoff |

### 16.2 Phase F — Foundation

**Prerequisites:** none.

**What ships:**

- `rebuild/go` branch cut from current `main`.
- Repository layout per §2: `cmd/`, `internal/`, `deploy/`,
  `docs/rebuild/`, etc.
- `go.mod` with pinned toolchain (Go 1.23+), initial dependencies.
- Development tooling: `.golangci.yml`, `.editorconfig`,
  `Makefile` with `make test`, `make build`, `make lint`, `make docs`.
- CI skeleton: GitHub Actions workflows for build, lint, test,
  docs-freshness (per §15.6). All passing on empty code.
- Docs infrastructure: `docs/rebuild/README.md`, empty stub files for
  each subsystem doc, `docs/generated/` placeholder committed with a
  README explaining it's auto-generated.
- Pre-commit hooks: link check, `gofmt`, `go generate` freshness.
- `deploy/docker-compose.dev.yml` skeleton: services stubbed but not
  functional. Container naming per `~/workspace/proxy/CONVENTIONS.md`
  (`found-footy-dev-*`).
- `deploy/caddy.d/found-footy.caddy` skeleton: hostname reservations
  for the new services (no live routing yet).

**Exit criteria:**

- CI is green on an empty commit.
- A junior dev could clone the repo, run `make build`, and see
  every target succeed on empty code.
- `docs/rebuild/README.md` renders and its links resolve.

**Explicit non-goals:**

- No domain logic. No infra adapters. No workflows.
- No functional docker-compose services (containers won't start
  meaningfully because there's no code yet).

**Estimated size:** 1-2 sessions.

### 16.3 Phase S — Substrate

**Prerequisites:** F complete.

**What ships:** the 10 infra adapters from §9 plus the runnable
docker-compose dev stack. Each adapter ships with:

- Interface definition (`internal/infra/<name>/client.go`).
- Real implementation with config-driven construction.
- In-memory fake for unit testing (`internal/testutil/fakes/`).
- Integration tests via testcontainers-go where a real backend is
  required (`pg`, `nats`, `s3`).
- Docstring coverage for every exported symbol.
- Log emission via the vocabulary package (which lands here too).

**Sub-milestone order:**

1. **S1: Observability + config plumbing** (Small).
   - `internal/observability/vocabulary/` — the enum package with
     `Module` and `Action` types.
   - `internal/observability/logging/` — the `Emit(...)` function that
     produces structured JSON.
   - `internal/observability/metrics/` — Prometheus registry + naming
     conventions.
   - `internal/observability/tracing/` — OpenTelemetry SDK config.
   - `internal/config/` — envconfig-based struct parsing for all
     adapters.
   - Reason for going first: every subsequent adapter depends on
     these packages. Deferring them means retrofitting log calls into
     already-written adapters.

2. **S2: `pg` adapter** (Medium).
   - Connection pool, transaction helpers, structured error mapping.
   - `db/migrations/` skeleton with the initial schema from §3.
   - `goose` (or `migrate`) integration in `cmd/migrate`.
   - Testcontainers integration test.

3. **S3: `nats` adapter** (Medium).
   - Client from §9 spec.
   - Docker-compose entry pointing at workspace NATS (once
     `~/workspace/nats/` is stood up — this milestone unblocks that).
   - Testcontainers integration test.

4. **S4: `s3` adapter** (Small).
   - Garage client.
   - Bucket provisioning + presigned URL generation.
   - Testcontainers with Garage or `minio` for the fake (they speak
     the same protocol).

5. **S5: `temporal` adapter + worker skeleton** (Small).
   - Temporal Go SDK client construction.
   - `cmd/worker/main.go` with empty workflow/activity registration.
   - Runs against workspace Temporal.

6. **S6: `llm` adapter** (Medium).
   - OpenAI-compatible chat completions client.
   - Endpoint discovery via `/v1/models`.
   - Concurrency semaphore (see §6 discovery for details on why 2).
   - Testcontainer or mock server for tests.

7. **S7: External API adapters** (Medium, mostly parallel-safe).
   - `apifootball` — REST client + rate limiter.
   - `wikidata` — SPARQL client.
   - `syndication` — Twitter syndication API for video downloads.
   - `twitter` — HTTP client to the (still-Python) Twitter service.
   - `ffmpeg` — subprocess wrapper + probe.
   - Each is small individually; grouped because they're independent
     and someone can grind through them.

**Exit criteria:**

- `docker compose -f deploy/docker-compose.dev.yml up -d` brings up the
  full stack: Postgres, NATS, Garage, Temporal, empty worker.
- Every adapter has a passing integration test in CI.
- Every adapter emits its expected log events (verified by a grep in
  a test).
- Vocabulary catalog is populated with the actions the substrate
  emits; `docs/generated/actions.md` is committed.
- The dev stack is reachable via Caddy routes.

**Explicit non-goals:**

- No domain services. No stores yet. The `pg` adapter has migrations
  but no query methods for domain tables (those come in Phase D).
- No workflows. `cmd/worker/main.go` compiles and connects to Temporal
  but registers zero workflows.
- The API service (`cmd/api`) is not even a stub yet.

**Estimated size:** 2-3 weeks of focused work.

**Checkpoint before continuing:** every adapter has a passing test AND
emits observable output. If we can't tell whether the substrate is
working by looking at the dev-stack Grafana, we're not done yet.

### 16.4 Phase D — Domain

**Prerequisites:** S1 (observability), S2 (`pg` with migrations), S3
(`nats`). Other adapters can lag.

**What ships:** the 8 domain packages from §4, each with:

- Types (`internal/domain/<name>/model.go`).
- Store interface + Postgres implementation.
- Service interface + orchestration-facing implementation.
- Fake store for unit testing.
- Unit tests for the service logic (mocking the store).
- Integration tests for the store against Postgres.

**Sub-milestone order:**

1. **D1: `fixture` domain** (Medium).
   - Lifecycle: staging / active / completed with league_id, kickoff_at,
     status.
   - Store: fetch by ID, list-by-status, transition-lifecycle methods.
   - Service: activate, complete, cleanup-old.
   - Unit + integration tests.

2. **D2: `event` domain** (Medium).
   - Event model with the `natural_key` uniqueness invariant.
   - Store: insert, fetch, update workflow-tracking fields.
   - Service: register-workflow, mark-monitor-complete, mark-download-complete.
   - Atomic ops via `INSERT ... ON CONFLICT DO NOTHING`, `UPDATE ...
     WHERE ... RETURNING`.
   - Unit + integration tests.

3. **D3: `video` domain** (Large — this is the hairy one).
   - Two-table split: `video_assets` (canonical byte-store, content-hash
     PK) + `video_shares` (public share IDs, s_<12hex> format).
   - Store: insert-asset-if-not-exists, insert-share, list-shares-by-event,
     recalculate-ranks.
   - The rank invariant: `CREATE UNIQUE INDEX video_shares_event_rank_active`
     enforces uniqueness; `recalculate` runs inside `WithRetryableTx`.
   - Service: register-video (asset + share creation with dedup), replace,
     bump-popularity, recalculate-ranks-for-event.
   - This is where the URL stability contract gets encoded — worth a
     dedicated code review checkpoint.
   - Unit + integration tests with generated fixtures that stress the
     rank invariant.

4. **D4: `alias` domain** (Small).
   - Team alias cache with Wikidata QID + normalized alias list.
   - Store: upsert-alias, fetch-by-team-id.
   - Service: get-or-fetch (with RAG fallback via `llm` + `wikidata`
     adapters).
   - Unit + integration tests.

5. **D5: Discovery / Vision / Session / TextAnalysis domains** (Medium).
   - The 4 supporting domains from §4.
   - Discovery holds discovered Twitter URLs pre-download.
   - Vision holds AI validation results per video (extracted minute,
     confidence, etc.).
   - Session holds Twitter cookie state (moved from filesystem to
     Postgres for durability).
   - TextAnalysis holds RAG intermediates (Wikidata alias lists, LLM
     filtered outputs) for auditability.

**Exit criteria:**

- Every domain has its store + service + tests.
- Integration tests exercise the atomic-op invariants (e.g., a test
  that spawns two goroutines both trying to register the same event
  and asserts exactly one succeeds).
- The rank uniqueness invariant is exercised under contention.
- All auto-generated docs (SQL migration list, log actions) are
  refreshed and committed.

**Explicit non-goals:**

- No workflows. Services expose methods that workflows will call, but
  the workflow layer doesn't exist yet.
- No API. The domain services are internally callable but not
  externally accessible.

**Estimated size:** 3-4 weeks. The `video` domain alone can eat a week
because the invariants around URL stability + rank uniqueness are
where most historical bugs came from.

**Checkpoint:** before moving to Phase O, all domain integration tests
pass AND the video domain's rank invariant is proven under a
stress-test scenario (100 concurrent goroutines racing on
recalculate-ranks; every final state has exactly one video per rank).

### 16.5 Phase O — Orchestration

**Prerequisites:** D complete. S5 (`temporal` adapter) complete.

**What ships:** the 5 workflows and ~30 activities from §5, registered
in `cmd/worker`, running against workspace Temporal.

**Sub-milestone order:**

1. **O1: Ingest workflow** (Small).
   - Daily fixture fetch, categorization, cleanup.
   - Depends on: `fixture` domain, `apifootball` adapter,
     `alias` domain (for pre-caching).
   - Activities: `fetch_todays_fixtures`, `fetch_fixtures_by_ids`,
     `categorize_and_store_fixtures`, `cleanup_old_fixtures`,
     `precache_team_aliases`.
   - Runs standalone (no fan-out) so it's a good smoke test that the
     worker + Temporal + Postgres integration works end-to-end.

2. **O2: Monitor workflow** (Medium).
   - The 30s polling loop.
   - Depends on: `fixture` + `event` domains, `apifootball` adapter,
     `nats` adapter for stable-event publishes.
   - Activities: `fetch_active_fixtures`, `process_fixture_events`,
     `complete_fixture_if_ready`, `notify_frontend_refresh` (now via
     NATS instead of HTTP).
   - Emits stable-event notifications that trigger Discovery.
   - Ships without Discovery hooked up first — Monitor sends events
     to a NATS subject that has no consumer yet, verifiable by NATS CLI.

3. **O3: Discovery (Twitter) workflow** (Medium).
   - Fires per stable event, drives the 10-attempt search loop.
   - Depends on: `event` + `alias` + `discovery` domains, `twitter`
     adapter.
   - Activities: `check_event_exists`, `resolve_team_aliases`,
     `execute_twitter_search`, `save_discovered_videos`, and the
     signal-with-start plumbing for Download.
   - Wired to Monitor's NATS subject as a subscriber that starts a new
     Discovery workflow per stable event.

4. **O4: Download workflow** (Medium).
   - Per-video download + AI validation + hashing.
   - Depends on: `syndication`, `ffmpeg`, `s3`, `llm` adapters;
     `vision` domain.
   - Activities: `register_download_workflow`, `download_single_video`,
     `validate_video_is_soccer`, `generate_video_hash` (or embedding —
     depends on §5 embedding decision), `queue_videos_for_upload`.
   - This is the video pipeline (Phase V) landing inside a workflow.

5. **O5: Upload workflow** (Medium).
   - Serialized per event via `SignalWithStartWorkflow` with
     deterministic ID `upload-{event_id}`.
   - Depends on: `video` domain, `s3` adapter.
   - Activities: `fetch_event_data`, `deduplicate_by_md5`,
     `deduplicate_videos`, `upload_single_video`, `replace_s3_video`,
     `update_video_in_place`, `bump_video_popularity`,
     `recalculate_video_ranks`, `check_and_mark_download_complete`,
     cleanup activities.
   - The URL-stability + rank-invariant checkpoint from Phase D pays
     off here — Upload workflow gets to be short and clear because the
     domain layer already enforces the hard rules.

**Exit criteria:**

- All 5 workflows registered and running in the dev worker.
- A dev-mode "seed a fake event" script creates a Postgres event and
  triggers the workflow chain end-to-end, producing an S3 video +
  event NATS message.
- Every workflow has retry policy documented in `docs/rebuild/temporal.md`,
  auto-generated from source.
- Integration tests exercise the workflow chain against the dev stack.

**Explicit non-goals:**

- No public API. The workflows write to Postgres; nothing reads it
  externally yet.
- No production traffic. Dev worker only.
- No frontend integration. That's Phase A + C.

**Estimated size:** 2-3 weeks. Each workflow is ~ 3 days of focused work
including tests.

### 16.6 Phase V — Video pipeline

**Prerequisites:** S1, S3 (nats), S4 (s3), S6 (llm), S7 (syndication +
ffmpeg). D3 (video domain).

**Runs in parallel with Phase O.** The video pipeline landed as
activities within Download and Upload workflows in Phase O, but the
lower-level video-processing code (frame extraction, hash / embedding
generation, MD5 dedup, validation prompt engineering) can be built and
tested in isolation.

**What ships:**

- `internal/video/` — pure video-processing code: frame extraction via
  ffmpeg, hash or embedding generation, MD5 batch dedup, aspect/duration
  filters.
- `internal/vision/` — the AI validation logic that wraps `llm` adapter
  calls for SOCCER/SCREEN classification and clock extraction. Prompt
  templates live here as versioned Go strings.
- Unit tests with golden-file inputs (frames from known videos, expected
  hash/embedding outputs).
- Integration tests that run a full validate + hash pass against the
  dev `llm` adapter.

**Explicit non-goals:**

- Not a service. `internal/video/` and `internal/vision/` are libraries
  called by workflow activities.

**Estimated size:** 1-2 weeks. Overlaps with O4 heavily.

**Decision gate — embeddings vs hashing.** Per the current roadmap's
Phase 5 decision gate: after O5 ships and the hash-based pipeline is
proven working, decide whether to swap perceptual hashing for
sentence-transformers embeddings. Two paths:

- **Path A**: ship the rebuild with the hash-based pipeline. Land
  embeddings as a follow-up after cutover.
- **Path B**: block cutover on embeddings landing. Risk: more work
  before World Cup deadline.

Recommendation: Path A. Cutover is high-risk enough; changing the video
processing algorithm at the same time compounds the risk. Embeddings
land as a post-cutover roadmap item.

### 16.7 Phase A — API surface

**Prerequisites:** S1, S2, S3 (nats). D1 (fixture), D2 (event), D3
(video).

**Runs in parallel with Phase O.** The API only needs read paths at
first; write paths come from workflows independently.

**What ships:**

- `cmd/api/main.go` — Chi + Huma server.
- `internal/api/handlers/` — request handlers per resource (fixtures,
  events, videos).
- `internal/api/sse/` — SSE stream subscribing to NATS `event.>` +
  `fixture.>` subjects, forwarding to browser clients. Backfill on
  reconnect via `event_log`.
- `internal/api/webhooks/` — subscription CRUD + JetStream durable
  consumer for delivery.
- `docs/generated/openapi.yaml` — auto-generated from Huma tags.
- `docs/rebuild/api-contract.md` — hand-maintained intent + versioning
  policy, linking to the generated spec.

**Sub-milestone order:**

1. **A1: Read endpoints** (Medium).
   - `GET /api/v1/fixtures`, `GET /api/v1/fixtures/{id}`,
     `GET /api/v1/events/{id}`, `GET /api/v1/videos/shares/{share_id}`.
   - Auth via `FOUND_FOOTY_API_TOKEN`.
   - OpenAPI generation working.

2. **A2: SSE stream** (Small).
   - `GET /api/v1/events/stream` (SSE endpoint).
   - Subscribes to NATS event subjects.
   - Backfill on `Last-Event-ID` header via `event_log` query.

3. **A3: Webhook infrastructure** (Medium).
   - Subscription CRUD.
   - JetStream durable consumer with delivery worker in `cmd/webhookd`.
   - HMAC signing of payloads.
   - Delivery retry with exponential backoff.

**Exit criteria:**

- Frontend can hit `/api/v1/fixtures` from a dev vedanta-systems and
  see fixtures from the rebuild's Postgres.
- SSE stream delivers events in realtime when a workflow publishes.
- Webhook consumer receives events even across worker restarts.

**Estimated size:** 1-2 weeks. Overlaps with O.

### 16.8 Phase T — Testing hardening

**Prerequisites:** runs throughout. Backfill milestone at the end.

**What ships:** the three-tier pyramid from §12.

- **Unit tests** — land with every domain / adapter / workflow.
  This is expected, not a separate milestone.
- **Integration tests** — land alongside code that needs them. Also
  expected.
- **Synthetic e2e tests** — a separate deliverable that lands late.

**Synthetic e2e milestone:**

- `test/synthetic/` — recorded scenarios that drive the full pipeline:
  a real API-Football response for a live match, a set of Twitter
  search results (recorded), video files, expected end-state Postgres
  rows + S3 objects.
- Runs in CI on every PR to `rebuild/go`.
- The scenarios themselves are versioned artifacts stored in
  `test/fixtures/scenarios/`.

**Exit criteria:**

- Coverage floor per §12 met (unit 60%+, integration 40%+ on
  boundary-critical code).
- At least 3 synthetic e2e scenarios cover: happy-path goal capture,
  VAR-cancelled goal, re-attribution recovery.
- CI runs the full test pyramid on every PR in < 10 minutes.

**Estimated size for the backfill:** 1-2 weeks. Ongoing testing during
Phases D + O + V + A is not counted here — that's just "how we work."

### 16.9 Phase M — Migration

**Prerequisites:** D complete.

**What ships:**

- `cmd/migrate-legacy/main.go` — one-shot script that reads
  `fixtures_active` + `fixtures_completed` from the legacy MongoDB,
  translates the embedded event / video arrays to the Postgres
  schema, and writes rows.
- Idempotent: safe to run multiple times.
- Chunked: processes 1000 fixtures at a time with progress logging.
- Verification mode: compare each written fixture back against the
  MongoDB source and log discrepancies.

**Sub-milestones:**

1. **M1: schema mapping** — a written mapping between MongoDB documents
   and Postgres rows, including the embedded-array-to-normalized-table
   translations for events + videos. Lives in `docs/rebuild/migration.md`.
2. **M2: migration script** — the Go implementation of the mapping.
3. **M3: dry-run** — run against a snapshot of prod Mongo in a dev
   Postgres. Compare row counts, check invariant preservation,
   spot-check individual records.

**Exit criteria:**

- Dry-run migrates the full prod snapshot in < 30 minutes.
- All post-migration invariant tests pass (rank uniqueness,
  URL stability, event `natural_key` uniqueness, no orphaned
  video_shares).
- Verification mode reports zero discrepancies on a small subset of
  fixtures manually re-checked.

**Estimated size:** 1 week.

**Explicit non-goals:**

- Not a live-migration script. This is a one-shot dump-and-load with
  a downtime window (per §14 cutover plan).

### 16.10 Phase C — Cutover

**Prerequisites:** everything above.

Per §14: endpoint-by-endpoint frontend migration with feature flags,
48-hour stability window per step, four-color metrics thresholds,
legacy turnoff after 30 days of parallel run.

**Milestone order internal to Phase C:**

1. **C1: Deploy new stack to prod alongside legacy** (Medium).
   - `docker compose -f docker-compose.yml up -d` runs the new stack
     with production env vars.
   - Legacy Python worker + twitter service continue running.
   - The new API is reachable at
     `found-footy-prod-api.<base-domain>` via Caddy; the old system's
     UIs stay at their existing hostnames.
   - vedanta-systems' `FOUND_FOOTY_API_URL` is set but no feature flags
     are enabled.

2. **C2: Data migration** (Small).
   - Run the migration script from Phase M against prod Mongo → prod
     Postgres.
   - Verify invariants.
   - Set the new stack's workflows to catch up from the migration
     cutoff point.

3. **C3: Frontend flag flips** (Medium, sequential).
   - Per §14: one endpoint at a time, 48h stability window each.
     Order: fixtures → events → videos → SSE → webhooks.
   - After each flip: dashboard watches for the four-color thresholds.
     Rollback if any hit red.

4. **C4: Parallel-run stabilization** (Medium).
   - Both stacks running, all traffic on new stack, legacy stack
     continues consuming API-Football and writing to MongoDB for
     rollback safety.
   - 30 days of soak.
   - Any bug found in new stack: fix on `main` (which is now the
     rebuild) + observe.

5. **C5: Legacy turnoff** (Small).
   - Per §14: stop legacy containers, `docker volume rm` of legacy
     volumes, delete the compat client from the new API.
   - `docs/architecture.md` updated to describe the new-only state.
   - Old `docs/` files moved to `docs/legacy/` per §15.10.

**Estimated size:** 4-8 weeks including the 30-day parallel-run soak.

### 16.11 Cross-phase concerns

Some work spans multiple phases and doesn't belong in any single
milestone.

**Observability catches up as we build:**

- Every phase adds vocabulary entries; the auto-generated catalog
  grows continuously.
- Every phase adds Prometheus metrics; the metrics registry grows.
- Grafana dashboards get built lazily as needed for debugging during
  Phase O + V.
- By the end of Phase O, we should have all the dashboards from the
  current Python system rebuilt for the Go stack — same queries,
  different vocabulary.

**`nexus` LLM endpoint swap:**

- Per decisions.md 2026-07-01: the LLM adapter is config-swappable.
- The rebuild ships pointing at `llama-small.joi` (current prod LLM
  endpoint).
- When nexus is ready, it's a `LLM_ENDPOINT_URL` change in `.env`
  and a container restart — no code change.
- No milestone owns this; it's a config change.

**Workspace NATS provisioning:**

- Milestone S3 depends on workspace NATS running at `~/workspace/nats/`.
- Standing up the workspace stack (docker-compose, Caddy routes, monitor
  scrape config) is done outside this project's repo but referenced
  in `deploy/INFRA-NOTES.md`.
- If workspace NATS isn't ready when S3 starts, S3 blocks; unblock by
  finishing workspace-side setup first.

**Docs generation runs from every merge:**

- Every PR triggers `go generate ./...`.
- CI fails if the diff is non-empty.
- This means every code change updates the derived catalogs
  automatically; no dedicated "docs maintenance" task.

### 16.12 Dependencies and merge points

**Strict prerequisites (must ship before dependents can start):**

```
F  →  S1
S1 →  S2, S3, S4, S5, S6, S7 (parallel)
S2 →  D1, D2, D3, D4, D5 (parallel among themselves once S2 done)
S3 →  O2 (Monitor needs NATS for stable-event fan-out)
S5 →  O1 (Ingest needs Temporal client)
S6 →  D4 (alias domain uses llm for RAG)
S7 →  D5 (discovery/vision/session/textanalysis use external APIs)
D1 →  O1 (Ingest activities read/write fixtures)
D1 + D2 →  O2 (Monitor)
D2 + D4 →  O3 (Discovery)
D5 + S6 + S7 →  O4 (Download)
D3 →  O5 (Upload) + A1 (video read endpoints)
D3 →  M (migration writes to video domain)
S1 + S2 + S3 →  A (API needs pg + nats + observability)
D + O + V + A →  T (synthetic e2e needs everything)
Everything →  M →  C (cutover needs migration, cutover needs everything)
```

**Parallelism opportunities:**

- Once S1 lands, adapters S2-S7 can be built in any order (or in parallel
  by different focus sessions).
- Once S2 lands, domains D1-D5 can be built in parallel.
- V (video pipeline lib) and O4 (download workflow) overlap by ~ 80%.
- V and A can proceed independently once their prerequisites clear.
- Testing (T) runs throughout; the "backfill" milestone at the end
  fills gaps.

**Merge points requiring coordination:**

- End of Phase S: dev stack must be fully functional before D starts.
  A subtle S2 bug blocks all D-phase integration tests.
- End of Phase D: video domain must have the URL-stability + rank
  invariants proven before O5 can be built without pain.
- End of Phase O: workflow-chain e2e must work before A2 (SSE) can be
  end-to-end tested.
- Beginning of Phase C: everything else complete. Cutover is the
  no-turning-back merge point.

### 16.13 Contingency and slip handling

**What can slip:**

- Phase V (video pipeline improvements) — the hash-based pipeline
  works today; embeddings are optional per §16.6 decision gate.
- Phase A3 (webhooks) — the vedanta-systems frontend currently uses
  SSE, not webhooks. External webhook consumers can be added
  post-cutover.
- Phase T backfill milestone — coverage floor is a target, not a
  release gate. If unit coverage hits 50% instead of 60%, ship anyway
  and fill the gap post-cutover.

**What CANNOT slip:**

- Phase F, S, D, O core — no cutover without a working workflow chain.
- Phase A1 + A2 (read + SSE) — frontend needs these to work.
- Phase M (migration) — no cutover without data.
- Phase C (cutover) itself, obviously — that's the deliverable.

**Slip decision framework:**

- If we're > 2 weeks behind the mental schedule for the current
  milestone: reassess scope. Can we ship a smaller version of this
  milestone that unblocks the next one?
- If a milestone reveals unforeseen work (e.g., an adapter integration
  is harder than expected): update the plan explicitly; don't just
  extend silently.
- If the cutover deadline (World Cup) is at risk: cut Phase V improvements,
  cut A3 (webhooks), ship the minimum viable rebuild.

**Rollback within the rebuild:**

- Every milestone leaves the rebuild branch in a "compiles + tests
  pass + docs current" state.
- If a milestone lands and then a fundamental design flaw is found:
  revert the milestone's PRs, re-plan, re-land. The long-lived branch
  strategy makes this cheap.

**Rollback of the cutover itself:**

- Per §14: feature flags are the primary rollback mechanism during
  endpoint-by-endpoint migration.
- If catastrophic issues emerge during the 30-day parallel run: flip
  the frontend flag back to legacy for that endpoint. Legacy stack is
  still running; recovery is one config change.
- After legacy turnoff (C5): no more rollback. That's why C5 waits
  for the 30-day soak.

### 16.14 Post-cutover roadmap

Not part of this plan, but worth naming so we know what's deferred:

- **Embedding-based video dedup** — the Path A deferral from §16.6.
- **Multi-region proxy for geo-restricted broadcasters** — deferred
  from the Python-side roadmap; still deferred here.
- **Self-hosted git migration** — moving this repo from github.com to
  a Gitea/Forgejo instance per user-global preferences.
- **Additional consumers via webhooks** — mobile app, Discord bot,
  external partners once the webhook infrastructure is proven.
- **Cutover automation** — the current cutover is manual; automated
  flag flipping + threshold-triggered rollback is a follow-up.

These live in `docs/rebuild/roadmap.md` (fresh file, not the current
`docs/roadmap.md` which is Python-era).

### 16.15 Retrospective and this document's future

Per §15.7, this document becomes historical at cutover. Its future:

- **At cutover completion (C5)**: add the "Historical" marker at the top.
- **Post-cutover retrospective (Day +45 per §14)**: capture what
  actually happened vs what this plan predicted. Land in
  `decisions.md` as a dated retrospective entry.
- **Ongoing**: this file stays in `docs/` as the source of truth for
  why the architecture is what it is. Future readers use it to
  understand the reasoning; current architecture always lives in
  `architecture.md` + `decisions.md`.

The success criterion for this plan isn't "everything went to
schedule" — it's "when we're done, we have a system that (a) works,
(b) is comprehensible from the docs, and (c) future contributors can
extend without carrying this document in their heads."

---

*End of rebuild-plan.md v1.0, drafted 2026-06-30 through 2026-07-01.*
