// Instruments bundle + prometheus metric registration for the Wikipedia
// adapter. See client.go for the client wrapper that consumes it.
package wikipedia

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

	// requests_total labeled by outcome. The single endpoint family here
	// (search) doesn't warrant its own label — kept flat to match the
	// simpler adapter shape.
	requests       *prometheus.CounterVec
	requestLatency *prometheus.HistogramVec
}

// RegisterMetrics registers:
//   - wikipedia_requests_total{outcome} — success | failure
//   - wikipedia_request_duration_seconds — 14 exp buckets, 50ms → ~400s.
//     CirrusSearch usually responds in <500ms; wide buckets cover tail.
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "wikipedia",
		Name:      "requests_total",
		Help:      "Cumulative Wikipedia HTTP requests, by outcome.",
	}, []string{"outcome"})

	requestLatency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "wikipedia",
		Name:      "request_duration_seconds",
		Help:      "Wikipedia request duration in seconds.",
		Buckets:   prometheus.ExponentialBuckets(0.05, 2, 14),
	}, []string{})

	reg.PrometheusRegistry().MustRegister(requests, requestLatency)
	return &Instruments{log: log, reg: reg, requests: requests, requestLatency: requestLatency}
}

func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraWikipedia, action, msg, fields...)
}
