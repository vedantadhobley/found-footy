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
// candidate cluster. ObservedAssetID is the exact MD5 variant the sources
// carried. Winner is non-nil when that variant becomes a new public root;
// Variant is non-nil when it is retained as a new superseded node. Otherwise
// WinnerAssetID identifies an existing durable winner and the observed node
// already exists. Compatibility placements may omit observed identity.
type ClipPlacement struct {
	EventID         uuid.UUID
	FixtureID       int64
	ObservedAssetID uuid.UUID
	WinnerAssetID   uuid.UUID
	Winner          *Asset
	Variant         *Asset
	Verified        bool
	ExtractedMinute *int
	LoserAssetIDs   []uuid.UUID
	Candidates      []PlacementCandidate
	CommittedAt     time.Time
}

// ClipPlacementResult reports either the canonical winner and retained
// observation or that event removal made the accepted cluster non-public. A
// committed retry returns the same winner/share and remains announceable.
type ClipPlacementResult struct {
	WinnerAssetID        uuid.UUID
	ShareID              string
	WinnerCreated        bool
	ObservedAssetCreated bool
	EventRemoved         bool
}

// PlacementRejectEventRemoved is the terminal candidate reason used when VAR
// removal wins the event-row lock before an accepted clip can commit.
const PlacementRejectEventRemoved = "event_removed"

// PlacementRepo atomically owns every database mutation caused by an accepted
// candidate: candidate terminal state, observed-variant attribution,
// popularity credit, asset/share mint, and optional supersession. S3
// copy/staging cleanup remains activity-owned.
type PlacementRepo interface {
	CommitClipPlacement(ctx context.Context, placement ClipPlacement) (ClipPlacementResult, error)
}
