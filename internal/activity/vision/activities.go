// activities.go — the V-phase clip-validation activity (rung V/4).
//
//	ValidateClip — fetch the cluster's staged clip from Garage → extract
//	  frames at 25/50/75% → ONE multi-image structured-output vision call
//	  (soccer/screen gate + clock OCR) → domain Evaluate against the API
//	  minute → verdict (verified / unverified / rejected).
//
// Runs once per perceptual cluster (after dedup), so it costs one vision
// call per unique clip, not per candidate — the model node is the throughput
// bottleneck. The verdict is a nil-error OUTCOME (verified/unverified/
// rejected all route the clip to a pool); only infra + model-call failures
// are errors the workflow's RetryPolicy bounds. Model config + prompt were
// validated on real prod clips in the 2026-07-28 bake-off (see vision.md).
package vision

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vedantadhobley/found-footy/internal/activity/heartbeat"
	"github.com/vedantadhobley/found-footy/internal/config"
	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
	"github.com/vedantadhobley/found-footy/internal/infra/ffmpeg"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
)

// framePositions samples the clip at these fractions of its duration. The
// schema requires exactly 3; more frames = more chances the clock is visible
// (it hides during replays/close-ups) without extra model calls.
var framePositions = []float64{0.25, 0.50, 0.75}

// --- dep interfaces (interface-shaped so tests inject fakes) ---

type ffmpegClient interface {
	ProbeMetadata(ctx context.Context, path string) (*ffmpeg.VideoMetadata, error)
	ExtractFrame(ctx context.Context, path string, positionSecs float64, quality int) ([]byte, error)
}

type s3Client interface {
	Download(ctx context.Context, key string) (io.ReadCloser, int64, error)
}

// visionLLM is the shared LLM client, narrowed to the one method we use.
type visionLLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
}

// Activities bundles the vision activity + its deps + tuning.
type Activities struct {
	FFmpeg ffmpegClient
	S3     s3Client
	LLM    visionLLM

	ScratchDir string
	Cfg        config.VisionConfig
}

// ValidateClip validates one staged clip. Infra/model failures return an
// error (retryable); the verdict itself is always a nil-error Outcome.
func (a *Activities) ValidateClip(ctx context.Context, in ValidateClipInput) (ValidateClipOutput, error) {
	var out ValidateClipOutput
	// One opaque VLM request that can queue behind the joi cap-4 semaphore past
	// the HeartbeatTimeout; keep the attempt alive (#184 audit P0-1).
	defer heartbeat.Keepalive(ctx, heartbeat.Interval)()

	dir, err := os.MkdirTemp(a.ScratchDir, "vision-*")
	if err != nil {
		return out, fmt.Errorf("vision.ValidateClip: scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	vidPath := filepath.Join(dir, "clip.mp4")
	if err := a.fetchStaged(ctx, in.StagingKey, vidPath); err != nil {
		return out, fmt.Errorf("vision.ValidateClip: fetch %s: %w", in.StagingKey, err)
	}

	meta, err := a.FFmpeg.ProbeMetadata(ctx, vidPath)
	if err != nil {
		return out, fmt.Errorf("vision.ValidateClip: probe: %w", err)
	}

	images := make([]llm.ChatImage, 0, len(framePositions))
	for _, frac := range framePositions {
		jpeg, err := a.FFmpeg.ExtractFrame(ctx, vidPath, frac*meta.DurationSecs, a.Cfg.FrameQuality)
		if err != nil {
			return out, fmt.Errorf("vision.ValidateClip: extract @%.2f: %w", frac, err)
		}
		images = append(images, llm.ChatImage{Data: jpeg, MimeType: "image/jpeg"})
	}

	resp, err := a.callModel(ctx, images)
	if err != nil {
		// llm typed errors (rate-limit/unavailable/etc.) flow up for the
		// workflow's RetryPolicy — no outcome/error conflation here.
		return out, fmt.Errorf("vision.ValidateClip: model: %w", err)
	}

	var vr dvision.VisionResponse
	if err := json.Unmarshal([]byte(resp.Content), &vr); err != nil {
		// Constrained decoding should make this impossible; if it happens
		// it's a real backend problem worth retrying.
		return out, fmt.Errorf("vision.ValidateClip: parse response %q: %w", resp.Content, err)
	}

	ev := dvision.Evaluate(vr.Frames, dvision.Expected{Elapsed: in.APIElapsed, Extra: in.APIExtra}, a.Cfg.ToleranceMinutes)
	out.Outcome = string(ev.Outcome)
	out.MatchedMinute = ev.MatchedMinute
	out.Reason = ev.Reason
	out.SoccerVotes, out.ScreenVotes = ev.SoccerVotes, ev.ScreenVotes
	out.DetectedMinute, out.DetectedPeriod = ev.DetectedMinute, ev.DetectedPeriod
	out.ExpectedMinute, out.ExpectedPeriod = ev.ExpectedMinute, ev.ExpectedPeriod
	out.Frames = vr.Frames
	return out, nil
}

// callModel issues the single multi-image structured-output vision call.
func (a *Activities) callModel(ctx context.Context, images []llm.ChatImage) (*llm.ChatResponse, error) {
	prompt := a.Cfg.Prompt
	if prompt == "" {
		prompt = dvision.DefaultPrompt
	}
	temp := a.Cfg.Temperature
	return a.LLM.Chat(ctx, llm.ChatRequest{
		Messages: []llm.ChatMessage{{
			Role:    llm.RoleUser,
			Content: prompt,
			Images:  images,
		}},
		Temperature:     &temp,
		DisableThinking: a.Cfg.DisableThinking,
		ResponseFormat: &llm.ResponseFormat{
			JSONSchema: &llm.JSONSchema{
				Name:   "frame_validation",
				Schema: dvision.ResponseSchema,
				Strict: true,
			},
		},
	})
}

// fetchStaged streams the staged clip from Garage into dstPath.
func (a *Activities) fetchStaged(ctx context.Context, key, dstPath string) error {
	rc, _, err := a.S3.Download(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	f, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
