// video_test.go — WorkflowTestSuite tests for VideoWorkflow. Activities are
// mocked (testify/mock) so the workflow runs in-process: no worker, no
// Temporal server, no Twitter/Garage/ffmpeg. Tests focus on control flow —
// happy path (download→hash→passed), terminal reject skips hashing, exhausted
// activity failures return typed terminal results, and cancellation propagates.
package workflow_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	"github.com/vedantadhobley/found-footy/internal/infra/syndication"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

func newVideoEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflow.VideoWorkflow)
	env.RegisterActivity(&videoactivity.Activities{})
	return env
}

func stdVideoInput() workflow.VideoWorkflowInput {
	return workflow.VideoWorkflowInput{
		EventID:   uuid.New(),
		FixtureID: 1583467,
		TweetURL:  "https://x.com/i/status/1234567890123456789",
	}
}

func TestVideoWorkflow_HappyPath(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newVideoEnv(&s)
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).Return(
		videoactivity.DownloadAndStageOutput{
			Outcome:    videoactivity.OutcomePassed,
			MD5:        "abc123",
			StagingKey: "staging/1583467/e/t.mp4",
			Width:      1280, Height: 720, DurationMS: 6677, FrameRate: 30,
		}, nil)
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).Return(
		videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8}}, nil)

	env.ExecuteWorkflow(workflow.VideoWorkflow, stdVideoInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}
	var out workflow.VideoWorkflowOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if out.Outcome != "passed" {
		t.Errorf("Outcome = %q, want passed", out.Outcome)
	}
	if out.StagingKey != "staging/1583467/e/t.mp4" || out.MD5 != "abc123" {
		t.Errorf("staging/md5 not carried: %+v", out)
	}
	if !reflect.DeepEqual(out.FrameHashes, []uint64{1, 2, 4, 8}) {
		t.Errorf("FrameHashes = %v, want [1 2 4 8]", out.FrameHashes)
	}
}

func TestVideoWorkflow_RejectedSkipsHash(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newVideoEnv(&s)
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).Return(
		videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomeRejected, RejectReason: "geo_restricted",
			Width: 1280, Height: 720,
		}, nil)
	// HashVideo intentionally NOT mocked: if the workflow wrongly calls it,
	// the real nil-deps activity panics → the workflow errors → this test
	// fails. So "no error + rejected" proves hashing was skipped.

	env.ExecuteWorkflow(workflow.VideoWorkflow, stdVideoInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}
	var out workflow.VideoWorkflowOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if out.Outcome != "rejected" || out.RejectReason != "geo_restricted" {
		t.Errorf("Outcome/reason = %q/%q, want rejected/geo_restricted", out.Outcome, out.RejectReason)
	}
	if len(out.FrameHashes) != 0 {
		t.Errorf("FrameHashes should be empty on reject, got %v", out.FrameHashes)
	}
}

func TestVideoWorkflow_InsufficientHashSequenceIsRejected(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newVideoEnv(&s)
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).Return(
		videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: "abc123",
			StagingKey: "staging/1583467/e/t.mp4",
		}, nil)
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).Return(
		videoactivity.HashVideoOutput{
			Outcome: videoactivity.OutcomeRejected, RejectReason: videoactivity.RejectInsufficientHashFrames,
		}, nil)

	env.ExecuteWorkflow(workflow.VideoWorkflow, stdVideoInput())

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out workflow.VideoWorkflowOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if out.Outcome != workflow.VideoOutcomeRejected || out.RejectReason != videoactivity.RejectInsufficientHashFrames {
		t.Fatalf("out = %+v, want insufficient-hash rejection", out)
	}
}

// TestVideoWorkflow_CDNForbiddenRetriesThenReturnsFailed verifies that a CDN
// 403 consumes all four activity attempts before becoming a correlated result.
func TestVideoWorkflow_CDNForbiddenRetriesThenReturnsFailed(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newVideoEnv(&s)
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).Return(
		videoactivity.DownloadAndStageOutput{}, syndication.ErrCDNForbidden)

	env.ExecuteWorkflow(workflow.VideoWorkflow, stdVideoInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error = %v, want terminal failed output", err)
	}
	var out workflow.VideoWorkflowOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if out.Outcome != workflow.VideoOutcomeFailed || out.FailureReason != workflow.VideoFailureDownload {
		t.Errorf("outcome/reason = %q/%q, want failed/download_error", out.Outcome, out.FailureReason)
	}
	if out.TweetURL != stdVideoInput().TweetURL || out.StagingKey != "" {
		t.Errorf("failed download output lost correlation or added staging: %+v", out)
	}
	env.AssertNumberOfCalls(t, "DownloadAndStage", videoDownloadAttemptsForTest)
}

// TestVideoWorkflow_HashErrorReturnsFailedWithStaging verifies that a staged
// clip remains addressable after all three hash attempts fail.
func TestVideoWorkflow_HashErrorReturnsFailedWithStaging(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newVideoEnv(&s)
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).Return(
		videoactivity.DownloadAndStageOutput{
			Outcome:    videoactivity.OutcomePassed,
			MD5:        "abc123",
			StagingKey: "staging/1583467/e/t.mp4",
			Width:      1280, Height: 720, DurationMS: 6677, SizeBytes: 900_000,
		}, nil)
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).Return(
		videoactivity.HashVideoOutput{}, errors.New("ffmpeg: extraction timeout"))

	env.ExecuteWorkflow(workflow.VideoWorkflow, stdVideoInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error = %v, want terminal failed output", err)
	}
	var out workflow.VideoWorkflowOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if out.Outcome != workflow.VideoOutcomeFailed || out.FailureReason != workflow.VideoFailureHash {
		t.Errorf("outcome/reason = %q/%q, want failed/hash_error", out.Outcome, out.FailureReason)
	}
	if out.TweetURL != stdVideoInput().TweetURL || out.StagingKey != "staging/1583467/e/t.mp4" {
		t.Errorf("failed hash output lost correlation or staging: %+v", out)
	}
	env.AssertNumberOfCalls(t, "HashVideo", videoHashAttemptsForTest)
}

// TestVideoWorkflow_CancellationRemainsError keeps event removal distinct from
// an exhausted candidate activity: cancellation must propagate to the parent.
func TestVideoWorkflow_CancellationRemainsError(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newVideoEnv(&s)
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).
		After(10*time.Minute).
		Return(videoactivity.DownloadAndStageOutput{}, nil).
		Once()
	env.RegisterDelayedCallback(env.CancelWorkflow, 30*time.Second)

	env.ExecuteWorkflow(workflow.VideoWorkflow, stdVideoInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("workflow error = %v, want canceled", err)
	}
}

// TestVideoWorkflow_DefaultVersionPreservesFailedWorkflowHistory proves an
// execution started before FF-002 retains its original replay command path.
func TestVideoWorkflow_DefaultVersionPreservesFailedWorkflowHistory(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newVideoEnv(&s)
	env.OnGetVersion(ff002ChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
		Return(sdkworkflow.DefaultVersion).
		Once()
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).Return(
		videoactivity.DownloadAndStageOutput{}, errors.New("cdn timeout"))

	env.ExecuteWorkflow(workflow.VideoWorkflow, stdVideoInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("default-version replay must preserve the historical failed workflow")
	}
}

// Mirrors the production retry constants without exporting implementation
// knobs solely for tests. These assertions catch accidental retry regression.
const (
	videoDownloadAttemptsForTest = 4
	videoHashAttemptsForTest     = 3
	ff002ChangeIDForTest         = "ff-002-terminal-video-failures"
)
