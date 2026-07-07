// state.go — fixture state transition logic.
package fixture

import (
	"fmt"
	"time"
)

// Activate transitions the fixture from staging to active. Sets
// ActivatedAt to at. Idempotent: calling Activate on an already-active
// fixture is a no-op (returns nil). Errors on any other from-state.
//
// This mirrors the "pre-activate + activate" logic in the Python
// MonitorWorkflow — a staging fixture whose kickoff has passed (or is
// within the lookahead window) becomes active so the monitor loop
// starts polling it every 30s.
func (f *Fixture) Activate(at time.Time) error {
	switch f.State {
	case StateActive:
		return nil // idempotent
	case StateStaging:
		f.State = StateActive
		utc := at.UTC()
		f.ActivatedAt = &utc
		f.LastActivityAt = &utc
		f.UpdatedAt = utc
		return nil
	default:
		return fmt.Errorf("%w: cannot activate from %s", ErrInvalidStateTransition, f.State)
	}
}

// Complete transitions the fixture from active to completed. Sets
// CompletedAt to at. Idempotent for completed → completed. Errors on
// staging → completed (a staging fixture must activate first even if
// the API reports it as immediately terminal — this preserves the
// activated_at NOT NULL invariant on completed rows).
func (f *Fixture) Complete(at time.Time) error {
	switch f.State {
	case StateCompleted:
		return nil // idempotent
	case StateActive:
		f.State = StateCompleted
		utc := at.UTC()
		f.CompletedAt = &utc
		f.LastActivityAt = &utc
		f.UpdatedAt = utc
		return nil
	default:
		return fmt.Errorf("%w: cannot complete from %s", ErrInvalidStateTransition, f.State)
	}
}

// Reschedule moves an active fixture back to staging when the API
// publishes a new kickoff far in the future (typical case: a match is
// postponed and the new kickoff is > 24h away, per docs/todo.md
// deferred PST handling item).
//
// Clears ActivatedAt because completed_at CHECK requires the state ↔
// timestamp invariant to hold at every write. Sets a new kickoff.
// Errors on transitions from any non-active state — a staging fixture
// doesn't need to be "rescheduled", and a completed fixture cannot go
// back.
func (f *Fixture) Reschedule(newKickoff time.Time, at time.Time) error {
	if f.State != StateActive {
		return fmt.Errorf("%w: cannot reschedule from %s", ErrInvalidStateTransition, f.State)
	}
	f.State = StateStaging
	f.ActivatedAt = nil
	f.Kickoff = newKickoff.UTC()
	utc := at.UTC()
	f.LastActivityAt = &utc
	f.UpdatedAt = utc
	return nil
}

// UpdateFromPoll captures a fresh API poll result on an active fixture.
// Refreshes api_status_*, api_elapsed, api_extra, home/away score, and
// last_polled_at without changing state. State transitions happen
// through the dedicated methods above.
func (f *Fixture) UpdateFromPoll(status APIStatus, elapsed, extra *int, homeScore, awayScore *int, at time.Time) {
	utc := at.UTC()
	f.APIStatus = status
	f.APIElapsed = elapsed
	f.APIExtra = extra
	if homeScore != nil {
		f.HomeScore = homeScore
	}
	if awayScore != nil {
		f.AwayScore = awayScore
	}
	f.LastPolledAt = &utc
	f.LastActivityAt = &utc
	f.UpdatedAt = utc
}
