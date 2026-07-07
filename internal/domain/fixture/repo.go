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
	// most recently updated first. The API-Football monitor loop calls
	// this with StateActive every 30s to pull the poll set.
	ListByState(ctx context.Context, state State) ([]*Fixture, error)

	// ListStagingBeforeKickoff returns staging fixtures whose kickoff
	// is before threshold. The monitor loop pre-activates the returned
	// fixtures within a lookahead window (default 30 min).
	ListStagingBeforeKickoff(ctx context.Context, threshold time.Time) ([]*Fixture, error)

	// PruneCompleted deletes completed fixtures older than threshold
	// that have NO surviving video_shares. The RESTRICT chain
	// (video_shares → events → fixtures) enforces this at the DB layer;
	// this method is the retention job's entry point.
	// Returns count of rows deleted.
	PruneCompleted(ctx context.Context, threshold time.Time) (int, error)
}
