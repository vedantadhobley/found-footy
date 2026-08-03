// repo.go — storage-side ports for video_assets + video_shares. The
// pg adapter implements these. Domain callers depend only on the
// interfaces.
package video

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrNotFound is returned when an Asset or Share ID doesn't exist.
var ErrNotFound = errors.New("video: not found")

// AssetRepo is the storage port for video_assets.
//
// The storage layer NO LONGER arbitrates perceptual dedup (#166): the
// EventWorkflow consumer decides dedup in-memory (fuzzy sliding-window over
// the frame-hash sequence) BEFORE calling InsertAsset, so the DB only sees
// clips it has already judged unique. The old GetByPerceptualHash /
// UpsertWithHashDedup / FindNearMatches methods — which assumed the DB was
// the dedup engine — are gone.
type AssetRepo interface {
	Get(ctx context.Context, id uuid.UUID) (*Asset, error)

	// InsertAsset writes a clip the workflow has already judged unique.
	// Idempotent on the exact layer: a retry (or a genuine byte-identical
	// dupe that slipped the in-memory md5 check) must NOT create a second
	// row — implement as INSERT ... ON CONFLICT (event_id, md5) DO NOTHING.
	// Returns inserted=false when the ON CONFLICT path was taken.
	InsertAsset(ctx context.Context, a *Asset) (inserted bool, err error)

	// BumpPopularity increments popularity on an existing asset — the
	// persist side of a collapse (a candidate deduped onto an asset that
	// already has a DB row).
	BumpPopularity(ctx context.Context, id uuid.UUID) error
}

// ShareRepo is the storage port for video_shares.
type ShareRepo interface {
	Get(ctx context.Context, id string) (*Share, error)
	GetByEvent(ctx context.Context, eventID uuid.UUID) ([]*Share, error)

	// Insert creates a new share. The (event_id, rank) UNIQUE partial
	// index means rank collisions with an active share error at write
	// time — callers RebalanceRanks first if they need a specific rank
	// slot open.
	Insert(ctx context.Context, s *Share) error

	// Upsert saves state changes (Remove) back to storage.
	Upsert(ctx context.Context, s *Share) error

	// RebalanceRanks reads all active shares for the event, sorts them
	// via CompareShares, and rewrites rank 1..N in a single transaction.
	// The transaction is critical — the (event_id, rank) UNIQUE index
	// only allows one rank value per event at a time in the active
	// pool, so intermediate states during the rewrite must be atomic.
	// Returns the count of shares repositioned (0 if no changes).
	RebalanceRanks(ctx context.Context, eventID uuid.UUID) (int, error)
}
