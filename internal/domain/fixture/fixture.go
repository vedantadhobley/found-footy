// Package fixture is the domain layer for match fixtures. Pure Go —
// no HTTP, no DB, no Temporal. The types here mirror the schema's
// fixtures table (see internal/infra/pg/schema.sql) but carry the
// invariants at the type layer where Postgres CHECK constraints
// enforce them at the storage layer.
//
// Fixture lifecycle: staging → active → completed. The Postponed edge
// case (active → staging when the API publishes a new kickoff > 24h
// away) is a valid transition; completed is terminal.
package fixture

import (
	"errors"
	"fmt"
	"time"
)

// State is the lifecycle phase of a fixture. Matches the fixture_state
// enum in the schema (staging, active, completed).
type State string

const (
	StateStaging   State = "staging"
	StateActive    State = "active"
	StateCompleted State = "completed"
)

// Valid returns whether s is one of the known states. Callers use this
// to sanity-check state values coming out of the storage layer.
func (s State) Valid() bool {
	switch s {
	case StateStaging, StateActive, StateCompleted:
		return true
	}
	return false
}

// APIStatus captures the API-reported fixture status. Kept as a small
// struct rather than a bare string so we can add helpers (Terminal,
// Live, Interrupted) without changing call sites.
type APIStatus struct {
	Short string // 'NS', '1H', 'HT', '2H', 'FT', 'PST', 'CANC', ...
	Long  string // human-readable form from the API
}

// Terminal reports whether the API-reported status is one the API
// considers final (match won't resume). Includes cancelled/awarded/
// walkover variants; does NOT include Postponed (that fixture can
// still play later).
func (a APIStatus) Terminal() bool {
	switch a.Short {
	case "FT", "AET", "PEN", "CANC", "ABD", "AWD", "WO":
		return true
	}
	return false
}

// Live reports whether the API considers the fixture currently
// worth active-cadence polling. Includes the obvious "playing" codes
// (1H/HT/2H/ET/BT/P/LIVE) AND three "not-currently-playing but might
// resume any minute" codes: SUSP (suspended), INT (interrupted), and
// PST (postponed).
//
// Matches Python's classification per archive/src/utils/fixture_status.py:
// "PST is treated as ACTIVE, not completed! Short delays (15-30 min)
//  are common and the match may still happen the same day."
//
// Consequences downstream:
//   • MonitorWorkflow polls Live()==true fixtures every 30s via the
//     batched /fixtures?ids= call. Cost of adding SUSP/INT/PST to
//     that batch is zero — one API call carries them all.
//   • A fresh fixture whose API returns any Live() code gets
//     emergency-activated by the ingest activity (already implemented
//     that way, this just widens the set of statuses that trigger it).
func (a APIStatus) Live() bool {
	switch a.Short {
	case "1H", "HT", "2H", "ET", "BT", "P", "LIVE",
		"SUSP", "INT", "PST":
		return true
	}
	return false
}

// Team is a minimal team reference — just the fields fixtures carry
// directly. Full team data lives on team_aliases and is fetched
// separately.
type Team struct {
	ID   int
	Name string
}

// League describes the competition the fixture belongs to. Season
// matters because team lineups + league IDs are season-scoped.
type League struct {
	ID     int
	Name   string
	Season int
}

// Fixture is the domain type. Field order + shape kept aligned with
// the schema for easy scanner mapping in the infra adapter layer, but
// nothing here depends on pgx or SQL — pure Go.
type Fixture struct {
	ID    int64
	State State

	APIStatus APIStatus
	APIElapsed *int // match minute; nil pre-kickoff
	APIExtra   *int // stoppage time

	Kickoff time.Time
	Home    Team
	Away    Team
	League  League

	HomeScore *int
	AwayScore *int

	ActivatedAt    *time.Time
	CompletedAt    *time.Time
	LastActivityAt *time.Time
	LastPolledAt   *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// New constructs a fresh Fixture in the staging state. Callers set only
// the fields available at ingest time; state transitions populate the
// activated_at / completed_at timestamps later.
func New(id int64, apiStatus APIStatus, kickoff time.Time, home, away Team, league League) *Fixture {
	now := time.Now().UTC()
	return &Fixture{
		ID:        id,
		State:     StateStaging,
		APIStatus: apiStatus,
		Kickoff:   kickoff,
		Home:      home,
		Away:      away,
		League:    league,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// StateInvariant errors surface violations of the state ↔ timestamp
// rules that the SQL CHECK constraint also enforces at write time.
// Catching them in Go gives friendlier errors than a Postgres constraint
// violation but they are ALSO caught by the constraint — defense in
// depth, not either/or.
var (
	ErrInvalidStateTransition = errors.New("fixture: invalid state transition")
	ErrStateTimestampMismatch = errors.New("fixture: state and timestamp fields inconsistent")
)

// ShouldActivateNow reports whether a staging fixture should be
// promoted to active because its kickoff is within window (inclusive
// on the boundary — kickoff exactly at now+window returns true).
//
// Two callers share this helper:
//
//  1. Ingest activity — at fresh-fixture upsert time. Prevents the
//     Python-era "manual ingest at 14:55 for 15:00 kickoff sits in
//     staging until the next 15-min monitor cycle, missing the opening
//     minutes" bug. If ShouldActivateNow is true, the ingest activity
//     calls Activate before the first Upsert, so the fixture never
//     lands in the staging bucket in the DB.
//
//  2. Monitor workflow's PreActivateUpcoming step — every cycle, scan
//     staging fixtures and promote any whose kickoff has crossed into
//     the window since the last check.
//
// Fixtures NOT in staging (already active or completed) return false —
// only staging is eligible for this transition. The window arg comes
// from workflow-level config; both callers should pass the same value
// (typically 30 min).
//
// A fixture whose kickoff is in the past also returns true: a delayed
// manual ingest still needs to be caught up. If the API meanwhile
// reports the match as already live, the monitor's separate
// "emergency activation" trigger (APIStatus.Live() on a staging row)
// catches that case.
func (f *Fixture) ShouldActivateNow(now time.Time, window time.Duration) bool {
	if f.State != StateStaging {
		return false
	}
	return !f.Kickoff.After(now.Add(window))
}

// ValidateInvariants checks the state ↔ timestamp rules that mirror the
// SQL CHECK constraint on the fixtures table. Returns nil when the
// fixture is consistent.
//
// The rules (from schema.sql):
//   staging   → activated_at == nil AND completed_at == nil
//   active    → activated_at != nil AND completed_at == nil
//   completed → activated_at != nil AND completed_at != nil
func (f *Fixture) ValidateInvariants() error {
	switch f.State {
	case StateStaging:
		if f.ActivatedAt != nil || f.CompletedAt != nil {
			return fmt.Errorf("%w: staging has activated_at=%v completed_at=%v",
				ErrStateTimestampMismatch, f.ActivatedAt, f.CompletedAt)
		}
	case StateActive:
		if f.ActivatedAt == nil {
			return fmt.Errorf("%w: active requires activated_at", ErrStateTimestampMismatch)
		}
		if f.CompletedAt != nil {
			return fmt.Errorf("%w: active must have completed_at=nil", ErrStateTimestampMismatch)
		}
	case StateCompleted:
		if f.ActivatedAt == nil || f.CompletedAt == nil {
			return fmt.Errorf("%w: completed requires activated_at and completed_at", ErrStateTimestampMismatch)
		}
	default:
		return fmt.Errorf("%w: unknown state %q", ErrInvalidStateTransition, f.State)
	}
	return nil
}
