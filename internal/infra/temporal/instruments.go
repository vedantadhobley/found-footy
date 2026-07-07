// Instruments bundle + prometheus metric registration for the Temporal adapter.
// See client.go for the client wrapper that consumes it.
package temporal

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Instruments bundles the Temporal adapter's metric handles + logger.
// One instance per binary, constructed at startup by the bootstrap layer
// and passed into temporal.NewClient(). Same shape as
// pg/nats/s3 Instruments.
type Instruments struct {
	log logging.Emitter
	reg *metrics.Registry

	workflowStarts    *prometheus.CounterVec
	workflowStartTime *prometheus.HistogramVec
	signals           *prometheus.CounterVec
	connectionState   prometheus.Gauge
}

// RegisterMetrics builds the Temporal adapter's metric families,
// registers them into reg, and returns the Instruments handle. Call
// once per binary.
//
// Metrics:
//   - found_footy_temporal_workflow_starts_total{workflow_type,outcome}
//     — every StartWorkflow counted by the workflow's type name +
//     outcome (success/failure). workflow_type is a bounded label
//     because we have a fixed set of workflow types (Ingest, Monitor,
//     Discovery, VideoValidation, AssetPersistence — per decisions.md
//     2026-07-07 workflow-rename entry).
//   - found_footy_temporal_workflow_start_duration_seconds{workflow_type}
//     — how long the StartWorkflow RPC took (not how long the workflow
//     ran; that's Temporal's own metric).
//   - found_footy_temporal_signals_total{outcome} — SignalWorkflow
//     outcomes. Signal target-workflow-type isn't always known at the
//     call site, so we don't label by workflow_type here.
//   - found_footy_temporal_connection_state — 1 when the client has
//     dialed the frontend at least once; 0 otherwise. Coarse signal,
//     since the client dials lazily on first RPC.
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	workflowStarts := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "temporal",
		Name:      "workflow_starts_total",
		Help:      "Cumulative StartWorkflow calls, by workflow type + outcome.",
	}, []string{"workflow_type", "outcome"})

	workflowStartTime := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "temporal",
		Name:      "workflow_start_duration_seconds",
		Help:      "StartWorkflow RPC duration in seconds, by workflow type.",
		Buckets:   prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms → ~16s
	}, []string{"workflow_type"})

	signals := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "temporal",
		Name:      "signals_total",
		Help:      "Cumulative SignalWorkflow calls, by outcome.",
	}, []string{"outcome"})

	connectionState := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "found_footy",
		Subsystem: "temporal",
		Name:      "connection_state",
		Help:      "1 if the Temporal client has connected to the frontend, 0 otherwise.",
	})

	reg.PrometheusRegistry().MustRegister(workflowStarts, workflowStartTime, signals, connectionState)

	return &Instruments{
		log:               log,
		reg:               reg,
		workflowStarts:    workflowStarts,
		workflowStartTime: workflowStartTime,
		signals:           signals,
		connectionState:   connectionState,
	}
}

// emitEvent — shared helper matching the other adapters' emitEvent for
// callsite brevity.
func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraTemporal, action, msg, fields...)
}
