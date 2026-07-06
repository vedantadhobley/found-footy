package logging

import (
	"context"
	"log/slog"
	"sync"

	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// TestEmitter captures emissions in memory instead of writing them
// anywhere. Downstream tests use it to assert "did activity X emit
// action Y with fixture_id=Z" without touching the file system.
//
// Safe for concurrent use — the internal slice is protected by a mutex.
type TestEmitter struct {
	mu       sync.Mutex
	Captured []CapturedEntry
}

// CapturedEntry is one recorded emission. Fields is the raw slog.Attr
// slice as-passed; test helpers can walk it.
type CapturedEntry struct {
	Level  Level
	Module vocabulary.Module
	Action vocabulary.Action
	Msg    string
	Fields []slog.Attr
}

// Emit implements Emitter. Appends to Captured under mu.
func (t *TestEmitter) Emit(
	_ context.Context,
	level Level,
	module vocabulary.Module,
	action vocabulary.Action,
	msg string,
	fields ...slog.Attr,
) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Captured = append(t.Captured, CapturedEntry{
		Level:  level,
		Module: module,
		Action: action,
		Msg:    msg,
		Fields: fields,
	})
}

// Snapshot returns a copy of Captured so callers can inspect without
// worrying about further emissions mutating it.
func (t *TestEmitter) Snapshot() []CapturedEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]CapturedEntry, len(t.Captured))
	copy(out, t.Captured)
	return out
}

// Reset clears Captured. Useful between test cases sharing an emitter.
func (t *TestEmitter) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Captured = nil
}

// HasAction returns true if any captured entry matches the given
// module+action. Common assertion in workflow + activity tests.
func (t *TestEmitter) HasAction(module vocabulary.Module, action vocabulary.Action) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.Captured {
		if e.Module == module && e.Action == action {
			return true
		}
	}
	return false
}
