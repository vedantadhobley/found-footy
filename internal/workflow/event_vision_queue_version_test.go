// Full EventWorkflow tests pin old/new queue routing after the version marker.
package workflow_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

// TestEventWorkflowVisionQueueVersion leaves old activity destinations intact
// and routes new claimant validation, but not hashing, onto the derived queue.
func TestEventWorkflowVisionQueueVersion(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "legacy"
		version := sdkworkflow.DefaultVersion
		if enabled {
			name, version = "isolated", sdkworkflow.Version(1)
		}
		t.Run(name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := preHashEventEnv(&suite)
			env.OnGetVersion("ff-037-vision-task-queue", sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).Return(version).Once()
			env.OnActivity("SearchTweets", mock.Anything, mock.Anything).Return(discoveryactivity.SearchTweetsOutput{
				Videos: []twitter.VideoRef{{TweetURL: "clip", VideoPageURL: "page", DurationSeconds: 7}}, Count: 1,
			}, nil)
			env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).Return(discoveryactivity.StoreCandidateOutput{}, nil)
			env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).Return(videoactivity.DownloadAndStageOutput{
				Outcome: videoactivity.OutcomePassed, MD5: "unique", StagingKey: "staging/clip", Width: 1280, Height: 720,
			}, nil).Once()
			env.OnActivity("HashVideo", mock.Anything, mock.Anything).Return(func(ctx context.Context, _ videoactivity.HashVideoInput) (videoactivity.HashVideoOutput, error) {
				require.Equal(t, "default-test-taskqueue", activity.GetInfo(ctx).TaskQueue)
				return videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8}}, nil
			}).Once()
			env.OnActivity("ValidateClip", mock.Anything, mock.Anything).Return(func(ctx context.Context, _ visionactivity.ValidateClipInput) (visionactivity.ValidateClipOutput, error) {
				want := "default-test-taskqueue"
				if enabled {
					want = discoverycontract.VisionTaskQueue(want)
				}
				require.Equal(t, want, activity.GetInfo(ctx).TaskQueue)
				return visionactivity.ValidateClipOutput{Outcome: "rejected", Reason: "test reject"}, nil
			}).Once()
			env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Once()
			env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Once()
			env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
			requireDone(t, env)
			env.AssertExpectations(t)
		})
	}
}
