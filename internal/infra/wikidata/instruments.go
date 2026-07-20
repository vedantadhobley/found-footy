// Instruments bundle + prometheus metric registration for the Wikidata adapter.
// See client.go for the client wrapper that consumes it.
package wikidata

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

	// requests_total labeled by (endpoint, outcome). endpoint is one of
	// "sparql" (Query), "search" (SearchEntities), "entity" (GetEntity).
	// outcome is one of "success", "failure".
	requests       *prometheus.CounterVec
	requestLatency *prometheus.HistogramVec
}

// RegisterMetrics: two metric families:
//   - wikidata_requests_total{endpoint,outcome} — outcomes per endpoint.
//   - wikidata_request_duration_seconds{endpoint} — 14 exp buckets, 50ms → ~400s.
//     Wikidata SPARQL can be slow; wbsearchentities and entity-JSON
//     fetches are fast (usually <500ms). Same histogram covers both.
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "wikidata",
		Name:      "requests_total",
		Help:      "Cumulative Wikidata HTTP requests, by endpoint and outcome.",
	}, []string{"endpoint", "outcome"})

	requestLatency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "wikidata",
		Name:      "request_duration_seconds",
		Help:      "Wikidata request duration in seconds, by endpoint.",
		Buckets:   prometheus.ExponentialBuckets(0.05, 2, 14),
	}, []string{"endpoint"})

	reg.PrometheusRegistry().MustRegister(requests, requestLatency)
	return &Instruments{log: log, reg: reg, requests: requests, requestLatency: requestLatency}
}

func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraWikidata, action, msg, fields...)
}
