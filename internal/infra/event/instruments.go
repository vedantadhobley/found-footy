// Instruments bundle + Prometheus metric registration for the event
// composer. Follows the same pg / NATS adapter template — one
// Instruments per binary, constructed at startup by the bootstrap
// layer and passed into event.New().
package event

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Instruments bundles the composer's metric handles + logger.
// Constructed via RegisterMetrics once per binary.
type Instruments struct {
	log logging.Emitter
	reg *metrics.Registry

	publishes       *prometheus.CounterVec
	publishDuration *prometheus.HistogramVec
	skew            prometheus.Counter
}

// RegisterMetrics builds the composer's metric families, registers
// them into reg, and returns the Instruments handle for event.New().
// Call once per binary.
//
// Metrics:
//   - found_footy_event_composer_publishes_total{kind,outcome} —
//     every Publish() call bucketed by kind + outcome. Outcomes:
//     "success" (both writes succeeded), "pg_write_failure" (pg
//     INSERT failed — nothing published), "nats_publish_failure"
//     (pg ok, NATS publish failed — skew), "envelope_marshal_failure"
//     (rare, JSON encoding of the envelope failed after pg write).
//   - found_footy_event_composer_publish_duration_seconds{kind} —
//     end-to-end wall-clock from Publish() entry to successful
//     return. Histogram: 0.1ms → ~1.6s (14 exp buckets).
//   - found_footy_event_composer_skew_total — cumulative pg-ok-
//     NATS-fail. A rising rate points at a flaky NATS; the outbox
//     catch-up worker (future) closes the gap on the next tick.
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	publishes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "event_composer",
		Name:      "publishes_total",
		Help:      "Cumulative composer publishes, by kind + outcome.",
	}, []string{"kind", "outcome"})

	publishDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "event_composer",
		Name:      "publish_duration_seconds",
		Help:      "End-to-end composer publish duration (pg INSERT + NATS publish).",
		Buckets:   prometheus.ExponentialBuckets(0.0001, 2, 14), // 0.1ms → ~1.6s
	}, []string{"kind"})

	skew := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "event_composer",
		Name:      "skew_total",
		Help:      "Cumulative pg-write-succeeded but NATS-publish-failed events. Outbox catch-up worker (future) closes the gap.",
	})

	reg.PrometheusRegistry().MustRegister(publishes, publishDuration, skew)

	return &Instruments{
		log:             log,
		reg:             reg,
		publishes:       publishes,
		publishDuration: publishDuration,
		skew:            skew,
	}
}

// emitEvent is a shorthand for the common "log line" call the
// composer's success/failure paths use. Same shape as pg.Instruments
// and nats.Instruments.
func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraEvent, action, msg, fields...)
}
