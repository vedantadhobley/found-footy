# observability.md — Go rebuild ledger

**Purpose.** As-shipped state of `internal/observability/` and the
observability substrate — vocabulary enums, structured logging,
Prometheus metrics, tracing (stub), and the semantic event stream
(pending).

Cross-refs [`../rebuild-plan.md`](../rebuild-plan.md) §11 for the
full design intent. Divergences from §11 live in
[`../decisions.md`](../decisions.md).

**Update rule.** Any change to emission taxonomy, adapter
instrumentation shape, or metric name updates this doc in the same
commit. Per the [2026-07-07 working rule](../decisions.md).

## Four pillars — status

Plan §11 defines four pillars (logs, metrics, traces, semantic events).
Shipped state:

| Pillar | Status | Location |
|---|---|---|
| Logs | ✓ shipped in S1 | `internal/observability/logging/` |
| Metrics | ✓ shipped in S1 | `internal/observability/metrics/` |
| Traces | ⊘ stub (Phase 5+ per plan) | `internal/observability/tracing/tracing.go` |
| Semantic event stream | ⊘ deferred to O2 | `internal/infra/event/` (composer stub) |

## The vocabulary substrate

`internal/observability/vocabulary/` is the compile-time contract.
Every `logging.Emit(...)` call takes a `vocabulary.Module` + a
`vocabulary.Action`, both typed strings declared as constants. A
call site using an undeclared value is a compile error, not a
runtime "huh why isn't this indexed."

### Module registry (shipped)

Full list per `vocabulary.go`:

**Workflows:** IngestWorkflow, MonitorWorkflow, DiscoveryWorkflow,
VideoValidationWorkflow, AssetPersistenceWorkflow. (Only IngestWorkflow
has emissions today; the rest are pre-declared for their upcoming phases.)

**Domain:** Fixture, Event, Video, Alias, Discovery, Vision, Session,
TextAnalysis.

**Adapters:** InfraPG, InfraNATS, InfraEvent, InfraS3, InfraLLM,
InfraTemporal, InfraAPIFootball, InfraTwitter, InfraSyndication,
InfraFFmpeg, InfraWikidata.

**Cross-cutting:** API, APISSE, WebhookDelivery, Scaler, Worker,
APIServer, TwitterService, Migration, Healthz, Deploy.

**Divergence from plan §11 vocabulary block:** the workflow rename
(TwitterWorkflow → DiscoveryWorkflow, DownloadWorkflow →
VideoValidationWorkflow, UploadWorkflow → AssetPersistenceWorkflow,
RAGWorkflow folded into Ingest) — logged in
[decisions.md 2026-07-05 workflow-rename entry](../decisions.md).

### Action registry (shipped)

Actions are per-family. `vocabulary.go` declares cross-cutting actions
(startup, shutdown, config_loaded, healthz_ok, etc.). Per-adapter
actions live in `actions_infra_<name>.go` — one file per adapter,
one const block per family. Ten adapter files shipped, matching the
ten adapter modules.

Each family's actions register via `registerActions(...)` in an
`init()` so `IsKnownAction` catches strays that slip through the
compile-time enum (e.g. `vocabulary.Action("typo")` synthesized at
a call site).

## Log emission (shipped)

`internal/observability/logging/logging.go` shape:

```go
type Field struct { Key string; Value any }

type Emitter interface {
    Emit(level Level, module vocabulary.Module, action vocabulary.Action,
         msg string, fields ...Field)
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
`workflow_id`, `activity_id`, `duration_ms`, `error_class`,
`error_message`.

**Divergence from plan §11 log-catalog generator:** Plan §11.3 said
`docs/generated/log-catalog.md` regenerates on every build via
`go generate` — the complete (module, action) matrix as a discoverable
markdown table. **NOT SHIPPED.** No generator, no catalog file.
Logged in [decisions.md 2026-07-07](../decisions.md).

## TestEmitter (shipped)

`internal/observability/logging/testemitter.go` — the test double
used by every adapter's unit test.

```go
type TestEmitter struct { Emissions []Emission }
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
- `s3` — request counter + bytes-transferred histogram
- `llm` — request counter + concurrency-cap gauge + retry counter
- `temporal` — worker task counter + workflow-start counter
- `apifootball` — request counter labeled by endpoint + daily-quota gauge
- `twitter`, `syndication`, `wikidata` — request counters + latency histograms

## Tracing (stub)

`internal/observability/tracing/tracing.go` — Phase F stub. 20 lines.
Returns a `Noop() *Tracer{}` sentinel. Adapters that need a Tracer
handle in their signature use this so the interface stabilizes
without a real OTLP pipeline attached.

Real OTLP wiring lands in Phase 5+ per plan §11 four-pillars table.

## Semantic event stream — pending

Plan §11 pillar 4 (semantic events published to NATS + written to
Postgres `event_log`) requires the `internal/infra/event/` composer
(the dual-write). Composer is stubbed; ships in Phase O2 alongside
MonitorWorkflow's `event.detected` / `event.stable` / `event.removed`
emissions.

The `dual_write_skew_total` metric mentioned in plan §11 exists only
as a design line; not registered yet.

## Cross-refs

- Plan §11 — [rebuild-plan.md §11](../rebuild-plan.md#11-observability)
- Vocabulary source — [`internal/observability/vocabulary/vocabulary.go`](../../internal/observability/vocabulary/vocabulary.go)
- Emission spec — [logging.md](./logging.md)
- Metric names + labels — populated per adapter (see architecture.md)
- Semantic events (when shipped) — [orchestration.md](./orchestration.md)
