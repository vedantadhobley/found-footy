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
| Storage layer | Keep Mongo; add discipline via Pydantic + JSON Schema validators + UUID | **Postgres from day one; Mongo not used** |
| Blob storage | MinIO stays (audit didn't call it out) | **Garage; MinIO not used** (community image licensing concerns) |
| Data-layer code | Refactor `mongo_store.py` incrementally into per-domain mixins | Fresh `src/domains/` with SQLModel or SQLAlchemy 2.0+ async |
| Orchestration | Temporal kept; workflow IDs unified, activities shrink incrementally | **Temporal kept**; workflows rewritten with the new domain services from day one |
| Frontend boundary | FastAPI in front of existing Mongo, incrementally | **FastAPI from day one**; vedanta-systems cuts over endpoint-by-endpoint |
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

Per-project docker-compose owns its data plane. found-footy's stack:

| Container                            | Role                                             | Replaces          |
| ------------------------------------ | ------------------------------------------------ | ----------------- |
| `postgres`                           | Structured data (fixtures, events, videos, aliases, telemetry) | Mongo |
| `garage`                             | S3-compatible blob storage for video files       | MinIO             |
| `temporal` + `temporal-postgres`     | Workflow orchestration + its metadata store      | *(kept as-is)*    |
| `temporal-ui`                        | Workflow observability                           | *(kept as-is)*    |
| `worker`                             | Temporal activity workers (scaled 2–8 by scaler) | *(kept as-is)*    |
| `twitter`                            | Firefox + Selenium fleet (scaled 2–8 by scaler)  | *(kept as-is; audit §8 methodology)* |
| `scaler`                             | Auto-scale controller                            | *(kept as-is)*    |
| `api`                                | FastAPI: HTTP + SSE + webhook delivery           | Node Express in vedanta-systems reaching into Mongo |

Shared workspace-level infrastructure (not in found-footy's compose):
- `~/workspace/proxy/` — Caddy fronts all HTTP hostnames
- `~/workspace/monitor/` — Prometheus + Grafana + Loki + Promtail

External endpoints:
- **LLM inference**: `LLM_ENDPOINT_URL` in `.env`. Today
  `http://llama-small.joi`. When nexus lands (est. end of 2026), a
  single `.env` edit and container restart. Application code
  unchanged — the LLM client reads model IDs from `/v1/models` on
  startup and passes through OpenAI-compatible request shapes.
- **API-Football**: unchanged.

### Why Postgres for structured data

Full rationale in [`design-audit.md`](./design-audit.md) §3, plus the
audit's incremental verdict was "keep Mongo" for backward-compat
reasons that don't apply in a fresh build. In the rebuild context:

- **Native schema is stronger than JSON Schema validators.** `CREATE
  TABLE ... NOT NULL CHECK (...)` can't be disabled or set to "warn."
- **Foreign keys enforce relationships.** `event.fixture_id REFERENCES
  fixtures(id)` catches orphans at write.
- **Transactions across rows are first-class.** No two-phase commit
  contortions for cross-collection consistency.
- **Extensions are load-bearing.** `pgvector` for embedding-based
  dedup when audit §4 Track 3 lands, `pg_trgm` for fuzzy team-name
  matching in RAG, `TimescaleDB` if telemetry grows into
  time-series shape, `pg_partman` for date-partitioning
  `event_log` — all bolt onto the same database without new
  services.
- **User preference**: informed by lived experience across recent
  projects.

Tradeoff acknowledged: `$addToSet` idempotency becomes `INSERT ...
ON CONFLICT DO NOTHING`, embedded arrays become normalized tables
with joins. In a fresh build these are the correct shapes, not
regressions.

### Why Garage for blobs

- **MinIO Inc.'s community-image trajectory is uncertain.** Web
  console removed from AGPL, replication features moved behind
  commercial license, commit cadence slowed. Betting on it
  long-term is a risk.
- **Garage is Rust, ~50 MB binary, filesystem-backed by default.**
  Single-node mode fits luv; the on-disk format is just files
  organized by content hash — `ls`-able, rsync-backupable.
- **S3-compatible API.** `boto3` / `aioboto3` client code is
  unchanged from MinIO. Only `S3_ENDPOINT` in `.env` differs.
- **Per-project isolation.** Each project's docker-compose stands
  up its own Garage instance with its own credentials and buckets.
  Blob accidents don't cross project boundaries.

Data on disk lives at `~/workspace/data/found-footy/garage/`
following the CLAUDE.md `data/<project>/` convention. Backup is
`rsync` of that directory; no proprietary format.

### Why Temporal stays

Audit §0 catalogs the load-bearing-correct patterns:
signal-with-start for serialized upload, ABANDON parent-close for
long-running children, workflow-ID-as-`$addToSet` idempotent
counters, scoped dedup via UploadWorkflow serialization. These
took experimentation to arrive at; they're not up for reconsideration.

Temporal's own Postgres (metadata store) stays as its own container.
It's independent of the application Postgres.

### Why FastAPI

- OpenAPI-native (no separate spec-writing step).
- Pydantic-native (types compose from the domain model to the API
  contract without duplication).
- Async-native (SSE with Postgres LISTEN/NOTIFY works cleanly with
  `asyncpg` and `sse-starlette`).
- Widely deployed at enterprise scale.

---

*(Remaining sections §2..§16 to follow in subsequent commits. Draft
scaffold established.)*
