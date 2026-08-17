# observability.md — Go rebuild ledger

**Purpose.** As-shipped state of `internal/observability/` and the
observability substrate — vocabulary enums, structured logging,
Prometheus metrics, tracing (stub), and the semantic event stream
(shipped).

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
| Logs | ✓ shipped | `internal/observability/logging/` |
| Metrics | ✓ shipped | `internal/observability/metrics/` |
| Traces | ⊘ no-op stub | `internal/observability/tracing/tracing.go` |
| Semantic events | ✓ shipped as separate SQL-audit and NATS-live planes | `internal/infra/event/` |

## The vocabulary substrate

`internal/observability/vocabulary/` is the compile-time contract.
Every `logging.Emit(...)` call takes a `vocabulary.Module` + a
`vocabulary.Action`, both typed strings declared as constants. A
call site using an undeclared value is a compile error, not a
runtime "huh why isn't this indexed."

### Module registry (shipped, with compatibility debt)

Full list per `vocabulary.go`:

The registry still carries names from superseded workflow topology. Its
workflow constants are `IngestWorkflow`, `MonitorWorkflow`, `EventWorkflow`
(whose string value remains `discovery_workflow`),
`VideoValidationWorkflow`, and `AssetPersistenceWorkflow`. The shipped
workflow types are listed in [`orchestration.md`](./orchestration.md); do not
infer their existence from this compatibility vocabulary.

**Domain:** Fixture, Event, Video, Alias, Discovery, Vision, Session,
TextAnalysis.

**Adapters:** InfraPG, InfraNATS, InfraEvent, InfraS3, InfraLLM,
InfraTemporal, InfraAPIFootball, InfraTwitter, InfraSyndication, InfraFFmpeg,
plus the now-unused InfraWikidata and InfraWikipedia constants.

**Cross-cutting:** API, APISSE, WebhookDelivery, Worker,
APIServer, TwitterService, Migration, Healthz, Deploy.

Removing or renaming the dormant workflow, Wikidata/Wikipedia, SSE, and webhook
vocabulary is tracked by `AUD-0815-ROT` in
[`todo.md`](./todo.md#audit-intake-requiring-current-code-validation).

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

`actions_workflow.go` owns FF-050's EventWorkflow measurement actions:
`event_lifecycle_measured`, `event_search_measured`,
`event_candidate_measured`, and `event_publish_measured`.

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

Temporal workflows use the SDK's replay-aware logger instead of the application
emitter. `internal/workflow/telemetry.go` adds the same typed `module` and
`action` vocabulary to those lines while preserving the SDK's replay
suppression. These workflow lines do not increment the emitter-derived call
metrics.

FF-050 emits correlated workflow-observed timings for lifecycle, each Twitter
search, candidate observation persistence, download, dense hash, vision,
promotion, terminal persistence, and `event.video` publication. Candidate
lines carry `event_id`, `fixture_id`, `tweet_url`, `search_attempt`,
`recovered`, `phase`, `outcome`, `duration_ms`, and `event_elapsed_ms`.
Publication lines carry the promotion or supersede cause. Durations include
Temporal queueing and retries; they are not activity CPU timers. They use
`workflow.Now`, feed logs only, and never affect commands or acceptance. No
event or tweet identifier is a Prometheus label.

Typed Field helpers: `String`, `Int`, `Int64`, `Float64`, `Bool`,
`Err`. Callers use these to keep the field map type-safe rather than
building `map[string]any` inline.

**Base fields on every log line** (per plan §11 canonical schema):
`ts`, `level`, `module`, `action`, `msg`, plus (when applicable)
`workflow_id`, `activity_id`, `duration_ms`, and `error` (from
`logging.Err`, holding `err.Error()`).

> **Tracked gap (`AUD-0813-P2-13`):** `logging.Err` emits a single `error` field, not the
> typed `error_class` the plan's schema names — so the `calls_total{error_class}`
> metric label reads a key that's never set and is always empty. Validate the
> metric path before promoting the candidate in [`todo.md`](./todo.md#audit-intake-requiring-current-code-validation).

**Divergence from plan §11 log-catalog generator:** Plan §11.3 said
`docs/generated/log-catalog.md` regenerates on every build via
`go generate` — the complete (module, action) matrix as a discoverable
markdown table. **NOT SHIPPED.** No generator, no catalog file.
Tracked as feature-scope candidate `AUD-DESIGN-LOG-CATALOG` in
[`todo.md`](./todo.md#audit-intake-requiring-current-code-validation).

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

Instrument bundles currently ship for `pg`, `nats`, `s3`, `llm`, `temporal`,
`apifootball`, `twitter`, `syndication`, `event`, and `ffmpeg`. Their
`instruments.go` files are the metric-name and label authority; this ledger
does not duplicate the full mutable catalog.

The shared bootstrap binds each binary's metrics/health socket synchronously
before application work starts (FF-026). A bind error is a startup failure,
not a degraded mode. If the listener fails after startup, bootstrap cancels
the application context, drains registered adapters, and returns a failing
process status. `/metrics` and `/healthz` therefore have the same process
lifecycle as the binary they describe.

## Tracing (stub)

`internal/observability/tracing/tracing.go` returns a `Noop() *Tracer{}`
sentinel. Adapters that need a Tracer
handle in their signature use this so the interface stabilizes
without a real OTLP pipeline attached.

Real OTLP wiring is deferred; the historical plan's phase label is not a
current schedule. It remains feature-scope candidate `AUD-DESIGN-TRACING` in
[`todo.md`](./todo.md#audit-intake-requiring-current-code-validation).

## Semantic event and live-feed planes

The SQL audit plane is the `internal/infra/event/` **Composer**. `Publish`
appends one row to Postgres
`event_log` (`INSERT ... RETURNING id`) and returns that id; it touches only
pg. The live-fanout half (the NATS envelope + SSE cursor) is no longer the
composer's job — per [decisions.md 2026-08-14](decisions.md) it moved out to
`event.NatsPublisher`. Six `Kind`s: `fixture.activated`, `fixture.completed`,
`event.detected`, `event.stable`, `event.removed`, `event.rank_recalculated`
(`subjects.go`).

The separate `NatsPublisher` owns the live fan-out plane. It emits the three
environment-scoped topics `fixture.clock`, `fixture.update`, and `event.video`
inside the workspace envelope. Payloads and consumer recovery rules live in
[`api.md`](./api.md).

Instruments (`found_footy_event_composer_*`): `publishes_total{kind,outcome}`
(outcome = `success` or `pg_write_failure`) and `publish_duration_seconds{kind}`
(the `event_log` INSERT wall-clock).

## Cross-refs

- Plan §11 — [rebuild-plan.md §11](design/rebuild-plan.md#11-observability)
- Vocabulary source — [`internal/observability/vocabulary/vocabulary.go`](../internal/observability/vocabulary/vocabulary.go)
- Emission spec — [logging.md](./logging.md)
- Metric names + labels — populated per adapter (see architecture.md)
- Semantic events and live feed — [api.md](./api.md) and [orchestration.md](./orchestration.md)
