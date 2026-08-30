// repo.go — storage-side ports for video_assets + video_shares. The
// pg adapter implements these. Domain callers depend only on the
// interfaces.
package video

import (
	"context"
	"errors"
	"time"

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

	// AddPopularity adds n (treated as ≥1) to an existing asset's popularity —
	// the persist side of a collapse. n is the loser's accumulated vote count,
	// so a clip that itself absorbed gate md5-dups (#180) transfers all of them
	// in one write.
	AddPopularity(ctx context.Context, id uuid.UUID, n int) error

	// Supersede retires loser onto winner atomically: sets loser.superseded_by
	// = winner AND merges loser's popularity into winner in ONE statement, so a
	// retried activity can't double-count. Idempotent — a second call (loser
	// already superseded) is a no-op; loser==winner is a no-op. The loser row
	// stays (shares FK it) and resolves through the chain; it just drops out of
	// the live set (partial index WHERE superseded_by IS NULL).
	Supersede(ctx context.Context, loserID, winnerID uuid.UUID) error

	// ListUnreclaimedObjectsByEvent returns every asset whose Garage deletion is
	// not durably confirmed. It includes live and superseded assets.
	ListUnreclaimedObjectsByEvent(ctx context.Context, eventID uuid.UUID) ([]ObjectRef, error)

	// MarkObjectReclaimed records a successful idempotent Garage delete. It is
	// idempotent and preserves the first successful timestamp.
	MarkObjectReclaimed(ctx context.Context, assetID uuid.UUID) error

	// ListUnreclaimedEventIDsBefore returns event IDs with at least one
	// unreclaimed asset under a completed fixture whose UTC kickoff is before
	// cutoff. This is the asset-owned media-retention worklist.
	ListUnreclaimedEventIDsBefore(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error)
}

// ShareRepo is the storage port for video_shares.
type ShareRepo interface {
	Get(ctx context.Context, id string) (*Share, error)
	GetByEvent(ctx context.Context, eventID uuid.UUID) ([]*Share, error)

	// Insert creates a compatibility share row. The (event_id, rank) UNIQUE
	// partial index remains for pre-FF-066 histories until the stored column is
	// removed.
	Insert(ctx context.Context, s *Share) error

	// Upsert saves state changes (Remove) back to storage.
	Upsert(ctx context.Context, s *Share) error

	// RebalanceRanks is the pre-FF-066 compatibility writer. New histories use
	// PlacementRepo and public reads derive rank without calling this method.
	// It reads all active shares, sorts via CompareShares, and rewrites rank 1..N.
	// The transaction is critical — the (event_id, rank) UNIQUE index
	// only allows one rank value per event at a time in the active
	// pool, so intermediate states during the rewrite must be atomic.
	// Returns the count of shares repositioned (0 if no changes).
	RebalanceRanks(ctx context.Context, eventID uuid.UUID) (int, error)

	// MarkSuperseded flips a share to 'superseded' — it leaves the active/
	// ranked list but still resolves (via its asset's chain) for direct-URL
	// play. Distinct from Remove (VAR / event gone, which stops resolving).
	MarkSuperseded(ctx context.Context, id string) error

	// RemoveByEvent revokes ALL of an event's non-removed shares (active +
	// superseded) to state='removed' with reason — the VAR/destroy path (#172).
	// After this, ResolveShare returns 'removed' → the redirect 410s, so an
	// overturned goal's clips stop serving. Idempotent: already-removed shares
	// are untouched.
	RemoveByEvent(ctx context.Context, eventID uuid.UUID, reason RemovalReason) error
}
