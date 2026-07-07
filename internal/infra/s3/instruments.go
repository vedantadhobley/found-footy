// Instruments bundle + prometheus metric registration for the S3 adapter.
// See client.go for the client wrapper that consumes it.
package s3

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Instruments bundles the S3 adapter's metric handles + logger. One
// instance per binary, constructed at startup by the bootstrap layer
// and passed into s3.New(). Same shape as pg.Instruments / nats.Instruments.
type Instruments struct {
	log logging.Emitter
	reg *metrics.Registry

	operations       *prometheus.CounterVec
	operationLatency *prometheus.HistogramVec
	bytesTransferred *prometheus.CounterVec
}

// RegisterMetrics builds the S3 adapter's metric families, registers
// them into reg, and returns the Instruments handle for s3.New. Call
// once per binary.
//
// Metrics:
//   - found_footy_s3_operations_total{op,outcome} — every op counted
//     by op (upload/download/head/delete/presign) and outcome
//     (success/failure/missing). "missing" applies to HeadObject
//     404s: expected case, distinct from a transport failure.
//   - found_footy_s3_operation_duration_seconds{op} — histogram
//     spanning 1 ms → ~16 s (14 exp buckets, factor 2). Buckets sized
//     for typical upload sizes (kB to a few MB video clips).
//   - found_footy_s3_bytes_transferred_total{direction} — cumulative
//     upload + download byte counters. Useful for cost / capacity
//     projection on the Garage volume.
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	operations := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "s3",
		Name:      "operations_total",
		Help:      "Cumulative S3 operations, by op + outcome.",
	}, []string{"op", "outcome"})

	operationLatency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "s3",
		Name:      "operation_duration_seconds",
		Help:      "S3 operation duration in seconds, by op.",
		Buckets:   prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms → ~16s
	}, []string{"op"})

	bytesTransferred := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "s3",
		Name:      "bytes_transferred_total",
		Help:      "Cumulative bytes uploaded + downloaded through the S3 adapter.",
	}, []string{"direction"})

	reg.PrometheusRegistry().MustRegister(operations, operationLatency, bytesTransferred)

	return &Instruments{
		log:              log,
		reg:              reg,
		operations:       operations,
		operationLatency: operationLatency,
		bytesTransferred: bytesTransferred,
	}
}

// emitEvent — shared helper matching nats.Instruments.emitEvent for
// callsite brevity in the wrapper.
func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraS3, action, msg, fields...)
}
