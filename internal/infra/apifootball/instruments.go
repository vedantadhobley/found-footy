// Instruments bundle + prometheus metric registration for the API-Football adapter.
// See client.go for the client wrapper that consumes it.
package apifootball

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Instruments bundles the API-Football adapter's metric handles + logger.
// Same shape as every other adapter's Instruments.
type Instruments struct {
	log logging.Emitter
	reg *metrics.Registry

	calls           *prometheus.CounterVec
	callDuration    *prometheus.HistogramVec
	rateLimitRemain prometheus.Gauge
	dailyQuotaRemain prometheus.Gauge
}

// RegisterMetrics builds the API-Football adapter's metric families,
// registers them into reg, and returns the Instruments handle. Call
// once per binary.
//
// Metrics:
//   - found_footy_apifootball_calls_total{endpoint, outcome} —
//     endpoint is a bounded label from a small set (fixtures, events,
//     leagues, teams, status). outcome ∈ success/failure/rate_limited.
//   - found_footy_apifootball_call_duration_seconds{endpoint} — 14
//     exp buckets, 50ms → ~400s. Remote occasionally takes 5-10s.
//   - found_footy_apifootball_ratelimit_remaining — parsed from the
//     X-RateLimit-Remaining response header (PER-MINUTE burst window).
//     Falling toward 0 warns the ingest job.
//   - found_footy_apifootball_daily_quota_remaining — parsed from the
//     x-ratelimit-requests-remaining header (DAILY quota, distinct
//     from per-minute — see docs/api-football/rate-limits.md).
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	calls := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "apifootball",
		Name:      "calls_total",
		Help:      "Cumulative API-Football HTTP calls, by endpoint + outcome.",
	}, []string{"endpoint", "outcome"})

	callDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "apifootball",
		Name:      "call_duration_seconds",
		Help:      "API-Football HTTP call duration in seconds, by endpoint.",
		Buckets:   prometheus.ExponentialBuckets(0.05, 2, 14),
	}, []string{"endpoint"})

	rateLimitRemain := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "found_footy",
		Subsystem: "apifootball",
		Name:      "ratelimit_remaining",
		Help:      "Remaining requests in the current rate-limit window (from response headers).",
	})

	dailyQuotaRemain := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "found_footy",
		Subsystem: "apifootball",
		Name:      "daily_quota_remaining",
		Help:      "Remaining requests in the current daily quota (from response headers).",
	})

	reg.PrometheusRegistry().MustRegister(calls, callDuration, rateLimitRemain, dailyQuotaRemain)

	return &Instruments{
		log:              log,
		reg:              reg,
		calls:            calls,
		callDuration:     callDuration,
		rateLimitRemain:  rateLimitRemain,
		dailyQuotaRemain: dailyQuotaRemain,
	}
}

// emitEvent — shared helper matching the other adapters' pattern.
func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraAPIFootball, action, msg, fields...)
}
