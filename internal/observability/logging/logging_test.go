package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"DEBUG":   LevelDebug,
		"debug":   LevelDebug,
		"  INFO ": LevelInfo,
		"WARN":    LevelWarn,
		"WARNING": LevelWarn,
		"ERROR":   LevelError,
		"":        LevelInfo, // empty defaults to INFO
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}

	// Unknown value returns INFO (with a stderr warning we don't assert on).
	if got := ParseLevel("chatty"); got != LevelInfo {
		t.Errorf("ParseLevel(unknown) = %v, want %v (LevelInfo)", got, LevelInfo)
	}
}

func TestEmit_JSON_ContainsStandardFields(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(config.ObservabilityConfig{
		LogLevel:  "INFO",
		LogFormat: "json",
	}, &buf)

	e.Emit(context.Background(), LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionStartup,
		"binary starting",
		String("git_sha", "abc123"),
		Int("port", 8080),
	)

	line := strings.TrimSpace(buf.String())
	var parsed map[string]any
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("emitted line is not valid JSON: %v\nline: %s", err, line)
	}

	// Standard fields
	if parsed["module"] != "deploy" {
		t.Errorf("module = %v, want deploy", parsed["module"])
	}
	if parsed["action"] != "startup" {
		t.Errorf("action = %v, want startup", parsed["action"])
	}
	if parsed["msg"] != "binary starting" {
		t.Errorf("msg = %v, want 'binary starting'", parsed["msg"])
	}

	// Caller fields
	if parsed["git_sha"] != "abc123" {
		t.Errorf("git_sha = %v, want abc123", parsed["git_sha"])
	}
	if parsed["port"] != float64(8080) { // JSON numbers are float64 after unmarshal
		t.Errorf("port = %v, want 8080", parsed["port"])
	}

	// slog auto-adds
	if _, ok := parsed["time"]; !ok {
		t.Error("missing time field")
	}
	if _, ok := parsed["level"]; !ok {
		t.Error("missing level field")
	}
}

func TestEmit_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(config.ObservabilityConfig{
		LogLevel:  "INFO",
		LogFormat: "text",
	}, &buf)

	e.Emit(context.Background(), LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionStartup, "hi")

	line := strings.TrimSpace(buf.String())
	if !strings.Contains(line, "module=deploy") {
		t.Errorf("text output missing module=deploy: %q", line)
	}
	if !strings.Contains(line, "action=startup") {
		t.Errorf("text output missing action=startup: %q", line)
	}
}

func TestEmit_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(config.ObservabilityConfig{
		LogLevel:  "WARN", // debug + info suppressed
		LogFormat: "json",
	}, &buf)

	e.Emit(context.Background(), LevelDebug, vocabulary.ModuleDeploy, vocabulary.ActionStartup, "debug msg")
	e.Emit(context.Background(), LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionStartup, "info msg")
	e.Emit(context.Background(), LevelWarn, vocabulary.ModuleDeploy, vocabulary.ActionStartup, "warn msg")

	got := buf.String()
	if strings.Contains(got, "debug msg") {
		t.Error("debug emission not suppressed by LOG_LEVEL=WARN")
	}
	if strings.Contains(got, "info msg") {
		t.Error("info emission not suppressed by LOG_LEVEL=WARN")
	}
	if !strings.Contains(got, "warn msg") {
		t.Error("warn emission unexpectedly suppressed")
	}
}

func TestErrHelper_NilAndReal(t *testing.T) {
	nilField := Err(nil)
	if nilField.Key != "error" || nilField.Value != "" {
		t.Errorf("Err(nil) = %+v, want {Key: error, Value: \"\"}", nilField)
	}

	realField := Err(context.Canceled)
	if realField.Key != "error" || realField.Value != "context canceled" {
		t.Errorf("Err(context.Canceled) = %+v, want Value='context canceled'", realField)
	}
}

func TestTestEmitter_HasAction(t *testing.T) {
	te := &TestEmitter{}
	te.Emit(context.Background(), LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionStartup, "hi")
	te.Emit(context.Background(), LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionShutdown, "bye")

	if !te.HasAction(vocabulary.ModuleDeploy, vocabulary.ActionStartup) {
		t.Error("HasAction should find recorded startup")
	}
	if te.HasAction(vocabulary.ModuleDeploy, vocabulary.ActionConfigLoaded) {
		t.Error("HasAction should not find un-recorded action")
	}

	snap := te.Snapshot()
	if len(snap) != 2 {
		t.Errorf("Snapshot returned %d entries, want 2", len(snap))
	}

	te.Reset()
	if len(te.Snapshot()) != 0 {
		t.Error("Reset should clear captured")
	}
}
