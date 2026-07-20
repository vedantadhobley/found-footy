// repo.go — storage port for team_aliases. The pg adapter implements
// this. Domain callers depend only on the interface.
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
	// the returned map — no error. Used by Ingest's pre-caching step to
	// skip teams whose aliases already exist.
	BulkGet(ctx context.Context, ids []int) (map[int]*TeamAlias, error)

	// UpsertVendorFields writes ONLY the phase-1 vendor fields
	// (canonical_name / team_code / country / city / is_national).
	// If the row exists, phase-2 fields (wikidata_qid / aliases /
	// resolved_at) are preserved. Used by Ingest's daily refresh —
	// vendor data changes (team rename, venue move) but resolution
	// data shouldn't get wiped.
	UpsertVendorFields(ctx context.Context, t *TeamAlias) error

	// UpsertResolution writes a full row including phase-2 fields.
	// Used by the alias resolution activity after SetResolution has
	// populated wikidata_qid + aliases + resolved_at.
	UpsertResolution(ctx context.Context, t *TeamAlias) error
}
