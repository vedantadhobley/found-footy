// repo.go — storage-side port. The pg adapter implements this.
// Domain callers depend only on the interface.
package team

import (
	"context"
	"time"
)

// Repo is the storage port for the tracked-teams cache. Concrete
// implementation lives in internal/infra/pg.
type Repo interface {
	// List returns every tracked team currently in the cache. Ordering
	// is unspecified. Used by the ingest activity to build the filter
	// Set for a fetch cycle. Empty result is not an error.
	List(ctx context.Context) ([]TrackedTeam, error)

	// OldestRefreshedAt returns the earliest refreshed_at in the cache,
	// or a zero time.Time (with ok=false) if the cache is empty. Ingest
	// compares this against config.TopFlightCacheHours to decide
	// refresh-vs-cache-hit.
	OldestRefreshedAt(ctx context.Context) (t time.Time, ok bool, err error)

	// Replace atomically wipes the cache and writes the given rows in a
	// single transaction, so a mid-refresh crash never leaves a
	// partially-refreshed cache visible. Each row carries its own
	// RefreshedAt (not one instant for the batch): the refresh path
	// carries forward prior rows for leagues it couldn't refresh this run,
	// and those must keep their ORIGINAL timestamp so OldestRefreshedAt
	// stays stale and they get retried. See ingest.RefreshTrackedTeamsIfStale
	// + decisions.md 2026-08-13 (audit P1-1).
	Replace(ctx context.Context, teams []TrackedTeam) error
}
