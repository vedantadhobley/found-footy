// Instruments bundle + prometheus metric registration for the LLM adapter.
// See client.go for the client wrapper that consumes it.
package llm

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Instruments bundles the LLM adapter's metric handles + logger. Same
// shape as every other adapter's Instruments (pg / nats / s3 /
// temporal).
type Instruments struct {
	log logging.Emitter
	reg *metrics.Registry

	calls           *prometheus.CounterVec
	callDuration    *prometheus.HistogramVec
	tokens          *prometheus.CounterVec
	concurrentCalls prometheus.Gauge
	connectionState prometheus.Gauge
}

// RegisterMetrics builds the LLM adapter's metric families, registers
// them into reg, and returns the Instruments handle. Call once per
// binary.
//
// Metrics:
//   - found_footy_llm_calls_total{kind, outcome} — kind ∈ chat/embed;
//     outcome ∈ success/failure. Rolled into the four-golden-signals
//     dashboard.
//   - found_footy_llm_call_duration_seconds{kind} — 14 exp buckets,
//     0.05s → ~800s. Vision calls on Qwen3-VL take 3-30s; the upper
//     tail catches degraded joi performance.
//   - found_footy_llm_tokens_used_total{kind, direction} — direction ∈
//     prompt/completion. Populated from the API response's usage block.
//   - found_footy_llm_concurrent_calls — live count of in-flight chat
//     calls. Should sit at ≤ ChatConcurrencyCap; sustained saturation
//     means the semaphore is bottlenecking the pipeline.
//   - found_footy_llm_connection_state — 1 when /v1/models probe last
//     succeeded, 0 otherwise.
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	calls := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "llm",
		Name:      "calls_total",
		Help:      "Cumulative LLM calls, by kind (chat/embed) + outcome (success/failure).",
	}, []string{"kind", "outcome"})

	callDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "llm",
		Name:      "call_duration_seconds",
		Help:      "LLM call duration in seconds, by kind.",
		Buckets:   prometheus.ExponentialBuckets(0.05, 2, 14), // 50ms → ~400s
	}, []string{"kind"})

	tokens := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "llm",
		Name:      "tokens_used_total",
		Help:      "Cumulative LLM tokens by kind + direction (prompt/completion).",
	}, []string{"kind", "direction"})

	concurrentCalls := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "found_footy",
		Subsystem: "llm",
		Name:      "concurrent_calls",
		Help:      "In-flight LLM chat calls; should sit at or below ChatConcurrencyCap.",
	})

	connectionState := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "found_footy",
		Subsystem: "llm",
		Name:      "connection_state",
		Help:      "1 if the last /v1/models probe succeeded, 0 otherwise.",
	})

	reg.PrometheusRegistry().MustRegister(calls, callDuration, tokens, concurrentCalls, connectionState)

	return &Instruments{
		log:             log,
		reg:             reg,
		calls:           calls,
		callDuration:    callDuration,
		tokens:          tokens,
		concurrentCalls: concurrentCalls,
		connectionState: connectionState,
	}
}

// emitEvent — shared helper matching the other adapters' pattern.
func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraLLM, action, msg, fields...)
}
