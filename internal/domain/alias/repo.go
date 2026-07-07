// repo.go — storage-side port. The pg adapter implements this. Domain
// callers depend only on the interface.
package alias

import (
	"context"
	"errors"
)

// ErrNotFound is returned by Get / BulkGet when a team ID isn't in the
// alias cache.
var ErrNotFound = errors.New("alias: not found")

// Repo is the storage port. Team ID is the natural primary key; no
// separate UUID needed (unlike event/video which are internal-facing).
type Repo interface {
	// Get returns the alias by team ID or ErrNotFound.
	Get(ctx context.Context, teamID int) (*TeamAlias, error)

	// BulkGet returns a map of team_id → *TeamAlias for every team_id
	// in ids that has a cached entry. Missing IDs are simply absent from
	// the returned map — no error. Used by the ingest activity's
	// pre-caching step to skip teams whose aliases are already resolved.
	BulkGet(ctx context.Context, ids []int) (map[int]*TeamAlias, error)

	// Upsert inserts or updates by team_id primary key. Used by the
	// alias resolution activity at both the "we just discovered this
	// team via ingest" moment (Wikidata + LLM fields still nil) AND
	// after each stage of the RAG pipeline populates its results.
	Upsert(ctx context.Context, t *TeamAlias) error
}
