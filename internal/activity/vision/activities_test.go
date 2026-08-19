// activities_test.go — unit tests for ValidateClip with injected fakes for
// ffmpeg / s3 / the LLM. No real model or containers: the domain logic has
// its own table tests; here we check the plumbing (frames extracted, the
// structured-output request shape, verdict mapping, error passthrough).
package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/ffmpeg"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
)

type fakeFFmpeg struct {
	dur       float64
	positions []float64
}

func (f *fakeFFmpeg) ProbeMetadata(_ context.Context, _ string) (*ffmpeg.VideoMetadata, error) {
	return &ffmpeg.VideoMetadata{DurationSecs: f.dur, Width: 1280, Height: 720}, nil
}
func (f *fakeFFmpeg) ExtractFrame(_ context.Context, _ string, pos float64, _ int) ([]byte, error) {
	f.positions = append(f.positions, pos)
	return []byte{0xFF, 0xD8, 0xFF, 0xE0}, nil // minimal JPEG magic
}

type fakeS3 struct{}

func (fakeS3) Download(_ context.Context, _ string) (io.ReadCloser, int64, error) {
	b := []byte("fake mp4 bytes")
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

type fakeLLM struct {
	resp    string
	err     error
	lastReq llm.ChatRequest
}

func (f *fakeLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return &llm.ChatResponse{Content: f.resp}, nil
}

func newActivities(t *testing.T, llmResp string, llmErr error) (*Activities, *fakeFFmpeg, *fakeLLM) {
	t.Helper()
	ff := &fakeFFmpeg{dur: 6.677}
	fl := &fakeLLM{resp: llmResp, err: llmErr}
	a := &Activities{
		FFmpeg:     ff,
		S3:         fakeS3{},
		LLM:        fl,
		ScratchDir: t.TempDir(),
		Cfg:        config.VisionConfig{ToleranceMinutes: 1, FrameQuality: 3, DisableThinking: true},
	}
	return a, ff, fl
}

func framesJSON(frames ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"frames": frames})
	return string(b)
}

func TestValidateClip_VerifiedAndRequestShape(t *testing.T) {
	// Lauberbach 90+2: frozen 90:00 + sub-timer → minute 91.
	resp := framesJSON(
		map[string]any{"soccer": true, "screen": false, "clock": "90:00", "period": "2H", "added": "+2", "stoppage_clock": "+1:48"},
		map[string]any{"soccer": true, "screen": false, "clock": "90:00", "period": "2H", "added": "+2", "stoppage_clock": "+1:53"},
		map[string]any{"soccer": true, "screen": false, "clock": "90:00", "period": "2H", "added": "+2", "stoppage_clock": "+1:58"},
	)
	a, ff, fl := newActivities(t, resp, nil)

	out, err := a.ValidateClip(context.Background(), ValidateClipInput{
		EventID: uuid.New(), FixtureID: 1583467, StagingKey: "staging/x.mp4",
		APIElapsed: 90, APIExtra: 2,
	})
	if err != nil {
		t.Fatalf("ValidateClip: %v", err)
	}
	if out.Outcome != "verified" {
		t.Errorf("Outcome = %q (%s), want verified", out.Outcome, out.Reason)
	}
	if out.MatchedMinute == nil || *out.MatchedMinute != 91 {
		t.Errorf("MatchedMinute = %v, want 91", out.MatchedMinute)
	}
	if len(out.Frames) != 3 {
		t.Errorf("Frames len = %d, want 3", len(out.Frames))
	}
	if len(out.ClockReadings) != 3 || out.ClockReadings[0].Period != "2H" {
		t.Errorf("ClockReadings = %+v, want three 2H readings", out.ClockReadings)
	}

	// Frames sampled at 25/50/75% of 6.677s.
	wantPos := []float64{0.25 * 6.677, 0.50 * 6.677, 0.75 * 6.677}
	if len(ff.positions) != 3 {
		t.Fatalf("extracted %d frames, want 3", len(ff.positions))
	}
	for i, w := range wantPos {
		if diff := ff.positions[i] - w; diff > 0.001 || diff < -0.001 {
			t.Errorf("frame %d position = %.3f, want %.3f", i, ff.positions[i], w)
		}
	}

	// Request carried 3 images, the schema, and thinking-off.
	if got := len(fl.lastReq.Messages[0].Images); got != 3 {
		t.Errorf("request images = %d, want 3", got)
	}
	if !fl.lastReq.DisableThinking {
		t.Error("request should DisableThinking")
	}
	if fl.lastReq.ResponseFormat == nil || fl.lastReq.ResponseFormat.JSONSchema == nil {
		t.Error("request should carry a JSONSchema response_format")
	} else if !bytes.Contains(fl.lastReq.ResponseFormat.JSONSchema.Schema, []byte(`"period"`)) {
		t.Error("response schema should require per-frame period evidence")
	}
}

func TestValidateClip_ScreenRejected(t *testing.T) {
	// Phone-of-TV: majority screen=true → rejected regardless of clock.
	resp := framesJSON(
		map[string]any{"soccer": true, "screen": true, "clock": nil, "period": nil, "added": nil, "stoppage_clock": nil},
		map[string]any{"soccer": true, "screen": true, "clock": nil, "period": nil, "added": nil, "stoppage_clock": nil},
		map[string]any{"soccer": true, "screen": true, "clock": nil, "period": nil, "added": nil, "stoppage_clock": nil},
	)
	a, _, _ := newActivities(t, resp, nil)
	out, err := a.ValidateClip(context.Background(), ValidateClipInput{StagingKey: "k", APIElapsed: 90, APIExtra: 2})
	if err != nil {
		t.Fatalf("ValidateClip: %v", err)
	}
	if out.Outcome != "rejected" {
		t.Errorf("Outcome = %q, want rejected", out.Outcome)
	}
}

func TestValidateClip_TransientModelErrorRemainsRetryable(t *testing.T) {
	a, _, _ := newActivities(t, "", llm.ErrRateLimited)
	_, err := a.ValidateClip(context.Background(), ValidateClipInput{StagingKey: "k", APIElapsed: 71})
	if !errors.Is(err, llm.ErrRateLimited) {
		t.Errorf("err = %v, want wraps ErrRateLimited (retryable)", err)
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.NonRetryable() {
		t.Fatalf("rate limit became non-retryable: %v", err)
	}
}

func TestValidateClip_PermanentModelErrorsAreNonRetryable(t *testing.T) {
	for _, modelErr := range []error{
		llm.ErrInvalidJSON,
		llm.ErrModelNotFound,
		llm.ErrInvalidRequest,
		llm.ErrAuthFailed,
	} {
		t.Run(modelErr.Error(), func(t *testing.T) {
			a, _, _ := newActivities(t, "", modelErr)
			_, err := a.ValidateClip(context.Background(), ValidateClipInput{StagingKey: "k", APIElapsed: 71})
			assertPermanentLLMError(t, err, modelErr)
		})
	}
}

func TestValidateClip_MalformedResponseIsNonRetryable(t *testing.T) {
	a, _, _ := newActivities(t, "not json", nil)
	_, err := a.ValidateClip(context.Background(), ValidateClipInput{StagingKey: "k", APIElapsed: 71})
	assertPermanentLLMError(t, err, llm.ErrInvalidJSON)
}

func assertPermanentLLMError(t *testing.T, err, sentinel error) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wraps %v", err, sentinel)
	}
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("err = %v, want Temporal ApplicationError", err)
	}
	if !appErr.NonRetryable() || appErr.Type() != permanentLLMErrorType {
		t.Fatalf("ApplicationError nonretryable/type = %v/%q, want true/%q", appErr.NonRetryable(), appErr.Type(), permanentLLMErrorType)
	}
}
