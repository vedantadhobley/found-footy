// types.go — activity I/O for clip validation.
package vision

import (
	"github.com/google/uuid"

	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// ValidateClipInput points at one hash-successful exact-MD5 claimant plus the
// API-reported goal time to validate its clock against. StagingKey is the
// Garage key produced by the video pipeline. Category-scoped perceptual dedup
// runs after this activity supplies the verification category.
type ValidateClipInput struct {
	EventID    uuid.UUID
	FixtureID  int64
	StagingKey string
	APIElapsed int // fixture event time.elapsed
	APIExtra   int // fixture event time.extra (0 if not stoppage)
}

// ValidateClipOutput is the verdict for one staged clip. Outcome is the domain
// Outcome as a string (crosses the Temporal boundary). Frames carries the raw
// model observations and ClockReadings carries their normalized timing
// interpretation so the workflow can persist both for post-hoc diagnosis.
type ValidateClipOutput struct {
	Outcome       string // "verified" | "unverified" | "rejected"
	MatchedMinute *int   // set only when verified
	Reason        string
	SoccerVotes   int
	ScreenVotes   int
	// DetectedMinute/Period is the clock the OCR read (nil if none), carried
	// through even on a clock-reject so the candidate record is triageable
	// (#181); ExpectedMinute/Period is what it was checked against.
	DetectedMinute *int
	DetectedPeriod string
	ExpectedMinute int
	ExpectedPeriod string
	Frames         []dvision.FrameObservation
	ClockReadings  []dvision.ClockReading
}
