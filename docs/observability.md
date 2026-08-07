# observability.md — Go rebuild ledger

**Purpose.** As-shipped state of `internal/observability/` and the
observability substrate — vocabulary enums, structured logging,
Prometheus metrics, tracing (stub), and the semantic event stream
(pending).

Cross-refs [`../rebuild-plan.md`](design/rebuild-plan.md) §11 for the
full design intent. Divergences from §11 live in
[`../decisions.md`](decisions.md).

**Update rule.** Any change to emission taxonomy, adapter
instrumentation shape, or metric name updates this doc in the same
commit. Per the [2026-07-07 working rule](decisions.md).

## Four pillars — status

Plan §11 defines four pillars (logs, metrics, traces, semantic events).
Shipped state:

| Pillar | Status | Location |
|---|---|---|
| Logs | ✓ shipped in S1 | `internal/observability/logging/` |
| Metrics | ✓ shipped in S1 | `internal/observability/metrics/` |
| Traces | ⊘ stub (Phase 5+ per plan) | `internal/observability/tracing/tracing.go` |
| Semantic event stream | ✓ shipped O3/a (composer dual-write) | `internal/infra/event/` (composer + subjects) |

## The vocabulary substrate

`internal/observability/vocabulary/` is the compile-time contract.
Every `logging.Emit(...)` call takes a `vocabulary.Module` + a
`vocabulary.Action`, both typed strings declared as constants. A
call site using an undeclared value is a compile error, not a
runtime "huh why isn't this indexed."

### Module registry (shipped)

Full list per `vocabulary.go`:

**Workflows:** IngestWorkflow, MonitorWorkflow, EventWorkflow,
VideoValidationWorkflow, AssetPersistenceWorkflow. (IngestWorkflow, the
monitor poll workflows, and EventWorkflow emit today.)

**Domain:** Fixture, Event, Video, Alias, Discovery, Vision, Session,
TextAnalysis.

**Adapters:** InfraPG, InfraNATS, InfraEvent, InfraS3, InfraLLM,
InfraTemporal, InfraAPIFootball, InfraTwitter, InfraSyndication,
InfraFFmpeg, InfraWikidata, InfraWikipedia.

**Cross-cutting:** API, APISSE, WebhookDelivery, Scaler, Worker,
APIServer, TwitterService, Migration, Healthz, Deploy.

**Divergence from plan §11 vocabulary block:** the workflow rename
(TwitterWorkflow → EventWorkflow, DownloadWorkflow →
VideoValidationWorkflow, UploadWorkflow → AssetPersistenceWorkflow,
RAGWorkflow folded into Ingest) — logged in
[decisions.md 2026-07-05 workflow-rename entry](decisions.md).

### Action registry (shipped)

Actions are per-family. `vocabulary.go` declares cross-cutting actions
(startup, shutdown, config_loaded, healthz_ok, etc.). Per-adapter
actions live in `actions_infra_<name>.go` — one file per adapter,
one const block per family — one `actions_infra_<name>.go` per adapter
module (the Adapters list above; source of truth is `vocabulary.go`).

Each family's actions register via `registerActions(...)` in an
`init()` so `IsKnownAction` catches strays that slip through the
compile-time enum (e.g. `vocabulary.Action("typo")` synthesized at
a call site).

## Log emission (shipped)

`internal/observability/logging/logging.go` shape:

```go
type Field struct { Key string; Value any }

type Emitter interface {
    Emit(ctx context.Context, level Level, module vocabulary.Module,
         action vocabulary.Action, msg string, fields ...Field)
}

func New(cfg config.ObservabilityConfig, m *metrics.Registry) Emitter
```

Backing implementation: `slogEmitter` writes JSON via `log/slog` to
stdout. Promtail on the host scrapes stdout, ships to Loki. Standard
container log discipline.

Typed Field helpers: `String`, `Int`, `Int64`, `Float64`, `Bool`,
`Err`. Callers use these to keep the field map type-safe rather than
building `map[string]any` inline.

**Base fields on every log line** (per plan §11 canonical schema):
`ts`, `level`, `module`, `action`, `msg`, plus (when applicable)
`workflow_id`, `activity_id`, `duration_ms`, and `error` (from
`logging.Err`, holding `err.Error()`).

> **Gap (#178 / G6):** `logging.Err` emits a single `error` field, not the
> typed `error_class` the plan's schema names — so the `calls_total{error_class}`
> metric label reads a key that's never set and is always empty. Tracked in the
> G6 observability cluster.

**Divergence from plan §11 log-catalog generator:** Plan §11.3 said
`docs/generated/log-catalog.md` regenerates on every build via
`go generate` — the complete (module, action) matrix as a discoverable
markdown table. **NOT SHIPPED.** No generator, no catalog file.
Logged in [decisions.md 2026-07-07](decisions.md).

## TestEmitter (shipped)

`internal/observability/logging/testemitter.go` — the test double
used by every adapter's unit test.

```go
type TestEmitter struct { Captured []CapturedEntry }  // + HasAction/Snapshot/Reset helpers
```

Captures all `Emit` calls into a slice for assertion. Every adapter
test constructs one, passes it via the `RegisterMetrics(reg, log)`
constructor, and asserts specific emissions fired (or didn't) as
part of behavior tests.

## Metrics substrate (shipped)

`internal/observability/metrics/metrics.go`:

```go
type Registry struct { *prometheus.Registry }

func New() *Registry
```

Each adapter's `RegisterMetrics(reg *metrics.Registry, log
logging.Emitter) *<Adapter>Instruments` bundles its counters +
histograms + prometheus.Collector for scrape-time gauges + emits a
"metrics_registered" log line.

Adapters that have shipped instruments:
- `pg` — query duration histogram + pool-stats collector
- `nats` — publish/subscribe counters + queue-depth collector
- `s3` — operations counter + operation-latency histogram + bytes-transferred counter
- `llm` — call counter + call-duration histogram + token counter + concurrency & connection-state gauges (no retry counter)
- `temporal` — worker task counter + workflow-start counter
- `apifootball` — request counter labeled by endpoint + daily-quota gauge
- `twitter`, `syndication`, `wikidata` — request counters + latency histograms

## Tracing (stub)

`internal/observability/tracing/tracing.go` — Phase F stub. 20 lines.
Returns a `Noop() *Tracer{}` sentinel. Adapters that need a Tracer
handle in their signature use this so the interface stabilizes
without a real OTLP pipeline attached.

Real OTLP wiring lands in Phase 5+ per plan §11 four-pillars table.

## Semantic event stream (shipped O3/a)

Plan §11 pillar 4 — semantic events dual-written to Postgres `event_log` AND
published to NATS — shipped as the `internal/infra/event/` **Composer**
(`Publish` INSERTs `event_log` via `RETURNING id`, then publishes an envelope
carrying that id as the SSE cursor). Six `Kind`s: `fixture.activated`,
`fixture.completed`, `event.detected`, `event.stable`, `event.removed`,
`event.rank_recalculated` (`subjects.go`).

Instruments (`found_footy_event_composer_*`): `publishes_total{kind,outcome}`,
`publish_duration_seconds{kind}`, and `skew_total` — the last increments when
the pg write succeeded but the NATS publish failed (truth stays in `event_log`;
a durable outbox catch-up worker is future work, #169).

## Cross-refs

- Plan §11 — [rebuild-plan.md §11](design/rebuild-plan.md#11-observability)
- Vocabulary source — [`internal/observability/vocabulary/vocabulary.go`](../internal/observability/vocabulary/vocabulary.go)
- Emission spec — [logging.md](./logging.md)
- Metric names + labels — populated per adapter (see architecture.md)
- Semantic events (when shipped) — [orchestration.md](./orchestration.md)
