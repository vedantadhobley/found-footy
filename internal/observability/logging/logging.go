// Package logging is the structured JSON emission layer.
//
// Every log line goes through Emit(level, module, action, msg, fields...).
// Module + Action are compile-time enums from the vocabulary package —
// arbitrary strings are rejected at build time.
//
// Fields use logging.Field (a Key/Value pair), not slog.Attr — that
// keeps callers from importing log/slog and lets us evolve field
// handling (e.g. adding value validation, redaction) in one place.
//
// Handler: log/slog with a JSON handler. Level and format are configured
// via the ObservabilityConfig from internal/config.
//
// See docs/rebuild-plan.md §11 for the canonical schema (ts, level,
// module, action, msg, plus per-emission fields).
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Field is one Key/Value pair emitted alongside a log entry. Callers
// build fields via the helper constructors below (String, Int, Err, ...)
// so the log/slog dependency stays contained inside this package.
//
// Value is `any` so specialized helpers (a future FixtureID, HashBytes,
// etc.) can attach whatever type is most natural; the slog handler
// serializes via reflection.
type Field struct {
	Key   string
	Value any
}

// Emitter is the interface every package emits through. Concrete
// implementations: the real slog-backed emitter returned by New(),
// and the TestEmitter helper used by unit tests.
type Emitter interface {
	// Emit writes one structured log entry. Fields is a slice of Field
	// values built via the helper constructors (String, Int, Err, ...);
	// no need to import log/slog in caller packages.
	Emit(ctx context.Context, level Level, module vocabulary.Module, action vocabulary.Action, msg string, fields ...Field)
}

// Level is the log severity. Wraps slog.Level with type-safe conversion
// from the string values ObservabilityConfig uses.
type Level slog.Level

// Standard levels. Matches slog's built-in constants.
const (
	LevelDebug Level = Level(slog.LevelDebug)
	LevelInfo  Level = Level(slog.LevelInfo)
	LevelWarn  Level = Level(slog.LevelWarn)
	LevelError Level = Level(slog.LevelError)
)

// ParseLevel converts a config string ("DEBUG", "INFO", "WARN", "ERROR")
// to a Level. Case-insensitive. Unknown values default to INFO with a
// warning line written directly to stderr — misconfigured LOG_LEVEL
// shouldn't crash the binary.
func ParseLevel(s string) Level {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return LevelDebug
	case "INFO", "":
		return LevelInfo
	case "WARN", "WARNING":
		return LevelWarn
	case "ERROR":
		return LevelError
	default:
		// Direct stderr write bypassing our own logger — we're not up yet.
		_, _ = os.Stderr.WriteString("logging: unknown LOG_LEVEL " + s + " — defaulting to INFO\n")
		return LevelInfo
	}
}

// slogEmitter is the real Emitter. Wraps *slog.Logger to enforce
// vocabulary types + inject standard attributes on every emission.
//
// Optionally holds a reference to the metrics.Registry so every Emit
// increments the baseline log_lines_total + calls_total counters —
// the "metrics come from the same emissions as logs" principle from
// rebuild-plan.md §11. A nil registry (for standalone tests) skips
// the metric increments; log output is unaffected.
type slogEmitter struct {
	logger  *slog.Logger
	metrics *metrics.Registry
}

// New returns an Emitter configured per cfg. Writes to os.Stdout (which
// Promtail scrapes when LOKI_ENABLED is true; humans read during dev).
// If m is non-nil, every Emit also increments the baseline calls_total
// and log_lines_total counters registered on it.
func New(cfg config.ObservabilityConfig, m *metrics.Registry) Emitter {
	return newWithWriter(cfg, os.Stdout, m)
}

// newWithWriter is the injectable form used by tests. Writes go to w
// instead of os.Stdout. m may be nil for tests that don't care about
// counter side-effects.
func newWithWriter(cfg config.ObservabilityConfig, w io.Writer, m *metrics.Registry) Emitter {
	level := ParseLevel(cfg.LogLevel)
	handlerOpts := &slog.HandlerOptions{
		Level: slog.Level(level),
	}

	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(cfg.LogFormat)) {
	case "text":
		handler = slog.NewTextHandler(w, handlerOpts)
	default: // json (also the default)
		handler = slog.NewJSONHandler(w, handlerOpts)
	}

	return &slogEmitter{logger: slog.New(handler), metrics: m}
}

// Emit writes one log entry via the underlying slog.Logger.
//
// Attributes emitted on every line: module, action. The message and any
// caller-supplied Field values follow. slog handles the ts, level, and
// JSON serialization.
func (e *slogEmitter) Emit(
	ctx context.Context,
	level Level,
	module vocabulary.Module,
	action vocabulary.Action,
	msg string,
	fields ...Field,
) {
	attrs := make([]slog.Attr, 0, 2+len(fields))
	attrs = append(attrs,
		slog.String("module", string(module)),
		slog.String("action", string(action)),
	)
	for _, f := range fields {
		attrs = append(attrs, slog.Any(f.Key, f.Value))
	}
	e.logger.LogAttrs(ctx, slog.Level(level), msg, attrs...)

	// Baseline metric emission — §11 four-pillars principle: metrics
	// come from the same emissions as logs. Both counters share the
	// module label with the log line; log_lines_total keeps cardinality
	// tiny (module × level), calls_total adds action + outcome + a
	// caller-supplied error_class for failure-mode aggregation.
	if e.metrics != nil {
		e.metrics.LogLinesTotal.WithLabelValues(string(module), levelName(level)).Inc()
		outcome := outcomeFromLevel(level)
		errorClass := extractErrorClass(fields)
		e.metrics.CallsTotal.WithLabelValues(string(module), string(action), outcome, errorClass).Inc()
	}
}

// levelName returns the canonical string form of a Level for the
// log_lines_total{level} label. Matches slog's stringification.
func levelName(l Level) string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// outcomeFromLevel maps the log severity to the calls_total{outcome}
// label. WARN + ERROR count as failure; everything else counts as
// success. This matches the "structured errors = ERROR-level emission"
// convention adapters follow.
func outcomeFromLevel(l Level) string {
	if l >= LevelWarn {
		return "failure"
	}
	return "success"
}

// extractErrorClass returns the value of a field with key "error_class"
// as a string, or "" if none is present. Adapters attach this field on
// failure emissions to name the typed error class ("pg.pool_lost",
// "nats.timeout", etc.) so dashboards can slice by failure mode
// without per-error metric families.
func extractErrorClass(fields []Field) string {
	for _, f := range fields {
		if f.Key == "error_class" {
			if s, ok := f.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

// ── Field constructors ──────────────────────────────────────────
//
// Callers use these helpers to build fields:
//
//   log.Emit(ctx, LevelInfo, ModuleDeploy, ActionStartup, "worker starting",
//     logging.String("git_sha", "..."),
//     logging.Int("port", 8080),
//   )
//
// The helpers exist so callers don't need to import log/slog directly.
// They also give us a place to add value validation (e.g. FixtureID()
// could reject negative values) as invariants get formalized in later
// phases.

// String returns a string-valued Field.
func String(key, value string) Field { return Field{Key: key, Value: value} }

// Int returns an int-valued Field.
func Int(key string, value int) Field { return Field{Key: key, Value: value} }

// Int64 returns an int64-valued Field.
func Int64(key string, value int64) Field { return Field{Key: key, Value: value} }

// Float64 returns a float64-valued Field.
func Float64(key string, value float64) Field { return Field{Key: key, Value: value} }

// Bool returns a bool-valued Field.
func Bool(key string, value bool) Field { return Field{Key: key, Value: value} }

// Err returns a Field carrying an error's message. Convention: key is
// always "error". Nil errors produce an empty-string value so callers
// don't need to branch on nil at the call site.
func Err(err error) Field {
	if err == nil {
		return Field{Key: "error", Value: ""}
	}
	return Field{Key: "error", Value: err.Error()}
}
