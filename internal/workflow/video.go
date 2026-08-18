// VideoWorkflow — the per-candidate download-and-hash child workflow.
//
// One instance runs per candidate video. It is the ONLY real Temporal
// parent→child in the V-phase: EventWorkflow spawns one of these per
// candidate and awaits it, so the parent gets completion tracking for free.
//
// A Temporal *workflow* is just a function that calls *activities* (the code
// that does real I/O — network, disk, DB) and records each result into a
// durable history log. If the worker crashes mid-run, Temporal replays the
// history to rebuild the function's state and resumes exactly where it left
// off — which is why workflow code must be deterministic (no wall-clock,
// no rand, no direct I/O; all side effects go through activities).
//
// This child is deliberately SEQUENTIAL — download, then hash — so there is
// no concurrency here. The concurrency (workflow.Go + Selector) lives in the
// parent EventWorkflow. Keeping the child linear means a HashVideo retry
// re-fetches the already-staged bytes from Garage (cheap, internal) rather
// than re-hitting Twitter.
//
// Outcome model mirrors the activities: a definitive "this clip is out"
// (geo-blocked / deleted / wrong shape) returns rejected. An activity failure
// that exhausts retries returns failed with its stage and any staging key so
// the parent can persist the terminal candidate outcome and reclaim bytes.
// Cancellation remains an error because it belongs to the event-removal path.
package workflow

import (
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// Timeouts + retries for the two child activities. Hardcoded (not env)
// because they bound infra behavior, not operator policy — bumping them is a
// worker-restart concern, matching the discoveryPG* constant convention.
// DownloadAndStage does an external CDN fetch of up to ~100 MB + a Garage
// upload; HashVideo does an internal Garage fetch + dense frame extraction.
const (
	videoDownloadTimeout   = 3 * time.Minute
	videoHashTimeout       = 2 * time.Minute
	videoActivityHeartbeat = 30 * time.Second
	videoDownloadRetries   = 4 // transient CDN/rate-limit; terminal rejects return nil-error
	videoHashRetries       = 3

	ff002TerminalVideoFailuresChangeID = "ff-002-terminal-video-failures"
	ff002TerminalVideoFailuresVersion  = workflow.Version(1)
)

// videoDownloadActivityContext applies the durable download/stage contract in
// both the legacy VideoWorkflow child and FF-022's parent-owned pipeline.
func videoDownloadActivityContext(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: videoDownloadTimeout,
		HeartbeatTimeout:    videoActivityHeartbeat,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    videoDownloadRetries,
		},
	})
}

// videoHashActivityContext applies the durable dense-hash contract in both
// candidate orchestration paths.
func videoHashActivityContext(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: videoHashTimeout,
		HeartbeatTimeout:    videoActivityHeartbeat,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    videoHashRetries,
		},
	})
}

// VideoWorkflowInput identifies the one candidate to process. Fields match
// DownloadAndStageInput — the child is a thin durable wrapper over the two
// activities.
type VideoWorkflowInput struct {
	EventID   uuid.UUID `json:"event_id"`
	FixtureID int64     `json:"fixture_id"`
	TweetURL  string    `json:"tweet_url"`
}

// VideoWorkflowOutcome is the terminal per-candidate result returned to the
// parent. Failed is distinct from rejected: a failed clip never received a
// content verdict because infrastructure exhausted its retries.
type VideoWorkflowOutcome string

const (
	VideoOutcomePassed   VideoWorkflowOutcome = "passed"
	VideoOutcomeRejected VideoWorkflowOutcome = "rejected"
	VideoOutcomeFailed   VideoWorkflowOutcome = "failed"
)

// VideoWorkflowFailureReason identifies the stage that exhausted retries.
// Values are stable slugs persisted in event_search_candidates.reject_reason.
type VideoWorkflowFailureReason string

const (
	VideoFailureDownload           VideoWorkflowFailureReason = "download_error"
	VideoFailureHash               VideoWorkflowFailureReason = "hash_error"
	VideoFailureUnexpectedChild    VideoWorkflowFailureReason = "video_workflow_error"
	VideoFailureInvalidChildOutput VideoWorkflowFailureReason = "video_workflow_invalid_outcome"
)

// VideoWorkflowOutput is the fingerprint bundle the parent's consumer queue
// deduplicates on. TweetURL echoes back so the parent correlates the result
// to the candidate. FrameHashes is empty when Outcome != passed.
type VideoWorkflowOutput struct {
	TweetURL      string                     `json:"tweet_url"`
	Outcome       VideoWorkflowOutcome       `json:"outcome"`
	RejectReason  string                     `json:"reject_reason,omitempty"`
	FailureReason VideoWorkflowFailureReason `json:"failure_reason,omitempty"`
	MD5           string                     `json:"md5,omitempty"`
	StagingKey    string                     `json:"staging_key,omitempty"`
	HashVersion   dvideo.FrameHashVersion    `json:"hash_version,omitempty"`
	FrameHashes   []uint64                   `json:"frame_hashes,omitempty"`

	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	DurationMS int     `json:"duration_ms,omitempty"`
	Bitrate    int     `json:"bitrate,omitempty"`
	FrameRate  float64 `json:"frame_rate,omitempty"`
	SizeBytes  int64   `json:"size_bytes,omitempty"`
}

// VideoWorkflow runs download→stage→hash for one candidate.
func VideoWorkflow(ctx workflow.Context, in VideoWorkflowInput) (VideoWorkflowOutput, error) {
	log := workflow.GetLogger(ctx)
	log.Info("VideoWorkflow started", "event_id", in.EventID, "tweet_url", in.TweetURL)

	out := VideoWorkflowOutput{TweetURL: in.TweetURL}
	terminalFailures := workflow.GetVersion(ctx,
		ff002TerminalVideoFailuresChangeID,
		workflow.DefaultVersion,
		ff002TerminalVideoFailuresVersion,
	) != workflow.DefaultVersion

	// Step 1 — download + fingerprint (md5) + probe + hard-filter + stage.
	// WithActivityOptions returns a child ctx carrying the timeout + retry
	// policy; ExecuteActivity(...).Get blocks (durably) until the activity
	// resolves, writing its result into &dlOut.
	dlCtx := videoDownloadActivityContext(ctx)
	var dlOut videoactivity.DownloadAndStageOutput
	if err := workflow.ExecuteActivity(dlCtx,
		(*videoactivity.Activities).DownloadAndStage,
		videoactivity.DownloadAndStageInput{
			EventID:   in.EventID,
			FixtureID: in.FixtureID,
			TweetURL:  in.TweetURL,
		}).Get(dlCtx, &dlOut); err != nil {
		if temporal.IsCanceledError(err) || !terminalFailures {
			return out, err
		}
		out.Outcome = VideoOutcomeFailed
		out.FailureReason = VideoFailureDownload
		log.Warn("VideoWorkflow download failed after retries",
			"tweet_url", in.TweetURL, "err", err)
		return out, nil
	}

	// Carry probed metadata regardless of outcome (useful on the candidate
	// record even for a reject).
	out.Width, out.Height = dlOut.Width, dlOut.Height
	out.DurationMS, out.Bitrate, out.FrameRate = dlOut.DurationMS, dlOut.Bitrate, dlOut.FrameRate
	out.SizeBytes, out.MD5 = dlOut.SizeBytes, dlOut.MD5

	// Terminal reject (geo / deleted / wrong shape) — no point hashing.
	if dlOut.Outcome == videoactivity.OutcomeRejected {
		out.Outcome, out.RejectReason = VideoOutcomeRejected, dlOut.RejectReason
		log.Info("VideoWorkflow candidate rejected pre-hash",
			"tweet_url", in.TweetURL, "reason", dlOut.RejectReason)
		return out, nil
	}
	out.StagingKey = dlOut.StagingKey

	// Step 2 — dense frame extraction + per-frame perceptual hash. Retries
	// re-fetch the staged bytes from Garage, never Twitter.
	hashCtx := videoHashActivityContext(ctx)
	var hashOut videoactivity.HashVideoOutput
	if err := workflow.ExecuteActivity(hashCtx,
		(*videoactivity.Activities).HashVideo,
		videoactivity.HashVideoInput{StagingKey: dlOut.StagingKey}).Get(hashCtx, &hashOut); err != nil {
		if temporal.IsCanceledError(err) || !terminalFailures {
			return out, err
		}
		out.Outcome = VideoOutcomeFailed
		out.FailureReason = VideoFailureHash
		log.Warn("VideoWorkflow hash failed after retries",
			"tweet_url", in.TweetURL, "staging_key", out.StagingKey, "err", err)
		return out, nil
	}

	if hashOut.Outcome == videoactivity.OutcomeRejected {
		out.Outcome, out.RejectReason = VideoOutcomeRejected, hashOut.RejectReason
		log.Info("VideoWorkflow candidate rejected during hash",
			"tweet_url", in.TweetURL, "reason", hashOut.RejectReason)
		return out, nil
	}
	if hashOut.Outcome != "" && hashOut.Outcome != videoactivity.OutcomePassed {
		out.Outcome = VideoOutcomeFailed
		out.FailureReason = VideoFailureHash
		log.Warn("VideoWorkflow hash returned invalid outcome",
			"tweet_url", in.TweetURL, "outcome", hashOut.Outcome)
		return out, nil
	}

	out.Outcome = VideoOutcomePassed
	out.HashVersion = dvideo.NormalizeFrameHashVersion(hashOut.HashVersion)
	out.FrameHashes = hashOut.FrameHashes
	log.Info("VideoWorkflow passed",
		"tweet_url", in.TweetURL, "staging_key", out.StagingKey,
		"frame_count", len(out.FrameHashes))
	return out, nil
}
