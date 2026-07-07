// Instruments bundle + prometheus metric registration for the syndication adapter.
// See client.go for the client wrapper that consumes it.
package syndication

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

	fetches      *prometheus.CounterVec
	fetchLatency *prometheus.HistogramVec
}

// RegisterMetrics: syndication_fetches_total{outcome} +
// syndication_fetch_duration_seconds.
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	fetches := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "syndication",
		Name:      "fetches_total",
		Help:      "Cumulative syndication fetches, by outcome.",
	}, []string{"outcome"})

	fetchLatency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "syndication",
		Name:      "fetch_duration_seconds",
		Help:      "Syndication fetch duration in seconds.",
		Buckets:   prometheus.ExponentialBuckets(0.05, 2, 14),
	}, []string{})

	reg.PrometheusRegistry().MustRegister(fetches, fetchLatency)
	return &Instruments{log: log, reg: reg, fetches: fetches, fetchLatency: fetchLatency}
}

func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraSyndication, action, msg, fields...)
}
