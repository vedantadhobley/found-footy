// EventWorkflow promotion, publication, and critical-path telemetry tests.
package workflow_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

func TestEventWorkflow_AtomicExactDuplicateCommitsAndPublishes(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	assetID := uuid.New()
	env := baseEventEnvWithOptions(&s,
		discoveryactivity.LoadEventRecoveryStateOutput{},
		videoactivity.LoadEventAssetsOutput{Assets: []videoactivity.RestoredEventAsset{{
			AssetID: assetID, MD5: "0123456789abcdef0123456789abcdef",
			HashVersion: dvideo.CurrentFrameHashVersion(0.1),
			FrameHashes: []uint64{1, 2, 3}, Width: 1280, Height: 720,
			DurationMS: 7000, FileSizeBytes: 900_000, Popularity: 1, Verified: true,
		}}},
		true,
		true,
		true,
		discoveryactivity.GetDiscoveryConfigOutput{
			MaxAttempts: 1, AttemptSpacing: time.Minute,
			MaxAgeMinutes: 3, QueryTimeout: 2 * time.Minute,
		},
	)
	const tweetURL = "https://x.com/u/status/1111111111111111111"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}},
			Count:  1, StopReason: "age",
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed,
			MD5:     "0123456789abcdef0123456789abcdef", StagingKey: "staging/exact.mp4",
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil)
	var committed videoactivity.CommitClipPlacementInput
	env.OnActivity("CommitClipPlacement", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.CommitClipPlacementInput) (videoactivity.CommitClipPlacementOutput, error) {
			committed = in
			return videoactivity.CommitClipPlacementOutput{
				WinnerAssetID: assetID, ShareID: "s_atomic", Announce: true,
			}, nil
		}).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)
	if committed.NewWinner || committed.WinnerAssetID != assetID || len(committed.Candidates) != 1 {
		t.Fatalf("atomic placement = %+v, want one candidate on existing winner", committed)
	}
	if committed.Candidates[0].Evidence.TweetURL != tweetURL ||
		committed.Candidates[0].Outcome != discoverycontract.OutcomeDuplicate {
		t.Errorf("candidate placement = %+v, want attributed duplicate", committed.Candidates[0])
	}
	env.AssertNumberOfCalls(t, "CommitClipPlacement", 1)
	env.AssertNumberOfCalls(t, "PublishEventVideo", 1)
	env.AssertNumberOfCalls(t, "HashVideo", 0)
	env.AssertNumberOfCalls(t, "ValidateClip", 0)
	env.AssertNumberOfCalls(t, "BumpAssetPopularity", 0)
	env.AssertNumberOfCalls(t, "UpsertCandidateOutcome", 0)
}

func TestEventWorkflow_RemovedPlacementDoesNotPublish(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	assetID := uuid.New()
	env := baseEventEnvWithOptions(&s,
		discoveryactivity.LoadEventRecoveryStateOutput{},
		videoactivity.LoadEventAssetsOutput{Assets: []videoactivity.RestoredEventAsset{{
			AssetID: assetID, MD5: "fedcba9876543210fedcba9876543210",
			HashVersion: dvideo.CurrentFrameHashVersion(0.1),
			FrameHashes: []uint64{1, 2, 3}, Width: 1280, Height: 720,
			DurationMS: 7000, FileSizeBytes: 900_000, Popularity: 1, Verified: true,
		}}},
		true,
		true,
		true,
		discoveryactivity.GetDiscoveryConfigOutput{
			MaxAttempts: 1, AttemptSpacing: time.Minute,
			MaxAgeMinutes: 3, QueryTimeout: 2 * time.Minute,
		},
	)
	const tweetURL = "https://x.com/u/status/2222222222222222222"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}},
			Count:  1, StopReason: "age",
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed,
			MD5:     "fedcba9876543210fedcba9876543210", StagingKey: "staging/removed.mp4",
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil)
	env.OnActivity("CommitClipPlacement", mock.Anything, mock.Anything).
		Return(videoactivity.CommitClipPlacementOutput{EventRemoved: true}, nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)
	env.AssertNumberOfCalls(t, "CommitClipPlacement", 1)
	env.AssertNumberOfCalls(t, "PublishEventVideo", 0)
	env.AssertNumberOfCalls(t, "HashVideo", 0)
	env.AssertNumberOfCalls(t, "ValidateClip", 0)
}

// TestEventWorkflow_Pipeline_VerifyAndDedup requires two byte-identical
// candidates to produce one promoted asset and one popularity vote.
func TestEventWorkflow_Pipeline_VerifyAndDedup(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := baseEventEnv(&s)
	bumpTotal := 0
	env.OnActivity("BumpAssetPopularity", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.BumpAssetPopularityInput) error {
			n := in.Count
			if n < 1 {
				n = 1
			}
			bumpTotal += n
			return nil
		}).Maybe()
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()

	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{
				{TweetURL: "https://x.com/u/status/1111111111111111111", VideoPageURL: "vp1", DurationSeconds: 7},
				{TweetURL: "https://x.com/u/status/2222222222222222222", VideoPageURL: "vp2", DurationSeconds: 7},
			}, Count: 2, StopReason: "age",
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)

	// Both children pass with the same md5 → the second is an exact dup.
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, mock.Anything).
		Return(workflow.VideoWorkflowOutput{
			Outcome: "passed", MD5: "dupmd5", StagingKey: "staging/clip.mp4",
			FrameHashes: []uint64{1, 2, 4, 8, 16, 32}, Width: 1280, Height: 720,
			DurationMS: 7000, SizeBytes: 900_000,
		}, nil)
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil)
	promoteCalls, promotedPop := 0, 0
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.PromoteAndPersistInput) (videoactivity.PromoteAndPersistOutput, error) {
			promoteCalls++
			promotedPop = in.Popularity
			return videoactivity.PromoteAndPersistOutput{AssetID: uuid.New(), ShareID: "s_x", Inserted: true}, nil
		})

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow didn't complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.AssetsKept != 1 {
		t.Errorf("AssetsKept = %d, want 1 (one verified, one deduped)", out.AssetsKept)
	}
	if out.OutcomeClass != "assets_surfaced" {
		t.Errorf("OutcomeClass = %q, want assets_surfaced", out.OutcomeClass)
	}
	if promoteCalls != 1 {
		t.Errorf("PromoteAndPersist called %d times, want 1 (dup collapsed, not promoted)", promoteCalls)
	}
	// #180: the two md5-identical clips must count as 2 total — regardless of
	// interleaving. If the dup collapsed onto the still-pending clip it promotes
	// with popularity 2 (no bump); if it collapsed onto the already-promoted
	// asset it promotes with 1 + a bump. Either way the total is 2, no undercount.
	if promotedPop+bumpTotal != 2 {
		t.Errorf("total popularity = %d (promote %d + bumps %d), want 2 (#180)",
			promotedPop+bumpTotal, promotedPop, bumpTotal)
	}
}

// TestEventWorkflow_Pipeline_PromotePingsEventVideo — N3: a newly-minted clip
// (PromoteAndPersist → Minted=true) fires the event.video dirty-signal exactly
// once. Other pipeline tests deliberately leave Minted unset because their
// assertions do not exercise publication; this test owns the guard contract.
func TestEventWorkflow_Pipeline_PromotePingsEventVideo(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := baseEventEnv(&s)
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("BumpAssetPopularity", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{
				{TweetURL: "https://x.com/u/status/1111111111111111111", VideoPageURL: "vp1", DurationSeconds: 7},
			}, Count: 1, StopReason: "age",
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, mock.Anything).
		Return(workflow.VideoWorkflowOutput{
			Outcome: "passed", MD5: "md5a", StagingKey: "staging/a.mp4",
			FrameHashes: []uint64{1, 2, 4, 8, 16, 32}, Width: 1280, Height: 720,
			DurationMS: 7000, SizeBytes: 900_000,
		}, nil)
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil)
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(videoactivity.PromoteAndPersistOutput{AssetID: uuid.New(), ShareID: "s_x", Inserted: true, Minted: true}, nil)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)
	env.AssertNumberOfCalls(t, "PublishEventVideo", 1)
}

// TestEventWorkflow_EmitsCriticalPathMeasurements pins FF-050's current
// direct pipeline stages without changing the workflow's behavioral asserts.
func TestEventWorkflow_EmitsCriticalPathMeasurements(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	logger := &workflowLogCapture{}
	s.SetLogger(logger)
	env := baseEventEnvWithOptions(&s,
		discoveryactivity.LoadEventRecoveryStateOutput{},
		videoactivity.LoadEventAssetsOutput{},
		true,
		true,
		false,
		discoveryactivity.GetDiscoveryConfigOutput{
			MaxAttempts: 1, AttemptSpacing: time.Minute,
			MaxAgeMinutes: 3, QueryTimeout: 2 * time.Minute,
		},
	)
	const tweetURL = "https://x.com/u/status/1111111111111111111"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}},
			Count:  1, StopReason: "age",
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: "md5a", StagingKey: "staging/a.mp4",
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil)
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).
		Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8, 16, 32}}, nil)
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil)
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(videoactivity.PromoteAndPersistOutput{AssetID: uuid.New(), ShareID: "s_x", Inserted: true, Minted: true}, nil)
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil)
	env.OnActivity("PublishEventVideo", mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	actions, phases := logger.actionPhases()
	for _, action := range []string{
		"event_lifecycle_measured", "event_search_measured",
		"event_candidate_measured", "event_publish_measured",
	} {
		if !actions[action] {
			t.Errorf("missing workflow measurement action %q", action)
		}
	}
	for _, phase := range []string{
		"workflow_start", "observation_persist", "download", "hash",
		"vision", "promotion", "terminal_persist", "workflow_complete",
	} {
		if !phases[phase] {
			t.Errorf("missing workflow measurement phase %q", phase)
		}
	}
}
