// Instruments bundle + prometheus metric registration for the NATS adapter.
// See conn.go for the connection wrapper that consumes it.
package nats

import (
	"context"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Instruments bundles the NATS adapter's metric handles + logger. One
// instance per binary, constructed at startup by the bootstrap layer
// and passed into nats.New(). Same shape as pg.Instruments — every
// adapter follows this template.
type Instruments struct {
	log logging.Emitter
	reg *metrics.Registry

	publishes         *prometheus.CounterVec
	publishDuration   *prometheus.HistogramVec
	subscribes        *prometheus.CounterVec
	messagesDelivered *prometheus.CounterVec
	connectionState   prometheus.Gauge
	reconnects        prometheus.Counter
}

// RegisterMetrics builds the NATS adapter's metric families, registers
// them into reg, and returns the Instruments handle for nats.New().
// Call once per binary.
//
// Metrics:
//   - found_footy_nats_publishes_total{subject_kind,outcome} — every
//     publish counted by subject_kind (event/fixture/other) and
//     outcome (success/failure). subject_kind is derived from the
//     subject's first token to keep label cardinality bounded.
//   - found_footy_nats_publish_duration_seconds{subject_kind} —
//     histogram spanning 0.1ms → ~1.6s (14 exp buckets, factor 2).
//   - found_footy_nats_subscribes_total{subject_kind,outcome} —
//     Subscribe() call outcomes.
//   - found_footy_nats_messages_delivered_total{subject_kind} —
//     per-message delivery count into a subscription handler.
//   - found_footy_nats_connection_state — 1 when connected, 0
//     otherwise. Updated via the disconnect/reconnect callbacks.
//   - found_footy_nats_reconnects_total — cumulative reconnect
//     count; a rising value points at a flaky NATS or network.
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	publishes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "nats",
		Name:      "publishes_total",
		Help:      "Cumulative count of NATS publishes, by subject kind + outcome.",
	}, []string{"subject_kind", "outcome"})

	publishDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "nats",
		Name:      "publish_duration_seconds",
		Help:      "Duration of NATS publishes in seconds, by subject kind.",
		Buckets:   prometheus.ExponentialBuckets(0.0001, 2, 14), // 0.1ms → ~1.6s
	}, []string{"subject_kind"})

	subscribes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "nats",
		Name:      "subscribes_total",
		Help:      "Cumulative Subscribe() outcomes, by subject kind + outcome.",
	}, []string{"subject_kind", "outcome"})

	messagesDelivered := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "nats",
		Name:      "messages_delivered_total",
		Help:      "Cumulative messages delivered into subscription handlers, by subject kind.",
	}, []string{"subject_kind"})

	connectionState := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "found_footy",
		Subsystem: "nats",
		Name:      "connection_state",
		Help:      "1 if the NATS connection is currently up, 0 otherwise.",
	})

	reconnects := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "nats",
		Name:      "reconnects_total",
		Help:      "Cumulative NATS reconnection events.",
	})

	reg.PrometheusRegistry().MustRegister(publishes, publishDuration, subscribes, messagesDelivered, connectionState, reconnects)

	return &Instruments{
		log:               log,
		reg:               reg,
		publishes:         publishes,
		publishDuration:   publishDuration,
		subscribes:        subscribes,
		messagesDelivered: messagesDelivered,
		connectionState:   connectionState,
		reconnects:        reconnects,
	}
}

// classifySubject buckets a NATS subject by its domain token — the token
// after the found-footy project prefix. Keeps prometheus cardinality
// bounded — one series per domain family, not one per
// (fixture_id, event_id, share_id) combination.
//
// Subjects the found-footy producer uses (per decisions.md 2026-08-14):
//
//	found-footy.event.video                               → "event"
//	found-footy.fixture.presentation, found-footy.fixture.update → "fixture"
//
// Legacy unprefixed subjects (event.*, fixture.*) still classify during
// the transition; anything else → "other".
func classifySubject(subject string) string {
	// Strip the project prefix so the domain token drives the label.
	s := strings.TrimPrefix(subject, "found-footy.")
	dot := strings.IndexByte(s, '.')
	if dot <= 0 {
		return "other"
	}
	switch s[:dot] {
	case "event":
		return "event"
	case "fixture":
		return "fixture"
	default:
		return "other"
	}
}

// emitEvent is a shorthand for the common "log + metric" pair the
// callbacks + publish path use.
func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraNATS, action, msg, fields...)
}
