// Instruments bundle + Prometheus metric registration for the event composer's
// event_log writes. Per decisions.md 2026-08-14 the composer is event_log-only
// (the NATS half moved to event.NatsPublisher), so these meter pg appends, not
// bus publishes.
package event

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Instruments bundles the composer's metric handles + logger. One instance per
// binary, constructed via RegisterMetrics.
type Instruments struct {
	log logging.Emitter
	reg *metrics.Registry

	publishes       *prometheus.CounterVec
	publishDuration *prometheus.HistogramVec
}

// RegisterMetrics builds the composer's metric families, registers them into
// reg, and returns the Instruments handle for event.New(). Call once per binary.
//
// Metrics:
//   - found_footy_event_composer_publishes_total{kind,outcome} — every Publish()
//     bucketed by event_log type + outcome ("success" or "pg_write_failure").
//   - found_footy_event_composer_publish_duration_seconds{kind} — event_log
//     INSERT wall-clock. Histogram: 0.1ms → ~1.6s (14 exp buckets).
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	publishes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "event_composer",
		Name:      "publishes_total",
		Help:      "Cumulative composer event_log writes, by type + outcome.",
	}, []string{"kind", "outcome"})

	publishDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "event_composer",
		Name:      "publish_duration_seconds",
		Help:      "event_log INSERT duration in seconds, by type.",
		Buckets:   prometheus.ExponentialBuckets(0.0001, 2, 14), // 0.1ms → ~1.6s
	}, []string{"kind"})

	reg.PrometheusRegistry().MustRegister(publishes, publishDuration)

	return &Instruments{
		log:             log,
		reg:             reg,
		publishes:       publishes,
		publishDuration: publishDuration,
	}
}

// emitEvent is a shorthand for the composer's log emissions.
func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraEvent, action, msg, fields...)
}
