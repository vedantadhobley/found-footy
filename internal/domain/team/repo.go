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

	// Replace atomically wipes the cache and writes the given rows.
	// Used by the refresh path so a mid-refresh crash never leaves a
	// partially-refreshed cache visible. `refreshedAt` stamps every
	// row with the same instant so OldestRefreshedAt returns a
	// consistent value across the batch.
	Replace(ctx context.Context, teams []TrackedTeam, refreshedAt time.Time) error
}
