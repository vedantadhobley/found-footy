// state.go — event lifecycle state transitions.
package event

import (
	"errors"
	"fmt"
	"time"
)

// State transition errors.
var (
	ErrRemovedEvent           = errors.New("event: transition not valid on removed event")
	ErrAlreadyRemoved         = errors.New("event: already removed")
	ErrInvalidRemovalReason   = errors.New("event: unknown removal reason")
	ErrStateTimestampMismatch = errors.New("event: state and timestamp fields inconsistent")
)

// MarkMonitorComplete flips the legacy MonitorComplete field to true.
// Idempotent for already-complete events and invalid on removed events.
// Current production orchestration uses the downstream-workflow checklist;
// this compatibility helper has no production caller.
func (e *Event) MarkMonitorComplete(at time.Time) error {
	if e.Removed {
		return fmt.Errorf("%w: MarkMonitorComplete", ErrRemovedEvent)
	}
	if e.MonitorComplete {
		return nil
	}
	e.MonitorComplete = true
	e.UpdatedAt = at.UTC()
	return nil
}

// MarkDownloadComplete flips the legacy DownloadComplete field to true.
// Idempotent for already-complete events and invalid on removed events.
// Current production orchestration uses the downstream-workflow checklist;
// this compatibility helper has no production caller.
func (e *Event) MarkDownloadComplete(at time.Time) error {
	if e.Removed {
		return fmt.Errorf("%w: MarkDownloadComplete", ErrRemovedEvent)
	}
	if e.DownloadComplete {
		return nil
	}
	e.DownloadComplete = true
	e.UpdatedAt = at.UTC()
	return nil
}

// Remove marks the event as removed with the given reason. Idempotent
// for events already removed with the SAME reason; errors if the event
// was previously removed with a DIFFERENT reason (indicates a bug —
// something is trying to reclassify a removal).
//
// The active-poll reconciler calls this after the configured absence votes
// classify an event as removed.
func (e *Event) Remove(reason RemovalReason, at time.Time) error {
	if !reason.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidRemovalReason, reason)
	}
	if e.Removed {
		if e.RemovedReason != nil && *e.RemovedReason == reason {
			return nil // idempotent
		}
		return fmt.Errorf("%w: currently %v", ErrAlreadyRemoved, e.RemovedReason)
	}
	utc := at.UTC()
	e.Removed = true
	e.RemovedReason = &reason
	e.RemovedAt = &utc
	e.UpdatedAt = utc
	return nil
}

// UpdatePlayer refines a previously-unknown player. Does NOT touch the
// natural_key — that's immutable per row (see fixture.go docstring).
// The "unknown → known" flow at the workflow level is a remove-old +
// insert-new pattern, not an in-place natural_key update. This method
// is for the strictly less-common case where API refines the display
// name but the underlying player_id was already known.
func (e *Event) UpdatePlayer(newPlayer Player, at time.Time) {
	e.Player = newPlayer
	e.UpdatedAt = at.UTC()
}

// UpdateMinute captures a refreshed minute + stoppage-extra from a
// monitor poll. The API can adjust these as stoppage extends.
func (e *Event) UpdateMinute(minute int, extra *int, at time.Time) {
	e.Minute = minute
	e.Extra = extra
	e.UpdatedAt = at.UTC()
}

// ValidateInvariants mirrors the schema's CHECK constraint:
//
//	removed=false  → removed_reason=nil, removed_at=nil
//	removed=true   → removed_reason!=nil, removed_at!=nil
//
// Defense in depth vs Postgres.
func (e *Event) ValidateInvariants() error {
	if e.Removed {
		if e.RemovedReason == nil {
			return fmt.Errorf("%w: removed=true requires RemovedReason", ErrStateTimestampMismatch)
		}
		if e.RemovedAt == nil {
			return fmt.Errorf("%w: removed=true requires RemovedAt", ErrStateTimestampMismatch)
		}
	} else {
		if e.RemovedReason != nil || e.RemovedAt != nil {
			return fmt.Errorf("%w: removed=false but RemovedReason/RemovedAt set", ErrStateTimestampMismatch)
		}
	}
	if !e.Type.Valid() {
		return fmt.Errorf("event: invalid Type %q", e.Type)
	}
	return nil
}
