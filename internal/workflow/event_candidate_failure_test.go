// EventWorkflow candidate-failure classification and replay-compatibility tests.
package workflow_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

func TestEventWorkflow_DownloadFailureStampsCandidate(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, tweetURL := failedCandidateEnv(&s, workflow.VideoWorkflowOutput{
		Outcome:       workflow.VideoOutcomeFailed,
		FailureReason: workflow.VideoFailureDownload,
	}, nil)
	env.OnActivity("UpsertCandidateOutcome", mock.Anything,
		failedCandidateIs(tweetURL, workflow.VideoFailureDownload)).Return(nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireDone(t, env)
	env.AssertNotCalled(t, "DeleteStaging", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "ValidateClip", mock.Anything, mock.Anything)
}

// TestEventWorkflow_HashFailureStampsCandidateAndDeletesStaging covers the
// failure branch that owns a Garage staging object.
func TestEventWorkflow_HashFailureStampsCandidateAndDeletesStaging(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	const stagingKey = "staging/12345/event/tweet.mp4"
	env, tweetURL := failedCandidateEnv(&s, workflow.VideoWorkflowOutput{
		Outcome:       workflow.VideoOutcomeFailed,
		FailureReason: workflow.VideoFailureHash,
		StagingKey:    stagingKey,
	}, nil)
	env.OnActivity("UpsertCandidateOutcome", mock.Anything,
		failedCandidateIs(tweetURL, workflow.VideoFailureHash)).Return(nil).Once()
	env.OnActivity("DeleteStaging", mock.Anything,
		videoactivity.DeleteStagingInput{StagingKey: stagingKey}).Return(nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireDone(t, env)
	env.AssertNotCalled(t, "ValidateClip", mock.Anything, mock.Anything)
}

// TestEventWorkflow_UnexpectedChildFailureUsesCapturedTweetURL proves a child
// workflow error cannot erase the candidate correlation key.
func TestEventWorkflow_UnexpectedChildFailureUsesCapturedTweetURL(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, tweetURL := failedCandidateEnv(&s, workflow.VideoWorkflowOutput{}, errors.New("child panic"))
	env.OnActivity("UpsertCandidateOutcome", mock.Anything,
		failedCandidateIs(tweetURL, workflow.VideoFailureUnexpectedChild)).Return(nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireDone(t, env)
	env.AssertNotCalled(t, "DeleteStaging", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "ValidateClip", mock.Anything, mock.Anything)
}

// TestEventWorkflow_DefaultVersionPreservesChildFailureCommandSequence proves
// old histories do not gain a new persistence activity during replay.
func TestEventWorkflow_DefaultVersionPreservesChildFailureCommandSequence(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, _ := failedCandidateEnv(&s, workflow.VideoWorkflowOutput{}, errors.New("child panic"))
	env.OnGetVersion(ff002ChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
		Return(sdkworkflow.DefaultVersion).
		Once()
	env.OnGetVersion(ff034DurabilityChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
		Return(sdkworkflow.DefaultVersion).
		Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireDone(t, env)
	env.AssertNotCalled(t, "RecordCandidateOutcome", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "UpsertCandidateOutcome", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "DeleteStaging", mock.Anything, mock.Anything)
}
