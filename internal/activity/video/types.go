// types.go — activity I/O for the per-candidate video pipeline.
package video

import "github.com/google/uuid"

// Terminal outcome values for DownloadAndStage. A reject is a normal result
// (candidate definitively out), not an error.
const (
	OutcomePassed   = "passed"
	OutcomeRejected = "rejected"
)

// DownloadAndStageInput identifies one candidate to fetch + fingerprint.
type DownloadAndStageInput struct {
	EventID   uuid.UUID
	FixtureID int64
	TweetURL  string
}

// DownloadAndStageOutput is the per-candidate result: a terminal outcome
// plus (when passed) the md5, the Garage staging pointer, and probed
// metadata. Video bytes never travel in the payload — only this summary.
type DownloadAndStageOutput struct {
	Outcome      string
	RejectReason string // set when Outcome == OutcomeRejected (greppable slug)
	MD5          string // hex, of the raw download (empty if rejected pre-download)
	StagingKey   string // garage key (empty unless passed)
	Width        int
	Height       int
	DurationMS   int
	Bitrate      int
	FrameRate    float64
	SizeBytes    int64
}

// HashVideoInput points at a staged clip in Garage.
type HashVideoInput struct {
	StagingKey string
}

// HashVideoOutput carries the per-frame perceptual-hash sequence (uniform
// FrameIntervalSecs spacing; index i == time i*interval).
type HashVideoOutput struct {
	FrameHashes []uint64
}
