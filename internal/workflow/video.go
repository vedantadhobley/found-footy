// VideoWorkflow — the per-candidate child workflow (V-phase rung #165).
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
// (geo-blocked / deleted / wrong shape) comes back as a nil-error Output
// with Outcome=rejected; only a transient failure that exhausts retries
// surfaces as an error, which the parent's Selector callback turns into an
// inFlight decrement.
package workflow

import (
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
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
)

// VideoWorkflowInput identifies the one candidate to process. Fields match
// DownloadAndStageInput — the child is a thin durable wrapper over the two
// activities.
type VideoWorkflowInput struct {
	EventID   uuid.UUID `json:"event_id"`
	FixtureID int64     `json:"fixture_id"`
	TweetURL  string    `json:"tweet_url"`
}

// VideoWorkflowOutput is the fingerprint bundle the parent's consumer queue
// deduplicates on. TweetURL echoes back so the parent correlates the result
// to the candidate. FrameHashes is empty when Outcome != passed.
type VideoWorkflowOutput struct {
	TweetURL     string   `json:"tweet_url"`
	Outcome      string   `json:"outcome"` // "passed" | "rejected"
	RejectReason string   `json:"reject_reason,omitempty"`
	MD5          string   `json:"md5,omitempty"`
	StagingKey   string   `json:"staging_key,omitempty"`
	FrameHashes  []uint64 `json:"frame_hashes,omitempty"`

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

	// Step 1 — download + fingerprint (md5) + probe + hard-filter + stage.
	// WithActivityOptions returns a child ctx carrying the timeout + retry
	// policy; ExecuteActivity(...).Get blocks (durably) until the activity
	// resolves, writing its result into &dlOut.
	dlCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: videoDownloadTimeout,
		HeartbeatTimeout:    videoActivityHeartbeat,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    videoDownloadRetries,
		},
	})
	var dlOut videoactivity.DownloadAndStageOutput
	if err := workflow.ExecuteActivity(dlCtx,
		(*videoactivity.Activities).DownloadAndStage,
		videoactivity.DownloadAndStageInput{
			EventID:   in.EventID,
			FixtureID: in.FixtureID,
			TweetURL:  in.TweetURL,
		}).Get(dlCtx, &dlOut); err != nil {
		// Transient failure that exhausted retries — surface as a workflow
		// error; the parent decrements inFlight on the child future error.
		return out, err
	}

	// Carry probed metadata regardless of outcome (useful on the candidate
	// record even for a reject).
	out.Width, out.Height = dlOut.Width, dlOut.Height
	out.DurationMS, out.Bitrate, out.FrameRate = dlOut.DurationMS, dlOut.Bitrate, dlOut.FrameRate
	out.SizeBytes, out.MD5 = dlOut.SizeBytes, dlOut.MD5

	// Terminal reject (geo / deleted / wrong shape) — no point hashing.
	if dlOut.Outcome == videoactivity.OutcomeRejected {
		out.Outcome, out.RejectReason = "rejected", dlOut.RejectReason
		log.Info("VideoWorkflow candidate rejected pre-hash",
			"tweet_url", in.TweetURL, "reason", dlOut.RejectReason)
		return out, nil
	}
	out.StagingKey = dlOut.StagingKey

	// Step 2 — dense frame extraction + per-frame perceptual hash. Retries
	// re-fetch the staged bytes from Garage, never Twitter.
	hashCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: videoHashTimeout,
		HeartbeatTimeout:    videoActivityHeartbeat,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    videoHashRetries,
		},
	})
	var hashOut videoactivity.HashVideoOutput
	if err := workflow.ExecuteActivity(hashCtx,
		(*videoactivity.Activities).HashVideo,
		videoactivity.HashVideoInput{StagingKey: dlOut.StagingKey}).Get(hashCtx, &hashOut); err != nil {
		return out, err
	}

	out.Outcome = "passed"
	out.FrameHashes = hashOut.FrameHashes
	log.Info("VideoWorkflow passed",
		"tweet_url", in.TweetURL, "staging_key", out.StagingKey,
		"frame_count", len(out.FrameHashes))
	return out, nil
}
