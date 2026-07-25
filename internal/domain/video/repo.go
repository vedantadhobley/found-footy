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
type AssetRepo interface {
	Get(ctx context.Context, id uuid.UUID) (*Asset, error)

	// GetByPerceptualHash looks up an existing asset with the exact
	// perceptual hash inside an event. Used on the dedup path before
	// attempting Insert.
	GetByPerceptualHash(ctx context.Context, eventID uuid.UUID, hash []byte) (*Asset, error)

	// UpsertWithHashDedup is the audit §4 atomic dedup pattern. Attempts
	// INSERT; on unique_violation (event_id, perceptual_hash), fetches
	// the existing row and bumps its popularity. Returns:
	//   asset — the winning row (either the freshly-inserted a OR the
	//           existing one whose popularity got bumped)
	//   deduped — true if a duplicate was detected (a was NOT inserted)
	//   err   — transport / DB errors
	//
	// The Asset value pointed to by a MAY have its Popularity mutated
	// (bumped) by this method when deduped=true. Callers that need to
	// distinguish "I inserted this" from "someone else already had this"
	// should inspect deduped.
	UpsertWithHashDedup(ctx context.Context, a *Asset) (result *Asset, deduped bool, err error)

	// FindNearMatches returns assets in the same event whose
	// perceptual_hash_prefix matches a's, for the near-match backfill
	// compactor (§3 Track 3 — deferred behind embedding decision).
	FindNearMatches(ctx context.Context, eventID uuid.UUID, prefix int) ([]*Asset, error)
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
