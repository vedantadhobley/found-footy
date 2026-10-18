// Vision failure tests pin stage evidence, retry policy, and secret-free Temporal details.
package vision

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"

	"github.com/vedantadhobley/found-footy/internal/infra/ffmpeg"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
)

// TestValidateClip_FailureDetailCrossesTemporal covers every stage at its real activity boundary.
func TestValidateClip_FailureDetailCrossesTemporal(t *testing.T) {
	for _, tt := range []struct {
		name      string
		setup     func(*Activities, *fakeFFmpeg, *fakeLLM)
		stage     FailureStage
		class     FailureClass
		permanent bool
	}{
		{"scratch", func(a *Activities, _ *fakeFFmpeg, _ *fakeLLM) {
			a.ScratchDir = filepath.Join(a.ScratchDir, "missing", "parent")
		}, FailureScratch, FailureFilesystem, false},
		{"fetch", func(a *Activities, _ *fakeFFmpeg, _ *fakeLLM) { a.S3 = fakeS3{err: errors.New("SECRET signed URL")} }, FailureFetch, FailureStorage, false},
		{"probe", func(_ *Activities, f *fakeFFmpeg, _ *fakeLLM) { f.probeErr = ffmpeg.ErrProbeFailed }, FailureProbe, FailureProbeFailed, false},
		{"extract", func(_ *Activities, f *fakeFFmpeg, _ *fakeLLM) { f.extractErr = ffmpeg.ErrExtractionFailed }, FailureExtract, FailureExtractFailed, false},
		{"admission", func(_ *Activities, _ *fakeFFmpeg, l *fakeLLM) {
			l.err = errors.Join(llm.ErrLocalAdmission, context.DeadlineExceeded)
		}, FailureAdmission, FailureTimeout, false},
		{"request_timeout", func(_ *Activities, _ *fakeFFmpeg, l *fakeLLM) { l.err = context.DeadlineExceeded }, FailureRequest, FailureTimeout, false},
		{"rate_limit", func(_ *Activities, _ *fakeFFmpeg, l *fakeLLM) { l.err = llm.ErrRateLimited }, FailureRequest, FailureRateLimited, false},
		{"capacity", func(_ *Activities, _ *fakeFFmpeg, l *fakeLLM) { l.err = llm.ErrCapExceeded }, FailureRequest, FailureCapacity, false},
		{"model_missing", func(_ *Activities, _ *fakeFFmpeg, l *fakeLLM) { l.err = llm.ErrModelNotFound }, FailureRequest, FailureModelNotFound, true},
		{"auth", func(_ *Activities, _ *fakeFFmpeg, l *fakeLLM) { l.err = llm.ErrAuthFailed }, FailureRequest, FailureAuth, true},
		{"invalid_request", func(_ *Activities, _ *fakeFFmpeg, l *fakeLLM) { l.err = llm.ErrInvalidRequest }, FailureRequest, FailureInvalidRequest, true},
		{"wire_json", func(_ *Activities, _ *fakeFFmpeg, l *fakeLLM) { l.err = llm.ErrInvalidJSON }, FailureRequest, FailureInvalidJSON, true},
		{"response_parse", func(_ *Activities, _ *fakeFFmpeg, l *fakeLLM) { l.resp = "SECRET malformed response" }, FailureParse, FailureInvalidJSON, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, ff, l := newActivities(t, "", nil)
			tt.setup(a, ff, l)
			_, err := a.ValidateClip(context.Background(), ValidateClipInput{StagingKey: "SECRET staging key", APIElapsed: 30})
			if err == nil {
				t.Fatal("expected failure")
			}
			fc := temporal.GetDefaultFailureConverter()
			err = fc.FailureToError(fc.ErrorToFailure(err))
			var app *temporal.ApplicationError
			if !errors.As(err, &app) {
				t.Fatalf("expected serialized ApplicationError, got %v", err)
			}
			if app.NonRetryable() != tt.permanent {
				t.Fatalf("retry policy changed: %v", app)
			}
			wantType := FailureErrorType
			if tt.permanent {
				wantType = PermanentLLMErrorType
			}
			if app.Type() != wantType {
				t.Fatalf("type=%s, want %s", app.Type(), wantType)
			}
			var detail FailureDetail
			if err := app.Details(&detail); err != nil {
				t.Fatal(err)
			}
			if detail != (FailureDetail{Stage: tt.stage, Class: tt.class}) || !detail.Valid() {
				t.Fatalf("unexpected detail: %+v", detail)
			}
			var raw json.RawMessage
			if err := app.Details(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "SECRET") {
				t.Fatalf("raw cause escaped into details: %s", raw)
			}
		})
	}
}

// TestFailureDetailValid rejects unbounded values and falsely attributed timeout types.
func TestFailureDetailValid(t *testing.T) {
	for _, detail := range []FailureDetail{
		{}, {Stage: "raw path", Class: FailureUnknown}, {Stage: FailureActivity, Class: "raw upstream body"},
		{Stage: FailureActivity, Class: FailureTimeout, TimeoutType: "raw error"},
		{Stage: FailureRequest, Class: FailureTimeout, TimeoutType: TimeoutHeartbeat},
		{Stage: FailureActivity, Class: FailureUnknown, TimeoutType: TimeoutStartToClose},
		{Stage: FailureRequest, Class: FailureCapacity, ModelResponse: &llm.ResponseInfo{RequestID: "raw response text"}},
	} {
		if detail.Valid() {
			t.Errorf("accepted invalid detail: %+v", detail)
		}
	}
}

// TestVisionFailureGatewayRetry keeps capacity waits in Temporal and metadata across serialization.
func TestVisionFailureGatewayRetry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		delay time.Duration
	}{
		{"capacity", llm.ErrCapExceeded, 7 * time.Second},
		{"unavailable", llm.ErrUnavailable, 7 * time.Second},
		{"generic_limit", llm.ErrRateLimited, 7 * time.Second},
		{"permanent", llm.ErrInvalidRequest, 0},
		{"timeout", context.DeadlineExceeded, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := llm.ResponseInfo{RequestID: "req_abc", ErrorCode: "capacity_exceeded", StatusCode: 429, RetryAfter: 7 * time.Second}
			err := visionFailure(FailureRequest, &llm.ResponseError{Info: info, Err: tc.cause})
			converter := temporal.GetDefaultFailureConverter()
			err = converter.FailureToError(converter.ErrorToFailure(err))
			var app *temporal.ApplicationError
			if !errors.As(err, &app) || app.NextRetryDelay() != tc.delay {
				t.Fatalf("retry delay = %v, want %v", err, tc.delay)
			}
			var detail FailureDetail
			if app.Details(&detail) != nil || detail.ModelResponse == nil || *detail.ModelResponse != info || !detail.Valid() {
				t.Fatalf("lost metadata: %+v", detail)
			}
		})
	}
}
