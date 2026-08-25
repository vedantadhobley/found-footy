# observability.md — Go rebuild ledger

**Purpose.** As-shipped state of `internal/observability/` and the
observability substrate — vocabulary enums, structured logging,
Prometheus metrics, the deferred tracing boundary, and the semantic event stream
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
| Traces | deferred; no package or runtime surface | `AUD-DESIGN-TRACING` in `todo.md` |
| Semantic events | ✓ shipped as separate SQL-audit and NATS-live planes | `internal/infra/event/` |

## The vocabulary substrate

`internal/observability/vocabulary/` is the compile-time contract.
Every `logging.Emit(...)` call takes a `vocabulary.Module` + a
`vocabulary.Action`, both typed strings declared as constants. A
call site using an undeclared value is a compile error, not a
runtime "huh why isn't this indexed."

### Module registry (shipped)

Full list per `vocabulary.go`:

FF-045 removed every zero-caller compatibility constant. The registry now
contains only labels emitted by current code:

- **Workflow:** EventWorkflow, whose stable wire value remains
  `discovery_workflow` for log-query compatibility.
- **Adapters:** InfraPG, InfraNATS, InfraEvent, InfraS3, InfraLLM,
  InfraTemporal, InfraAPIFootball, InfraTwitter, InfraSyndication, and
  InfraFFmpeg.
- **Cross-cutting:** API and Deploy.

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

FF-051 extends each search line with the configured local `max_age_minutes`,
`stop_reason`, `scrolls`, `initial_articles`, `tweets_parsed`, and
`video_tweets`. FF-061 adds the bounded `result_state` and unavailable-probe
counter. The Twitter HTTP client emits the same result fields plus bounded,
secret-free evidence: final route/title, app-shell/empty/error bits,
SearchTimeline status/failure, and rate-limit headers when present.

`found_footy_twitter_calls_total{op,outcome}` uses `rendered`,
`explicit_empty`, `login`, `upstream_error`, or `unknown_timeout` for a
classified search response; transport/decode failures remain `failure` and an
older unclassified successful service remains `success` during rollout. These
are the only allowed search-result labels. Event IDs, queries, player names,
URLs, and failure text remain log/history fields, never Prometheus labels.
Usable states emit `twitter.search`; classified unavailable states emit the
warning-level `twitter.search_failed` action even when the service processed
the request and returned HTTP 200.

EventWorkflow checkpoints `attempts_completed`, `unavailable_attempts`,
`last_search_state`, and `last_search_evidence` in its downstream metadata.
The evidence therefore survives fleet reaping and failed-execution recovery.
`feed_timeout` now means `unknown_timeout` and cannot consume a usable attempt.
The [incident report](./incidents/2026-08-20-twitter-feed-suppression.md)
preserves why this surface exists.

FF-058 adds the typed InfraTwitter `twitter_verify` and
`twitter_verify_failed` actions for the static-service maintenance call. The
standalone Twitter service also emits raw JSON transition actions for
`twitter.cookie_backup_failed`, `twitter.cookie_backup_recovered`,
`twitter.cookie_reload_failed`, and `twitter.cookie_reload_recovered`; these
come from `cmd/twitter` rather than the application emitter and therefore are
not vocabulary constants or call metrics. `/status` retains the corresponding
last attempt, success, and error evidence in memory.

FF-059's opt-in raw-login process is outside the shared application emitter. It
writes JSON startup/server errors and exposes the durable operator evidence on
read-only `/status`: capture attempt and success times, auth expiry, cookie
count, fingerprint, last error, and build identity. It never logs or returns
cookie values. Because the process runs only during manual recovery, this
status surface is the primary proof rather than a new always-on metric family.

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
> metric path before promoting the candidate in [`todo.md`](./todo.md#deferred-decisions-and-validation).

**Divergence from plan §11 log-catalog generator:** Plan §11.3 said
`docs/generated/log-catalog.md` regenerates on every build via
`go generate` — the complete (module, action) matrix as a discoverable
markdown table. **NOT SHIPPED.** No generator, no catalog file.
Tracked as feature-scope candidate `AUD-DESIGN-LOG-CATALOG` in
[`todo.md`](./todo.md#deferred-decisions-and-validation).

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

## Tracing (deferred)

No adapter consumes a tracing contract, so FF-045 deleted the speculative
no-op package. Real OTLP wiring remains feature-scope candidate
`AUD-DESIGN-TRACING` in
[`todo.md`](./todo.md#deferred-decisions-and-validation); it should begin with a
measured diagnostic need and a concrete interface.

## Semantic event and live-feed planes

The SQL audit plane is the `internal/infra/event/` **Composer**. `Publish`
appends one row to Postgres
`event_log` (`INSERT ... RETURNING id`) and returns that id; it touches only
pg. The live-fanout half (the NATS envelope + SSE cursor) is no longer the
composer's job — per [decisions.md 2026-08-14](decisions.md) it moved out to
`event.NatsPublisher`. Six `Kind`s: `fixture.activated`, `fixture.completed`,
`event.detected`, `event.stable`, `event.removed`, `event.rank_recalculated`
(`subjects.go`).

`fixture.completed` records the bounded-retirement evidence: first terminal
observation, completion time, configured grace seconds, current provider
score/event parity, durable surviving-goal parity, and nullable `PEN` decision
state. Parity is nullable for exceptional terminal outcomes. These fields are
for forensic diagnosis; score quality no longer gates completion after grace.

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
- Semantic events and live feed — [api.md](./api.md) and the [orchestration ledger](./orchestration/)
