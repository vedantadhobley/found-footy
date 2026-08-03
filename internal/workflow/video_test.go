// video_test.go — WorkflowTestSuite tests for VideoWorkflow. Activities are
// mocked (testify/mock) so the workflow runs in-process: no worker, no
// Temporal server, no Twitter/Garage/ffmpeg. Tests focus on control flow —
// happy path (download→hash→passed), terminal reject skips hashing, and a
// transient download error fails the child (so the parent decrements inFlight).
package workflow_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
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

func TestVideoWorkflow_DownloadErrorFails(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newVideoEnv(&s)
	// Persistent transient error — retries exhaust, workflow fails, and the
	// parent's Selector callback turns that into an inFlight decrement.
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).Return(
		videoactivity.DownloadAndStageOutput{}, errors.New("cdn timeout"))

	env.ExecuteWorkflow(workflow.VideoWorkflow, stdVideoInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Error("expected workflow error after download retries exhausted")
	}
}
