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

// Repo is the storage-side port. The pg adapter implements it; test
// code can substitute an in-memory fake without touching Postgres.
//
// All methods are context-scoped. Cancellation propagates to the
// underlying pgx call.
type Repo interface {
	// Get returns the fixture by ID or ErrNotFound.
	Get(ctx context.Context, id int64) (*Fixture, error)

	// Upsert inserts or updates the fixture. Uses id as the primary key.
	// Called at ingest time (fresh fixtures land in staging) and at
	// monitor poll (existing fixtures get UpdateFromPoll'd).
	Upsert(ctx context.Context, f *Fixture) error

	// ListByState returns all fixtures currently in the given state,
	// most recently updated first. Used for callers that need the
	// full Fixture rows (e.g. building a dashboard view).
	ListByState(ctx context.Context, state State) ([]*Fixture, error)

	// ListActiveIDs returns only the IDs of active fixtures. Called by
	// MonitorWorkflow every 30s to build the batched
	// `apifootball.ListFixturesByIDs` call. Distinct from ListByState
	// because the monitor doesn't need the whole row — hitting the DB
	// for just the ID column keeps the per-cycle overhead near-zero.
	ListActiveIDs(ctx context.Context) ([]int64, error)

	// ListStagingBeforeKickoff returns staging fixtures whose kickoff
	// is before threshold. ActivePollWorkflow's PreActivateUpcoming
	// step pre-activates the returned fixtures within a lookahead
	// window (default 30 min).
	ListStagingBeforeKickoff(ctx context.Context, threshold time.Time) ([]*Fixture, error)

	// FixtureReadyToComplete returns true iff the fixture at id
	// satisfies the full completion contract per
	// docs/rebuild/proposals/completion-contract.md:
	//
	//   1. api_status_short is in the Terminal set
	//      (ft, aet, pen, canc, abd, wo, awd)
	//   2. completion_counter >= 3 OR HasDecidedWinner
	//      (home_winner or away_winner is non-null)
	//   3. Every non-removed event has downstream_triggered=true
	//      (debounce settled — no events in flight)
	//   4. No rows in event_downstream_workflows where
	//      completed_at IS NULL for any event in this fixture
	//      (no downstream workflows still writing)
	//
	// Returns ErrNotFound if the fixture id doesn't exist. Cheap by
	// design (partial index on event_downstream_workflows_pending +
	// early-exit CHECK constraints); intended to be called from
	// ReconcileFixture at the end of each per-fixture ActivePoll pass.
	FixtureReadyToComplete(ctx context.Context, id int64) (bool, error)

	// PruneCompleted deletes completed fixtures older than threshold
	// that have NO surviving video_shares. The RESTRICT chain
	// (video_shares → events → fixtures) enforces this at the DB layer;
	// this method is the retention job's entry point.
	// Returns count of rows deleted.
	PruneCompleted(ctx context.Context, threshold time.Time) (int, error)
}
