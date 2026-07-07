package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
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
	}, &buf, nil)

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
	}, &buf, nil)

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
	}, &buf, nil)

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

// TestEmit_BaselineMetricsIncrement verifies the §11 four-pillars
// promise: every Emit that reaches slogEmitter also increments
// log_lines_total{module,level} + calls_total{module,action,outcome,
// error_class}. Success outcome derived from DEBUG/INFO level; failure
// from WARN/ERROR. error_class extracted from a Field with key
// "error_class".
func TestEmit_BaselineMetricsIncrement(t *testing.T) {
	var buf bytes.Buffer
	reg := metrics.New()
	e := newWithWriter(config.ObservabilityConfig{
		LogLevel:  "DEBUG",
		LogFormat: "json",
	}, &buf, reg)

	e.Emit(context.Background(), LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionStartup, "hi")
	e.Emit(context.Background(), LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionStartup, "hi again")
	e.Emit(context.Background(), LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionQueryFailed,
		"query blew up", String("error_class", "pg.timeout"))

	// Scrape the registry to inspect resulting series.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	reg.Handler().ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	scrape := string(body)

	wantContains := []string{
		`found_footy_log_lines_total{level="INFO",module="deploy"} 2`,
		`found_footy_log_lines_total{level="ERROR",module="infra_pg"} 1`,
		`found_footy_calls_total{action="startup",error_class="",module="deploy",outcome="success"} 2`,
		`found_footy_calls_total{action="query_failed",error_class="pg.timeout",module="infra_pg",outcome="failure"} 1`,
	}
	for _, want := range wantContains {
		if !strings.Contains(scrape, want) {
			t.Errorf("scrape missing %q; got:\n%s", want, scrape)
		}
	}
}

// TestEmit_NilRegistryNoPanic ensures the metric-side-effect is
// optional — a logger constructed without a registry still writes
// logs, just skips the counter increment.
func TestEmit_NilRegistryNoPanic(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(config.ObservabilityConfig{LogLevel: "INFO", LogFormat: "json"}, &buf, nil)

	e.Emit(context.Background(), LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionStartup, "hi")
	if buf.Len() == 0 {
		t.Error("nil-registry emitter still needs to write logs")
	}
}

// TestEmit_UnknownModuleWarns exists mainly to guard against the check
// being deleted — an unknown Module is a caller mistake that should
// surface (via stderr, not the JSON log stream) rather than silently
// pollute a Loki label. We can't easily assert on the stderr write from
// inside the test, so we assert the emission itself still lands
// (behavior: never DROP an emission just because vocab was wrong).
func TestEmit_UnknownModuleAndActionStillEmit(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(config.ObservabilityConfig{LogLevel: "INFO", LogFormat: "json"}, &buf, nil)

	// Simulate a caller casting a stray string into a Module + Action.
	e.Emit(context.Background(), LevelInfo, vocabulary.Module("mystery"), vocabulary.Action("bogus_action"), "still logs")

	if !strings.Contains(buf.String(), "still logs") {
		t.Errorf("emission should still land even with unknown vocab; got: %q", buf.String())
	}
	if !strings.Contains(buf.String(), `"module":"mystery"`) {
		t.Errorf("emission should carry the unknown module label; got: %q", buf.String())
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
