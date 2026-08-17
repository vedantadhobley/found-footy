// telemetry_test.go verifies the workflow timing log envelope and arithmetic.
package workflow

import (
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

type capturedLog struct {
	message string
	fields  []interface{}
}

func (*capturedLog) Debug(string, ...interface{}) {}
func (l *capturedLog) Info(message string, fields ...interface{}) {
	l.message = message
	l.fields = append([]interface{}(nil), fields...)
}
func (*capturedLog) Warn(string, ...interface{})  {}
func (*capturedLog) Error(string, ...interface{}) {}

func TestEmitWorkflowMeasurementAddsTypedEnvelope(t *testing.T) {
	logger := &capturedLog{}
	emitWorkflowMeasurement(logger, vocabulary.ActionEventCandidateMeasured,
		"measured", "phase", "hash", "duration_ms", int64(12))

	if logger.message != "measured" {
		t.Fatalf("message = %q, want measured", logger.message)
	}
	want := map[interface{}]interface{}{
		"module":      string(vocabulary.ModuleEventWorkflow),
		"action":      string(vocabulary.ActionEventCandidateMeasured),
		"phase":       "hash",
		"duration_ms": int64(12),
	}
	got := make(map[interface{}]interface{})
	for i := 0; i+1 < len(logger.fields); i += 2 {
		got[logger.fields[i]] = logger.fields[i+1]
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("measurement field %v = %v, want %v", key, got[key], value)
		}
	}
}

func TestElapsedMillisecondsIsNonNegative(t *testing.T) {
	start := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	if got := elapsedMilliseconds(start, start.Add(1500*time.Millisecond)); got != 1500 {
		t.Fatalf("elapsedMilliseconds = %d, want 1500", got)
	}
	if got := elapsedMilliseconds(start, start.Add(-time.Second)); got != 0 {
		t.Fatalf("negative elapsedMilliseconds = %d, want 0", got)
	}
	if got := elapsedMilliseconds(time.Time{}, start); got != 0 {
		t.Fatalf("zero-start elapsedMilliseconds = %d, want 0", got)
	}
}
