// EventWorkflow exact-byte follower terminal-outcome regression tests.
package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

type capturedCandidateOutcome struct {
	outcome discoveryactivity.CandidateOutcome
	reason  string
	detail  json.RawMessage
}

type candidateOutcomeCapture struct {
	mu   sync.Mutex
	rows map[string]capturedCandidateOutcome
}

func captureCandidateOutcomes(env *testsuite.TestWorkflowEnvironment) *candidateOutcomeCapture {
	capture := &candidateOutcomeCapture{rows: make(map[string]capturedCandidateOutcome)}
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in discoveryactivity.UpsertCandidateOutcomeInput) error {
			capture.mu.Lock()
			defer capture.mu.Unlock()
			capture.rows[in.Evidence.TweetURL] = capturedCandidateOutcome{
				outcome: in.Outcome,
				reason:  in.RejectReason,
				detail:  append(json.RawMessage(nil), in.Detail...),
			}
			return nil
		}).Maybe()
	return capture
}

func (c *candidateOutcomeCapture) snapshot() map[string]capturedCandidateOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make(map[string]capturedCandidateOutcome, len(c.rows))
	for tweetURL, row := range c.rows {
		rows[tweetURL] = row
	}
	return rows
}

func mockExactDownloads(env *testsuite.TestWorkflowEnvironment, t1, t2 string) {
	const md5 = "identical-raw-bytes"
	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(t1)).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: "staging/one.mp4",
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil).Once()
	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(t2)).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: "staging/two.mp4",
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil).Once()
}

func requireOutcomeCounts(t *testing.T, rows map[string]capturedCandidateOutcome, want map[discoveryactivity.CandidateOutcome]int) {
	t.Helper()
	got := make(map[discoveryactivity.CandidateOutcome]int)
	for _, row := range rows {
		got[row.outcome]++
	}
	for outcome, count := range want {
		if got[outcome] != count {
			t.Errorf("%s outcomes = %d, want %d (all=%v)", outcome, got[outcome], count, got)
		}
	}
}

// TestEventWorkflow_ExactFollowerPromotesOnlyAfterWinnerExists covers a late
// byte-identical arrival while vision is pending. It shares one validation,
// contributes one popularity vote, and becomes duplicate only after promotion.
func TestEventWorkflow_ExactFollowerPromotesOnlyAfterWinnerExists(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidatePreHashEnv(&s)
	capture := captureCandidateOutcomes(env)
	const md5 = "identical-raw-bytes"
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("BumpAssetPopularity", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(t1)).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: "staging/one.mp4",
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil).Once()
	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(t2)).
		After(10*time.Millisecond).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: "staging/two.mp4",
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil).Once()
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).
		Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8}}, nil).Once()
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		After(20*time.Millisecond).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil).Once()
	assetID := uuid.New()
	promotedPopularity := 0
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.PromoteAndPersistInput) (videoactivity.PromoteAndPersistOutput, error) {
			promotedPopularity = in.Popularity
			return videoactivity.PromoteAndPersistOutput{AssetID: assetID, Inserted: true}, nil
		}).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	rows := capture.snapshot()
	requireOutcomeCounts(t, rows, map[discoveryactivity.CandidateOutcome]int{
		discoveryactivity.OutcomePromoted:  1,
		discoveryactivity.OutcomeDuplicate: 1,
	})
	if promotedPopularity != 2 {
		t.Errorf("promoted popularity = %d, want 2", promotedPopularity)
	}
	for _, row := range rows {
		if row.outcome != discoveryactivity.OutcomeDuplicate {
			continue
		}
		var detail struct {
			WinnerAssetID string `json:"winner_asset_id"`
		}
		if json.Unmarshal(row.detail, &detail) != nil || detail.WinnerAssetID != assetID.String() {
			t.Errorf("duplicate detail = %s, want winner_asset_id %s", row.detail, assetID)
		}
	}
	env.AssertNumberOfCalls(t, "HashVideo", 1)
	env.AssertNumberOfCalls(t, "ValidateClip", 1)
}

// TestEventWorkflow_ExactFollowerMatchingAssetClosesImmediately covers the
// branch where the durable winner exists before the second download completes.
func TestEventWorkflow_ExactFollowerMatchingAssetClosesImmediately(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidatePreHashEnv(&s)
	capture := captureCandidateOutcomes(env)
	const md5 = "identical-raw-bytes"
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(t1)).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: "staging/one.mp4",
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil).Once()
	env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(t2)).
		After(20*time.Millisecond).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: "staging/two.mp4",
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil).Once()
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).
		Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8}}, nil).Once()
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil).Once()
	assetID := uuid.New()
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(videoactivity.PromoteAndPersistOutput{AssetID: assetID, Inserted: true}, nil).Once()
	env.OnActivity("BumpAssetPopularity", mock.Anything,
		videoactivity.BumpAssetPopularityInput{AssetID: assetID, Count: 1}).Return(nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	rows := capture.snapshot()
	requireOutcomeCounts(t, rows, map[discoveryactivity.CandidateOutcome]int{
		discoveryactivity.OutcomePromoted:  1,
		discoveryactivity.OutcomeDuplicate: 1,
	})
	for _, row := range rows {
		if row.outcome != discoveryactivity.OutcomeDuplicate {
			continue
		}
		var detail struct {
			WinnerAssetID string `json:"winner_asset_id"`
		}
		if json.Unmarshal(row.detail, &detail) != nil || detail.WinnerAssetID != assetID.String() {
			t.Errorf("duplicate detail = %s, want winner_asset_id %s", row.detail, assetID)
		}
	}
	env.AssertNumberOfCalls(t, "HashVideo", 1)
	env.AssertNumberOfCalls(t, "ValidateClip", 1)
}

// TestEventWorkflow_ExactFollowersShareVisionRejection proves a representative
// content verdict applies to every identical candidate without creating a
// duplicate row that points to no asset.
func TestEventWorkflow_ExactFollowersShareVisionRejection(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidatePreHashEnv(&s)
	capture := captureCandidateOutcomes(env)
	mockExactDownloads(env, t1, t2)
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).
		After(10*time.Millisecond).
		Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8}}, nil).Once()
	const reason = "clock present but does not match expected"
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "rejected", Reason: reason, SoccerVotes: 3}, nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	rows := capture.snapshot()
	requireOutcomeCounts(t, rows, map[discoveryactivity.CandidateOutcome]int{
		discoveryactivity.OutcomeRejected:  2,
		discoveryactivity.OutcomeDuplicate: 0,
	})
	for tweetURL, row := range rows {
		if row.reason != reason {
			t.Errorf("%s reason = %q, want %q", tweetURL, row.reason, reason)
		}
	}
	var sharedDetail string
	for tweetURL, row := range rows {
		if len(row.detail) == 0 {
			t.Errorf("%s has no shared rejection evidence", tweetURL)
		}
		if sharedDetail == "" {
			sharedDetail = string(row.detail)
		} else if string(row.detail) != sharedDetail {
			t.Errorf("%s detail differs from representative evidence", tweetURL)
		}
	}
	env.AssertNumberOfCalls(t, "HashVideo", 1)
	env.AssertNumberOfCalls(t, "ValidateClip", 1)
	env.AssertNotCalled(t, "PromoteAndPersist", mock.Anything, mock.Anything)
}

// TestEventWorkflow_ExactFollowersShareVisionFailure keeps one bounded vision
// retry unit for identical bytes and stamps every claimant with its result.
func TestEventWorkflow_ExactFollowersShareVisionFailure(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidatePreHashEnv(&s)
	capture := captureCandidateOutcomes(env)
	mockExactDownloads(env, t1, t2)
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).
		After(10*time.Millisecond).
		Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8}}, nil).Once()
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{}, errors.New("inference unavailable"))

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	rows := capture.snapshot()
	requireOutcomeCounts(t, rows, map[discoveryactivity.CandidateOutcome]int{
		discoveryactivity.OutcomeFailed:    2,
		discoveryactivity.OutcomeDuplicate: 0,
	})
	for tweetURL, row := range rows {
		if row.reason != "vision_error" {
			t.Errorf("%s reason = %q, want vision_error", tweetURL, row.reason)
		}
	}
	env.AssertNumberOfCalls(t, "ValidateClip", 3)
}

// TestEventWorkflow_ExactFollowersSharePromotionFailure keeps one bounded
// promotion retry unit and leaves no follower claiming a nonexistent winner.
func TestEventWorkflow_ExactFollowersSharePromotionFailure(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidatePreHashEnv(&s)
	capture := captureCandidateOutcomes(env)
	mockExactDownloads(env, t1, t2)
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).
		After(10*time.Millisecond).
		Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8}}, nil).Once()
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil).Once()
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(videoactivity.PromoteAndPersistOutput{}, errors.New("garage unavailable"))

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	rows := capture.snapshot()
	requireOutcomeCounts(t, rows, map[discoveryactivity.CandidateOutcome]int{
		discoveryactivity.OutcomeFailed:    2,
		discoveryactivity.OutcomeDuplicate: 0,
	})
	for tweetURL, row := range rows {
		if row.reason != "promote_error" {
			t.Errorf("%s reason = %q, want promote_error", tweetURL, row.reason)
		}
	}
	env.AssertNumberOfCalls(t, "PromoteAndPersist", 5)
}

// TestEventWorkflow_PreFF065HistoryKeepsImmediateFollowerOutcome proves an
// in-flight history keeps the former duplicate-before-vision command sequence.
func TestEventWorkflow_PreFF065HistoryKeepsImmediateFollowerOutcome(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := preFF065PreHashEventEnv(&s)
	t1, t2 := wireTwoCandidateSearch(env)
	capture := captureCandidateOutcomes(env)
	mockExactDownloads(env, t1, t2)
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).
		After(10*time.Millisecond).
		Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8}}, nil).Once()
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "rejected", Reason: "wrong_clock"}, nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	requireOutcomeCounts(t, capture.snapshot(), map[discoveryactivity.CandidateOutcome]int{
		discoveryactivity.OutcomeRejected:  1,
		discoveryactivity.OutcomeDuplicate: 1,
	})
}
