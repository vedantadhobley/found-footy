// types.go — activity I/O for the per-candidate video pipeline.
package video

import (
	"github.com/google/uuid"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// Terminal outcome values for DownloadAndStage. A reject is a normal result
// (candidate definitively out), not an error.
const (
	OutcomePassed   = "passed"
	OutcomeRejected = "rejected"

	// RejectInsufficientHashFrames is deterministic for the staged bytes: the
	// sequence cannot satisfy the configured perceptual-match window, so the
	// activity returns an outcome instead of retrying identical work.
	RejectInsufficientHashFrames = "insufficient_hash_frames"
)

// DownloadFailureErrorType identifies a retryable DownloadAndStage failure
// whose details carry a bounded stage and class through Temporal retries.
const DownloadFailureErrorType = "video_download_failure"

// DownloadFailureStage identifies the operation that failed inside the
// otherwise-atomic DownloadAndStage activity.
type DownloadFailureStage string

const (
	DownloadFailureResolve       DownloadFailureStage = "resolve"
	DownloadFailureScratch       DownloadFailureStage = "scratch"
	DownloadFailureCDNDownload   DownloadFailureStage = "cdn_download"
	DownloadFailureProbe         DownloadFailureStage = "probe"
	DownloadFailureStagingUpload DownloadFailureStage = "staging_upload"
	DownloadFailureActivity      DownloadFailureStage = "activity"
)

// DownloadFailureClass is deliberately bounded so candidate rows, logs, and
// future metrics can aggregate it without parsing raw error strings.
type DownloadFailureClass string

const (
	DownloadFailureForbidden       DownloadFailureClass = "forbidden"
	DownloadFailureRateLimited     DownloadFailureClass = "rate_limited"
	DownloadFailureTimeout         DownloadFailureClass = "timeout"
	DownloadFailureTransport       DownloadFailureClass = "transport"
	DownloadFailureInvalidResponse DownloadFailureClass = "invalid_response"
	DownloadFailureStream          DownloadFailureClass = "stream"
	DownloadFailureFilesystem      DownloadFailureClass = "filesystem"
	DownloadFailureBinaryMissing   DownloadFailureClass = "binary_missing"
	DownloadFailureInputMissing    DownloadFailureClass = "input_missing"
	DownloadFailureProbeFailed     DownloadFailureClass = "probe_failed"
	DownloadFailureConcurrency     DownloadFailureClass = "concurrency"
	DownloadFailureStorage         DownloadFailureClass = "storage"
	DownloadFailureUnknown         DownloadFailureClass = "unknown"
)

// DownloadFailureDetail is persisted under candidate outcome_detail after
// all retries exhaust. It intentionally excludes raw errors and signed URLs.
type DownloadFailureDetail struct {
	Stage DownloadFailureStage `json:"stage"`
	Class DownloadFailureClass `json:"class"`
}

// Valid reports whether the detail contains only registered bounded values.
func (d DownloadFailureDetail) Valid() bool {
	switch d.Stage {
	case DownloadFailureResolve, DownloadFailureScratch, DownloadFailureCDNDownload,
		DownloadFailureProbe, DownloadFailureStagingUpload, DownloadFailureActivity:
	default:
		return false
	}
	switch d.Class {
	case DownloadFailureForbidden, DownloadFailureRateLimited, DownloadFailureTimeout,
		DownloadFailureTransport, DownloadFailureInvalidResponse, DownloadFailureStream,
		DownloadFailureFilesystem, DownloadFailureBinaryMissing, DownloadFailureInputMissing,
		DownloadFailureProbeFailed, DownloadFailureConcurrency, DownloadFailureStorage,
		DownloadFailureUnknown:
		return true
	default:
		return false
	}
}

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

// HashVideoOutput carries a versioned per-frame perceptual-hash sequence.
// A too-short sequence is a terminal rejected outcome, not an activity error.
type HashVideoOutput struct {
	Outcome      string
	RejectReason string
	HashVersion  dvideo.FrameHashVersion
	FrameHashes  []uint64
}
