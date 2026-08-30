// repo.go — the storage-side port. Infra (internal/infra/pg/fixture_repo.go)
// implements this; domain callers depend only on the interface.
package fixture

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Repo.Get when the fixture ID doesn't exist.
// Callers use errors.Is(err, fixture.ErrNotFound) to distinguish this
// from transport/DB errors.
var ErrNotFound = errors.New("fixture: not found")

// CompletionAssessment is the storage-backed answer to one active fixture's
// completion check. DurableScoreEventParity is nil for exceptional terminal
// outcomes, whose aggregate score is not authoritative; otherwise it records
// audit evidence without gating completion.
type CompletionAssessment struct {
	Ready                   bool
	DurableScoreEventParity *bool
}

// Repo is the storage-side port. The pg adapter implements it; test
// code can substitute an in-memory fake without touching Postgres.
//
// All methods are context-scoped. Cancellation propagates to the
// underlying pgx call.
type Repo interface {
	// Get returns the fixture by ID or ErrNotFound.
	Get(ctx context.Context, id int64) (*Fixture, error)

	// StoreFromIngest inserts a fixture when it is new. On conflict it applies
	// the provider snapshot only when last_polled_at is newer than the stored
	// snapshot, without changing lifecycle state or transition timestamps. The
	// returned state is the authoritative stored state after the write.
	StoreFromIngest(ctx context.Context, f *Fixture) (State, error)

	// RefreshActivePoll persists only the provider and terminal-observation
	// fields owned by active reconciliation. It returns false when the row no
	// longer exists in active state or the response is older than the stored
	// provider snapshot.
	RefreshActivePoll(ctx context.Context, f *Fixture) (bool, error)

	// RefreshStagingPoll persists only the status, kickoff, and poll timestamp
	// owned by passive staging reconciliation. It returns false when the row no
	// longer exists in staging state.
	RefreshStagingPoll(ctx context.Context, f *Fixture) (bool, error)

	// ListByState returns all fixtures currently in the given state,
	// most recently updated first. Used for callers that need the
	// full Fixture rows (e.g. building a dashboard view).
	ListByState(ctx context.Context, state State) ([]*Fixture, error)

	// ListActiveIDs returns only the IDs of active fixtures. Called by
	// ActivePollWorkflow at active cadence to build the batched
	// `apifootball.ListFixturesByIDs` call. Distinct from ListByState
	// because the monitor doesn't need the whole row — hitting the DB
	// for just the ID column keeps the per-cycle overhead near-zero.
	ListActiveIDs(ctx context.Context) ([]int64, error)

	// ListStagingBeforeKickoff returns staging fixtures whose kickoff
	// is before threshold. ActivePollWorkflow's PreActivateUpcoming
	// step pre-activates the returned fixtures within a lookahead
	// window (default 30 min).
	ListStagingBeforeKickoff(ctx context.Context, threshold time.Time) ([]*Fixture, error)

	// AssessCompletion returns the current completion decision for id
	// satisfies the full completion contract per
	// docs/decisions/2026-08-25-terminal-observation-grace-bounds-completion.md:
	//
	//   1. api_status_short is in the Terminal set
	//      (ft, aet, pen, canc, abd, wo, awd)
	//   2. terminal_observed_at is at or before terminalBefore
	//   3. Every non-removed named event has settled its debounce
	//      (debounce settled — no events in flight)
	//   4. No rows in event_downstream_workflows where
	//      completed_at IS NULL for any event in this fixture
	//      (no downstream workflows still writing)
	//
	// Score/event parity and PEN decision state are audit evidence, not
	// permanent completion gates. The grace interval bounds upstream lateness.
	//
	// Returns ErrNotFound if the fixture id doesn't exist. Cheap by
	// design (partial index on event_downstream_workflows_pending +
	// early-exit CHECK constraints); intended to be called from
	// ReconcileFixture at the end of each per-fixture ActivePoll pass.
	AssessCompletion(ctx context.Context, id int64, terminalBefore time.Time) (CompletionAssessment, error)
}
