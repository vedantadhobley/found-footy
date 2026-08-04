// state.go — event lifecycle state transitions.
package event

import (
	"errors"
	"fmt"
	"time"
)

// State transition errors.
var (
	ErrRemovedEvent            = errors.New("event: transition not valid on removed event")
	ErrAlreadyRemoved          = errors.New("event: already removed")
	ErrInvalidRemovalReason    = errors.New("event: unknown removal reason")
	ErrStateTimestampMismatch  = errors.New("event: state and timestamp fields inconsistent")
)

// MarkMonitorComplete flips MonitorComplete to true. Idempotent for
// already-complete events. Errors on removed events (a VAR'd event
// doesn't advance).
//
// Called at the START of EventWorkflow — proves the 3-poll debounce
// was reached AND that discovery is now the owner of the event's video
// search. Setting it here rather than in MonitorWorkflow means a failed
// Discovery spawn leaves the flag false + the next monitor cycle
// retries cleanly. This is the Python-era pattern carried over.
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

// MarkDownloadComplete flips DownloadComplete to true. Idempotent for
// already-complete events. Errors on removed events.
//
// Set when the 10-download threshold is reached (all discovery attempts
// have registered + finished). The count check happens at the storage
// layer (SELECT count(*) FROM event_download_workflows WHERE event_id
// = ? >= 10); this domain method only records the fact.
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
// The typical caller is the MonitorWorkflow when it detects VAR
// cancellation via the event dropping out of the API poll for 3
// consecutive cycles.
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
//   removed=false  → removed_reason=nil, removed_at=nil
//   removed=true   → removed_reason!=nil, removed_at!=nil
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
