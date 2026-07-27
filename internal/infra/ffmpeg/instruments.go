// Instruments bundle + prometheus metric registration for the ffmpeg
// adapter. See client.go for the wrapper that consumes it.
package ffmpeg

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

	ops       *prometheus.CounterVec
	opLatency *prometheus.HistogramVec
}

// RegisterMetrics: found_footy_ffmpeg_ops_total{op,outcome} +
// found_footy_ffmpeg_op_duration_seconds{op}.
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Instruments {
	ops := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "ffmpeg",
		Name:      "ops_total",
		Help:      "Cumulative ffmpeg/ffprobe operations, by op + outcome.",
	}, []string{"op", "outcome"})

	opLatency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "ffmpeg",
		Name:      "op_duration_seconds",
		Help:      "ffmpeg/ffprobe operation duration in seconds.",
		Buckets:   prometheus.ExponentialBuckets(0.01, 2, 14),
	}, []string{"op"})

	reg.PrometheusRegistry().MustRegister(ops, opLatency)
	return &Instruments{log: log, reg: reg, ops: ops, opLatency: opLatency}
}

func (ins *Instruments) emitEvent(ctx context.Context, level logging.Level, action vocabulary.Action, msg string, fields ...logging.Field) {
	ins.log.Emit(ctx, level, vocabulary.ModuleInfraFFmpeg, action, msg, fields...)
}
