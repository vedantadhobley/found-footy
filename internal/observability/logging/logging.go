// Package logging is the structured JSON emission layer.
//
// Every log line goes through Emit(level, module, action, msg, fields...).
// Module + Action are compile-time enums from the vocabulary package —
// arbitrary strings are rejected at build time.
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
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Emitter is the interface every package emits through. Concrete
// implementations: the real slog-backed emitter returned by New(),
// and the TestEmitter helper used by unit tests.
type Emitter interface {
	// Emit writes one structured log entry. Fields is a slice of slog
	// attributes; callers use the helper constructors below
	// (EventID(...), FixtureID(...), etc.) rather than raw slog.Any.
	Emit(ctx context.Context, level Level, module vocabulary.Module, action vocabulary.Action, msg string, fields ...slog.Attr)
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
type slogEmitter struct {
	logger *slog.Logger
}

// New returns an Emitter configured per cfg. Writes to os.Stdout
// (which Promtail scrapes when LOKI_ENABLED is true; humans read
// during dev).
func New(cfg config.ObservabilityConfig) Emitter {
	return newWithWriter(cfg, os.Stdout)
}

// newWithWriter is the injectable form used by tests. Writes go to w
// instead of os.Stdout.
func newWithWriter(cfg config.ObservabilityConfig, w io.Writer) Emitter {
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

	return &slogEmitter{logger: slog.New(handler)}
}

// Emit writes one log entry via the underlying slog.Logger.
//
// Attributes emitted on every line: module, action. The message and any
// caller-supplied fields follow. slog handles the ts, level, and JSON
// serialization.
func (e *slogEmitter) Emit(
	ctx context.Context,
	level Level,
	module vocabulary.Module,
	action vocabulary.Action,
	msg string,
	fields ...slog.Attr,
) {
	// Build attribute slice: standard fields first, caller fields after.
	// slog's LogAttrs takes []slog.Attr not variadic; consolidate.
	attrs := make([]slog.Attr, 0, 2+len(fields))
	attrs = append(attrs,
		slog.String("module", string(module)),
		slog.String("action", string(action)),
	)
	attrs = append(attrs, fields...)

	e.logger.LogAttrs(ctx, slog.Level(level), msg, attrs...)
}

// ── Field constructors ──────────────────────────────────────────
//
// Callers use these helpers to build fields:
//
//   log.Emit(ctx, LevelInfo, ModuleDeploy, ActionStartup, "worker starting",
//     logging.String("git_sha", "..."),
//     logging.String("built_at", "..."),
//   )
//
// The helpers are thin wrappers around slog.* but exist so callers
// don't need to import log/slog directly. They also give us a place to
// add value validation (e.g. FixtureID() could reject negative values)
// as invariants get formalized in later phases.

// String returns a string-valued attribute.
func String(key, value string) slog.Attr { return slog.String(key, value) }

// Int returns an int-valued attribute.
func Int(key string, value int) slog.Attr { return slog.Int(key, value) }

// Int64 returns an int64-valued attribute.
func Int64(key string, value int64) slog.Attr { return slog.Int64(key, value) }

// Float64 returns a float64-valued attribute.
func Float64(key string, value float64) slog.Attr { return slog.Float64(key, value) }

// Bool returns a bool-valued attribute.
func Bool(key string, value bool) slog.Attr { return slog.Bool(key, value) }

// Err returns an attribute carrying an error's message. Convention: key
// is always "error". Nil errors produce an empty-string value.
func Err(err error) slog.Attr {
	if err == nil {
		return slog.String("error", "")
	}
	return slog.String("error", err.Error())
}
