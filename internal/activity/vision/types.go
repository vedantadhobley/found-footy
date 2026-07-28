// types.go — activity I/O for the V-phase clip-validation activity.
package vision

import (
	"github.com/google/uuid"

	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// ValidateClipInput points at ONE staged clip (once per perceptual cluster,
// after dedup) plus the API-reported goal time to validate its clock
// against. StagingKey is the Garage key produced by the video pipeline.
type ValidateClipInput struct {
	EventID    uuid.UUID
	FixtureID  int64
	StagingKey string
	APIElapsed int // fixture event time.elapsed
	APIExtra   int // fixture event time.extra (0 if not stoppage)
}

// ValidateClipOutput is the verdict for the cluster's representative clip.
// Outcome is the domain Outcome as a string (crosses the Temporal boundary);
// Frames carries the raw per-frame model observations so the workflow can
// persist them on the candidate record for post-hoc query tuning.
type ValidateClipOutput struct {
	Outcome       string // "verified" | "unverified" | "rejected"
	MatchedMinute *int   // set only when verified
	Reason        string
	SoccerVotes   int
	ScreenVotes   int
	Frames        []dvision.FrameObservation
}
