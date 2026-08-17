// telemetry.go provides replay-safe structured timing logs for workflows.
package workflow

import (
	"time"

	"go.temporal.io/sdk/log"

	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// emitWorkflowMeasurement attaches the typed workflow vocabulary to a
// Temporal SDK log. It is deliberately log-only: callers must never branch on
// a measured duration or use it to alter the workflow command sequence.
func emitWorkflowMeasurement(logger log.Logger, action vocabulary.Action, message string, fields ...interface{}) {
	base := []interface{}{
		"module", string(vocabulary.ModuleEventWorkflow),
		"action", string(action),
	}
	logger.Info(message, append(base, fields...)...)
}

// elapsedMilliseconds returns a stable non-negative whole-millisecond
// duration. Temporal workflow callers supply workflow.Now timestamps, so the
// value remains deterministic during replay.
func elapsedMilliseconds(start, end time.Time) int64 {
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}
