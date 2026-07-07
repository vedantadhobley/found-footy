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

	queries      *prometheus.CounterVec
	queryLatency *prometheus.HistogramVec
}

// RegisterMetrics: two metric families:
//   - wikidata_queries_total{outcome} — SPARQL query outcomes.
//   - wikidata_query_duration_seconds — 14 exp buckets, 50ms → ~400s
//     (Wikidata SPARQL can be slow for complex team lookups).
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	queries := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "wikidata",
		Name:      "queries_total",
		Help:      "Cumulative Wikidata SPARQL queries, by outcome.",
	}, []string{"outcome"})

	queryLatency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "wikidata",
		Name:      "query_duration_seconds",
		Help:      "Wikidata SPARQL query duration in seconds.",
		Buckets:   prometheus.ExponentialBuckets(0.05, 2, 14),
	}, []string{})

	reg.PrometheusRegistry().MustRegister(queries, queryLatency)
	return &Instruments{log: log, reg: reg, queries: queries, queryLatency: queryLatency}
}

func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraWikidata, action, msg, fields...)
}
