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

*(Remaining sections §2..§16 to follow in subsequent commits. Scaffold
+ Go-informed §1 established.)*
