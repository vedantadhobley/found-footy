# architecture.md — Go rebuild ledger

**Purpose.** This doc records **what has actually shipped** in the
Go rebuild — the concrete tree, which packages have real code vs
which are stubs, which adapters are live, which domain packages have
what. It's the ledger against which [`../rebuild-plan.md`](../rebuild-plan.md)
is the intent.

If code and plan diverge, the divergence is logged in
[`../decisions.md`](../decisions.md) with a date and reason. If code
and plan match, no entry — silence == alignment.

**Update rule.** Every commit that adds/removes a package, changes
an adapter shape, or lands a new domain type updates this doc in
the SAME commit. Not the next commit. Same commit.

## As-shipped tree (2026-07-07, end of Phase O1)

```
found-footy/
├── cmd/                                 4 binaries — each imports from internal/
│   ├── api/main.go                      Phase 6 — FastAPI-shaped read surface + SSE
│   ├── scaler/main.go                   scaffold; no scale logic yet (Phase A/M)
│   ├── twitter/main.go                  Go stub (BlockUntilDone); Python still runs prod
│   └── worker/main.go                   Temporal worker; wired for IngestWorkflow (O1d)
├── internal/
│   ├── domain/                          4 shipped, 4 stubbed
│   │   ├── fixture/                     ✓ D1: model + State + Repo + tests
│   │   ├── event/                       ✓ D2: model + State + Repo + tests
│   │   ├── video/                       ✓ D3: model + Repo + rank + tests
│   │   ├── alias/                       ✓ D4: model + Repo + Normalize + tests
│   │   ├── discovery/                   ⊘ doc.go stub — build when DiscoveryWorkflow lands (O3)
│   │   ├── vision/                      ⊘ doc.go stub — build when VideoValidationWorkflow lands (O4)
│   │   ├── session/                     ⊘ doc.go stub — build when Twitter Go service ports (post-O)
│   │   └── textanalysis/                ⊘ doc.go stub — extensibility hook per plan §4
│   ├── infra/                           9 live, 2 stubbed
│   │   ├── pg/                          ✓ S2: pool + instruments + schema.sql + FixtureRepo + AliasRepo
│   │   ├── nats/                        ✓ S3: client + instruments
│   │   ├── s3/                          ✓ S4: Garage client + instruments
│   │   ├── llm/                         ✓ S6: OpenAI-compatible client + typed errors + Chat
│   │   ├── temporal/                    ✓ S5: Client (with workerShutdownTimeout) + Worker
│   │   ├── apifootball/                 ✓ S7 + O1a: /status probe + /fixtures + /fixtures/{ids}
│   │   ├── twitter/                     ✓ S7: HTTP client + tests against mock (real service is Python)
│   │   ├── syndication/                 ✓ S7: Twitter syndication client + tests
│   │   ├── wikidata/                    ✓ S7: SPARQL client + tests
│   │   ├── event/                       ⊘ doc.go stub — semantic-event composer (build for O2 NATS emissions)
│   │   └── ffmpeg/                      ⊘ doc.go stub — subprocess wrapper (build for Phase A video pipeline)
│   ├── workflow/                        1 shipped
│   │   ├── ingest.go                    ✓ O1c: IngestWorkflow
│   │   └── ingest_test.go               ✓ O1c: 5 WorkflowTestSuite tests
│   ├── activity/                        1 package shipped
│   │   └── ingest/                      ✓ O1b: 4 activities + in-memory fakes + 11 tests
│   │       ├── activities.go
│   │       └── activities_test.go
│   ├── api/                             Phase 6 foundation only — SSE + read endpoints
│   ├── bootstrap/                       ✓ S1 (NOT IN PLAN — see decisions.md 2026-07-07)
│   │   └── bootstrap.go                 Deps + LIFO Closer registry; shared binary startup
│   ├── config/                          ✓ S1: envconfig-based Config with per-adapter sub-structs
│   ├── errors/                          scaffold only — populated as errors surface
│   ├── observability/
│   │   ├── vocabulary/                  ✓ S1: typed Module + Action enums
│   │   ├── logging/                     ✓ S1: slog Emit() + TestEmitter for unit tests
│   │   ├── metrics/                     ✓ S1: Prometheus registry helper
│   │   └── tracing/                     ⊘ empty (Phase 5+ per plan; deferred)
│   ├── scaler/                          scaffold; no logic (Phase A/M)
│   ├── testutil/                        ⊘ empty (build as testing needs surface)
│   └── usecases/                        ⊘ doc.go stub (build when cross-domain ops surface)
├── migrations/                          ⊘ EMPTY — schema.sql lives in internal/infra/pg/ instead
│                                          (see decisions.md 2026-07-07)
├── scripts/                             smoke + trigger scripts
│   ├── smoke_repos/main.go              ✓ live pg + repo smoke test (dev only)
│   └── trigger_ingest/main.go           ✓ live IngestWorkflow trigger (O1d verification)
├── caddy/found-footy.caddy              routing stubs; not yet copied into ~/workspace/proxy/caddy.d/
├── docker-compose.dev.yml               ✓ dev stack; air hot-reload on all 4 Go binaries
├── docker-compose.prod.yml              runs PYTHON codebase; unchanged (name reflects intent)
├── Dockerfile / Dockerfile.dev          ✓ multi-stage prod + air-based dev
├── go.mod / go.sum                      ✓ Go 1.25 (bumped from 1.23 for air compat)
├── Makefile                             ✓ build/test/test-short via docker run
└── docs/                                see docs/README.md for routing
```

Legend:
- `✓ <phase>` — shipped in that phase, has real code + tests
- `⊘` — stubbed (usually a `doc.go` marker), waiting for its dependent phase
- No marker — not part of the rebuild (Python-era or config)

## Domain packages — as-shipped shape

Each of the 4 shipped domain packages follows the same layout, matching
[rebuild-plan.md §4](../rebuild-plan.md#4-domain-model):

```
domain/<name>/
├── <name>.go               model type + New() constructor
├── state.go                state transitions with method receivers (mutate in place)
├── repo.go                 Repo interface + ErrNotFound sentinel
└── <name>_test.go          unit tests — pure Go, no adapters
```

**Cross-cutting rule (audit-verified):** domain packages import nothing
from `internal/infra/*`. Repos are interfaces defined in domain;
implementations live in `internal/infra/pg/` and satisfy them
structurally.

### fixture domain (D1)

Core type `fixture.Fixture` with `State` (staging/active/completed),
API-mirror fields (`APIStatus`, `APIElapsed`, `APIExtra`, scores), and
domain-managed timestamps (`ActivatedAt`, `CompletedAt`,
`LastActivityAt`, `LastPolledAt`).

State transitions:
- `Activate(at) → active` (sets ActivatedAt, LastActivityAt)
- `Complete(at) → completed` (sets CompletedAt, LastActivityAt)
- `Reschedule(newKickoff, at) → staging` (clears ActivatedAt; for PST/moved fixtures)
- `UpdateFromPoll(status, elapsed, extra, scores, at)` — refreshes
  API-mirror fields + LastPolledAt without changing state

Predicates: `ShouldActivateNow(now, window)` — used by both the ingest
activity (at-upsert-time activation for imminent kickoffs) and the
future MonitorWorkflow's `PreActivateUpcoming` step.

Repo methods shipped in `internal/infra/pg/fixture_repo.go`:
`Get`, `Upsert`, `ListByState`, `ListStagingBeforeKickoff`,
`PruneCompleted`.

### event domain (D2)

Core type `event.Event` with `State` (detected/stable/removed) and
per-event debounce counters. Model captures the 3-poll invariant
Python enforced via monitor-cycle registration counts.

Repo methods per plan §4 event — full implementation deferred (event
CRUD hits pg only when MonitorWorkflow lands in O2).

### video domain (D3)

Core types `video.Asset` and `video.Share` — the split from Python's
single `video` collection that supports the URL-stability + rank
invariants documented in `rebuild-plan.md` §3 and §4.

Ranking helpers in `rank.go` (`CompareShares` — the deterministic
tie-break Python's frontend uses).

### alias domain (D4)

Core type `alias.TeamAlias`. Fields: team_id, team_name, is_national,
country, city, wikidata_qid, wikidata_aliases, twitter_aliases,
LLM model + timestamp.

Predicates: `HasWikidataResolution()`, `HasTwitterAliases()`.
Setter: `SetTwitterAliases(aliases, model, at)` with normalization
(NFD Latin-diacritic strip).

Repo methods shipped: `Get`, `BulkGet`, `Upsert`. Load-bearing detail:
`Upsert` normalizes nil-slice fields to `[]string{}` before writing
because pg schema is `TEXT[] NOT NULL DEFAULT '{}'` and pgx serializes
Go nil-slice as SQL NULL.

## Adapters — as-shipped template

Every live adapter under `internal/infra/*/` follows the pattern
established by the pg adapter (S2):

```
infra/<name>/
├── client.go               constructor: New(ctx, cfg, instruments)
├── instruments.go          RegisterMetrics(reg, log) → *Instruments (bundle)
├── <name>_test.go          testcontainers-go OR httptest-based test
└── doc.go                  package-level docstring + "why this shape" notes
```

The `Instruments` bundle carries labeled counters/histograms + a
prometheus.Collector for scrape-time gauges + a framework-native
tracer where the adapter's library supports one (pg has QueryTracer,
NATS has connection callbacks, LLM has httptrace).

**Cross-cutting rule:** every adapter's `New(...)` returns
`(client, error)`, does NOT panic, and does NOT log at info level from
package init — all lifecycle logging goes through the
`bootstrap.Deps.Log` + vocabulary Action enums.

Adapter-specific notes:

- **pg**: pool via pgxpool; QueryTracer emits per-query duration histograms
  + pool-stats collector. Schema in `schema.sql` mounted into dev postgres
  via `/docker-entrypoint-initdb.d/` (fresh volume only) AND into
  testcontainers via `WithInitScripts`.
- **temporal**: Client wraps SDK client with `workerShutdownTimeout`
  accessor; NewWorker seeds `Options.WorkerStopTimeout` from Client if zero.
- **llm**: types.go owns domain-shaped `ChatRequest`/`ChatResponse`;
  classifyError translates HTTP status codes to typed errors
  (ErrRateLimited, ErrCapExceeded, etc.).
- **apifootball**: getJSON helper handles auth + rate-limit-header
  parsing + error classification; `/fixtures` (single + by-IDs) landed in O1a.

**Twitter service note.** `internal/infra/twitter/` is the HTTP client;
tests pass against a mock. The actual twitter container in dev runs
the Go BlockUntilDone stub (real Twitter search service is Python
`twitter/` in prod). Wire-up deferred until the Go twitter service
lands.

## Package dependency direction (audit-verified)

```
cmd/*
  ↓
internal/workflow/          (workflow definitions)
  ↓
internal/activity/*/        (activities — the boundary)
  ↓                              ↓
internal/domain/*/          internal/infra/*/  (adapters)
                                   ↑
                                   └── config, observability, bootstrap
```

**Never happens:**
- `internal/domain/*` importing `internal/infra/*` — enforced by review
- `internal/workflow/*` importing `internal/infra/*` — activities are the boundary
- `internal/infra/<a>` importing `internal/infra/<b>` — one composer package
  (`internal/infra/event/`, when built) is the sole exception

## Cross-refs

- Plan §2 (repo structure) — [rebuild-plan.md §2](../rebuild-plan.md#2-repository-structure)
- Plan §3 (schema) — [rebuild-plan.md §3](../rebuild-plan.md#3-postgres-schema)
- Plan §4 (domain model) — [rebuild-plan.md §4](../rebuild-plan.md#4-domain-model)
- Plan §9 (adapters) — [rebuild-plan.md §9](../rebuild-plan.md#adapter-inventory)
- Divergences from this baseline live in [decisions.md](../decisions.md)
- Orchestration + workflow ledger: [orchestration.md](./orchestration.md)
- Observability substrate: [observability.md](./observability.md)
- Testing patterns: [testing.md](./testing.md)
