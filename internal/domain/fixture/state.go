// state.go — fixture state transition logic.
package fixture

import (
	"fmt"
	"time"

	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// Activate transitions the fixture from staging to active. Sets
// ActivatedAt to at. Idempotent: calling Activate on an already-active
// fixture is a no-op (returns nil). Errors on any other from-state.
//
// A staging fixture whose kickoff has passed (or is within the lookahead
// window) becomes active so ActivePollWorkflow refreshes it at active cadence.
func (f *Fixture) Activate(at time.Time) error {
	switch f.State {
	case StateActive:
		return nil // idempotent
	case StateStaging:
		f.State = StateActive
		utc := at.UTC()
		f.ActivatedAt = &utc
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
		f.UpdatedAt = utc
		return nil
	default:
		return fmt.Errorf("%w: cannot complete from %s", ErrInvalidStateTransition, f.State)
	}
}

// Reschedule moves an active fixture back to staging when the API
// publishes a new kickoff far in the future (typical case: a match is
// postponed and the vendor publishes a new future kickoff).
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
	f.TerminalObservedAt = nil
	f.Kickoff = newKickoff.UTC()
	utc := at.UTC()
	f.UpdatedAt = utc
	return nil
}

// UpdateFromPoll captures a fresh API poll result on an active fixture.
// Refreshes api_status_*, api_elapsed, api_extra, home/away score,
// last_polled_at, and the first terminal observation without changing state.
// A successful non-terminal observation clears TerminalObservedAt; failed or
// missing provider responses never call this method and therefore preserve it.
// State transitions happen through the dedicated methods above.
//
// It deliberately does NOT touch last_activity_at: that is the wall-clock of the
// most recent meaningful lifecycle observation or event — the frontend's
// recency sort key — not "when we last polled." The API derives the value from
// activation, terminal observation, completion, and event timestamps. A plain
// poll is not activity. See decisions.md 2026-08-14.
func (f *Fixture) UpdateFromPoll(
	status APIStatus,
	elapsed, extra *int,
	homeScore, awayScore *int,
	at time.Time,
) {
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
	f.UpdatedAt = utc
	if status.Terminal() {
		if f.TerminalObservedAt == nil {
			f.TerminalObservedAt = &utc
		}
	} else {
		f.TerminalObservedAt = nil
	}
}

// UpdateResult derives ordinary and shootout winner state from the canonical
// score fields. API-Football's teams.*.winner flags describe the current leader
// during play, so preserving a prior non-nil flag across a later tie stores a
// false result. Exceptional terminal outcomes do not have a reliable score
// contract; for those statuses only, the provider's explicit nullable flags are
// authoritative.
func (f *Fixture) UpdateResult(providerHome, providerAway *bool) {
	switch f.APIStatus.Short {
	case apifootball.StatusPenaltyDone:
		f.HomeWinner, f.AwayWinner = winnerFromScore(f.HomePenalty, f.AwayPenalty)
	case apifootball.StatusCancelled,
		apifootball.StatusAbandoned,
		apifootball.StatusTechnicalLoss,
		apifootball.StatusWalkover:
		f.HomeWinner = cloneBool(providerHome)
		f.AwayWinner = cloneBool(providerAway)
	default:
		f.HomeWinner, f.AwayWinner = winnerFromScore(f.HomeScore, f.AwayScore)
	}
}

// UpdatePenalty mirrors the nullable shootout result (api score.penalty) from
// the current poll. Clearing an absent value is intentional: winner derivation
// and PEN completion must not operate on a stale shootout from an older poll.
func (f *Fixture) UpdatePenalty(home, away *int) {
	f.HomePenalty = cloneInt(home)
	f.AwayPenalty = cloneInt(away)
}

// UpdateMetadata replaces the provider-owned fixture identity and display
// fields. Lifecycle state and observation timestamps remain unchanged.
func (f *Fixture) UpdateMetadata(kickoff time.Time, home, away Team, league League) {
	f.Kickoff = kickoff.UTC()
	f.Home = home
	f.Away = away
	f.League = league
}

// winnerFromScore returns an exact nullable winner pair. A missing or tied
// score has no winner; a decided score always yields one true and one false.
func winnerFromScore(home, away *int) (*bool, *bool) {
	if home == nil || away == nil || *home == *away {
		return nil, nil
	}
	homeWon := *home > *away
	awayWon := !homeWon
	return &homeWon, &awayWon
}

// cloneBool copies a nullable boolean so domain state never aliases a transport
// response field.
func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// cloneInt copies a nullable integer so domain state never aliases a transport
// response field.
func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// RecordStagingPoll captures the result of a passive API poll on a
// staging fixture that did NOT result in a state transition. Refreshes
// APIStatus, Kickoff (vendor sometimes publishes corrected kickoff
// times), and LastPolledAt.
//
// LastActivityAt is intentionally NOT set — a passive poll doesn't
// count as "activity" for frontend-sort purposes; only Activate/
// Complete set that field. Matches Python's semantics from
// archive/src/activities/monitor.py where `_last_activity` is only
// touched on NS/TBD → live transitions, not on plain staging polls.
//
// Callers: monitor.PollStagingFixtures activity, when the API poll
// returns a non-Live status AND the kickoff isn't in the activation
// window. If either of those triggers activation, call Activate
// instead (it sets LastActivityAt correctly).
func (f *Fixture) RecordStagingPoll(status APIStatus, kickoff time.Time, at time.Time) {
	utc := at.UTC()
	f.APIStatus = status
	f.Kickoff = kickoff.UTC()
	f.LastPolledAt = &utc
	f.UpdatedAt = utc
}
