// EventWorkflow pre-hash exact-byte ownership and replay-compatibility tests.
package workflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

// TestEventWorkflow_PreHashExactClaimHashesIdenticalBytesOnce requires two
// identical downloads to share one dense hash while retaining both votes.
func TestEventWorkflow_PreHashExactClaimHashesIdenticalBytesOnce(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidatePreHashEnv(&s)
	const (
		md5 = "identical-raw-bytes"
		s1  = "staging/fixture/event/one.mp4"
		s2  = "staging/fixture/event/two.mp4"
	)
	frames := []uint64{1, 2, 4, 8, 16, 32}
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()

	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(t1)).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: s1,
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil).Once()
	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(t2)).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: s2,
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil).Once()
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).
		Return(videoactivity.HashVideoOutput{FrameHashes: frames}, nil).Once()
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil).Once()

	promotedPopularity, bumpedPopularity := 0, 0
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.PromoteAndPersistInput) (videoactivity.PromoteAndPersistOutput, error) {
			promotedPopularity = in.Popularity
			return videoactivity.PromoteAndPersistOutput{AssetID: uuid.New(), Inserted: true}, nil
		}).Once()
	env.OnActivity("BumpAssetPopularity", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.BumpAssetPopularityInput) error {
			bumpedPopularity += in.Count
			return nil
		}).Maybe()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	env.AssertNumberOfCalls(t, "HashVideo", 1)
	env.AssertNumberOfCalls(t, "ValidateClip", 1)
	env.AssertNumberOfCalls(t, "PromoteAndPersist", 1)
	env.AssertNotCalled(t, "VideoWorkflow", mock.Anything, mock.Anything)
	if promotedPopularity+bumpedPopularity != 2 {
		t.Errorf("total popularity = %d, want 2 exact-byte sightings", promotedPopularity+bumpedPopularity)
	}
}

func TestEventWorkflow_PreHashDeterministicRejectSkipsVision(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := preHashEventEnv(&s)
	const (
		tweetURL   = "https://x.com/u/status/1111111111111111111"
		stagingKey = "staging/fixture/event/short.mp4"
	)
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}}, Count: 1,
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil).Once()
	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(tweetURL)).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: "short", StagingKey: stagingKey,
		}, nil).Once()
	env.OnActivity("HashVideo", mock.Anything, hashStagingIs(stagingKey)).
		Return(videoactivity.HashVideoOutput{
			Outcome: videoactivity.OutcomeRejected, RejectReason: videoactivity.RejectInsufficientHashFrames,
		}, nil).Once()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything,
		mock.MatchedBy(func(in discoveryactivity.UpsertCandidateOutcomeInput) bool {
			return in.Evidence.TweetURL == tweetURL &&
				in.Outcome == discoveryactivity.OutcomeRejected &&
				in.RejectReason == videoactivity.RejectInsufficientHashFrames
		})).Return(nil).Once()
	env.OnActivity("DeleteStaging", mock.Anything,
		videoactivity.DeleteStagingInput{StagingKey: stagingKey}).Return(nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	env.AssertNumberOfCalls(t, "HashVideo", 1)
	env.AssertNotCalled(t, "ValidateClip", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "PromoteAndPersist", mock.Anything, mock.Anything)
}

// TestEventWorkflow_PreHashClaimTransfersAfterHashFailure proves a bad first
// staging object does not poison every exact-byte candidate. The first owner
// exhausts its three retries and is stamped failed; the waiting claimant then
// gets a fresh three-attempt budget and reaches vision.
func TestEventWorkflow_PreHashClaimTransfersAfterHashFailure(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidatePreHashEnv(&s)
	const (
		md5 = "identical-raw-bytes"
		s1  = "staging/fixture/event/primary.mp4"
		s2  = "staging/fixture/event/fallback.mp4"
	)

	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(t1)).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: s1,
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil).Once()
	// Ensure t1 owns the claim before t2 becomes ready, while still allowing t2
	// to join before t1's retry sequence exhausts.
	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(t2)).
		After(time.Second).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: s2,
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil).Once()
	env.OnActivity("HashVideo", mock.Anything, hashStagingIs(s1)).
		Return(videoactivity.HashVideoOutput{}, errors.New("garage object unreadable"))
	env.OnActivity("HashVideo", mock.Anything, hashStagingIs(s2)).
		Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8}}, nil).Once()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything,
		failedCandidateIs(t1, workflow.VideoFailureHash)).Return(nil).Once()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything,
		candidateOutcomeIs(t2, discoveryactivity.OutcomePromoted)).Return(nil).Once()
	env.OnActivity("DeleteStaging", mock.Anything,
		videoactivity.DeleteStagingInput{StagingKey: s1}).Return(nil).Once()
	env.OnActivity("ValidateClip", mock.Anything, stagingIs(s2)).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil).Once()
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(videoactivity.PromoteAndPersistOutput{AssetID: uuid.New(), Inserted: true}, nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	env.AssertNumberOfCalls(t, "HashVideo", 4)
	env.AssertNotCalled(t, "UpsertCandidateOutcome", mock.Anything,
		candidateOutcomeIs(t2, discoveryactivity.OutcomeDuplicate))
}

// TestEventWorkflow_PreHashCancellationEmitsNoFollowOnCommands keeps FF-015's
// cancellation ownership intact after removing the child boundary.
func TestEventWorkflow_PreHashCancellationEmitsNoFollowOnCommands(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := preHashEventEnv(&s)
	const (
		tweetURL   = "https://x.com/u/status/1111111111111111111"
		stagingKey = "staging/fixture/event/cancel.mp4"
	)
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}}, Count: 1,
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil).Once()
	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(tweetURL)).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: "md5", StagingKey: stagingKey,
		}, nil).Once()
	env.OnActivity("HashVideo", mock.Anything, hashStagingIs(stagingKey)).
		After(10*time.Minute).
		Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1}}, nil).Once()
	env.RegisterDelayedCallback(env.CancelWorkflow, 30*time.Second)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireCanceled(t, env)
	env.AssertNumberOfCalls(t, "HashVideo", 1)
	env.AssertNotCalled(t, "ValidateClip", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "UpsertCandidateOutcome", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "DeleteStaging", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "MarkDownstreamComplete", mock.Anything, mock.Anything)
}

// TestEventWorkflow_PreFF022HistoryKeepsVideoChild proves the version marker
// leaves already-running histories on their original child workflow path.
func TestEventWorkflow_PreFF022HistoryKeepsVideoChild(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := baseEventEnv(&s)
	const tweetURL = "https://x.com/u/status/1111111111111111111"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}}, Count: 1,
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil).Once()
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(tweetURL)).
		Return(workflow.VideoWorkflowOutput{Outcome: workflow.VideoOutcomeRejected, RejectReason: "test"}, nil).Once()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	env.AssertNumberOfCalls(t, "VideoWorkflow", 1)
	env.AssertNotCalled(t, "DownloadAndStage", mock.Anything, mock.Anything)
}
