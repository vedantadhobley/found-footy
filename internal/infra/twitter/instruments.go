// Instruments bundle + prometheus metric registration for the internal Twitter service adapter.
// See client.go for the client wrapper that consumes it.
package twitter

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Instruments — same shape as every adapter's Instruments.
type Instruments struct {
	log logging.Emitter
	reg *metrics.Registry

	calls        *prometheus.CounterVec
	callDuration *prometheus.HistogramVec
}

// RegisterMetrics:
//   - twitter_calls_total{op, outcome} — op=search|verify; classified search
//     outcomes use the bounded twittersearch.ResultState enum.
//   - twitter_call_duration_seconds{op} — browser calls can take
//     30-60s+ (browser automation), so histogram spans wider.
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	calls := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "twitter",
		Name:      "calls_total",
		Help:      "Cumulative internal Twitter service calls, by operation and bounded outcome.",
	}, []string{"op", "outcome"})

	callDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "twitter",
		Name:      "call_duration_seconds",
		Help:      "Internal Twitter service call duration in seconds, by op.",
		Buckets:   prometheus.ExponentialBuckets(0.1, 2, 14), // 100ms → ~800s
	}, []string{"op"})

	reg.PrometheusRegistry().MustRegister(calls, callDuration)
	return &Instruments{log: log, reg: reg, calls: calls, callDuration: callDuration}
}

func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraTwitter, action, msg, fields...)
}
