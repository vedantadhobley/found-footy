// Placement contracts make one accepted candidate cluster a single durable
// database mutation across assets, shares, attribution, and supersession.
package video

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
)

// PlacementCandidate is one source sighting whose terminal result and asset
// attribution commit with the public clip mutation it caused.
type PlacementCandidate struct {
	Evidence discoverycontract.CandidateEvidence
	Outcome  discoverycontract.CandidateOutcome
	Detail   json.RawMessage
}

// ClipPlacement describes the complete database-side result of one accepted
// candidate cluster. Winner is non-nil when this placement introduces a new
// asset; otherwise WinnerAssetID identifies an existing durable winner.
type ClipPlacement struct {
	EventID         uuid.UUID
	FixtureID       int64
	WinnerAssetID   uuid.UUID
	Winner          *Asset
	Verified        bool
	ExtractedMinute *int
	LoserAssetIDs   []uuid.UUID
	Candidates      []PlacementCandidate
	CommittedAt     time.Time
}

// ClipPlacementResult reports either the canonical winner and cleanup work or
// that event removal made the accepted cluster non-public. A committed retry
// returns the same winner/share and remains announceable.
type ClipPlacementResult struct {
	WinnerAssetID uuid.UUID
	ShareID       string
	WinnerCreated bool
	LoserObjects  []ObjectRef
	EventRemoved  bool
}

// PlacementRejectEventRemoved is the terminal candidate reason used when VAR
// removal wins the event-row lock before an accepted clip can commit.
const PlacementRejectEventRemoved = "event_removed"

// PlacementRepo atomically owns every database mutation caused by an accepted
// candidate: candidate terminal state, popularity credit, asset/share mint,
// and optional loser supersession. S3 copy/cleanup remains activity-owned.
type PlacementRepo interface {
	CommitClipPlacement(ctx context.Context, placement ClipPlacement) (ClipPlacementResult, error)
}
