// EventWorkflow category-scoped dedup and quality-winner tests.
package workflow_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

// TestEventWorkflow_Pipeline_CategoryScopedDedup requires visually matching
// verified and unverified clips to remain in separate dedup pools.
func TestEventWorkflow_Pipeline_CategoryScopedDedup(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidateEnv(&s)
	frames := []uint64{1, 2, 4, 8, 16, 32}

	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(t1)).
		Return(passedChild(t1, "md5a", "s1", 1280, 720, 7000, 900_000, frames), nil)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(t2)).
		Return(passedChild(t2, "md5b", "s2", 1280, 720, 7000, 900_000, frames), nil)
	env.OnActivity("ValidateClip", mock.Anything, stagingIs("s1")).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil)
	env.OnActivity("ValidateClip", mock.Anything, stagingIs("s2")).
		Return(visionactivity.ValidateClipOutput{Outcome: "unverified"}, nil)

	promoteCalls, supersedeCalls := 0, 0
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.PromoteAndPersistInput) (videoactivity.PromoteAndPersistOutput, error) {
			promoteCalls++
			return videoactivity.PromoteAndPersistOutput{AssetID: uuid.New(), ShareID: "s_" + in.MD5, Inserted: true}, nil
		})
	env.OnActivity("SupersedeAssets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, _ videoactivity.SupersedeAssetsInput) error { supersedeCalls++; return nil }).Maybe()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.AssetsKept != 2 {
		t.Errorf("AssetsKept = %d, want 2 (verified + unverified never collapse)", out.AssetsKept)
	}
	if promoteCalls != 2 {
		t.Errorf("PromoteAndPersist called %d times, want 2", promoteCalls)
	}
	if supersedeCalls != 0 {
		t.Errorf("SupersedeAssets called %d times, want 0 (cross-pool never supersedes)", supersedeCalls)
	}
}

// TestEventWorkflow_Pipeline_PerceptualDedupWithinPool — two perceptually-
// identical VERIFIED clips, different md5 (gate md5 check misses them), equal
// quality. Post-vision perceptual dedup collapses the second onto the first
// (keep-first on a quality tie): one asset, one promote, no supersede.
func TestEventWorkflow_Pipeline_PerceptualDedupWithinPool(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidateEnv(&s)
	frames := []uint64{1, 2, 4, 8, 16, 32}

	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(t1)).
		Return(passedChild(t1, "md5a", "s1", 1280, 720, 7000, 900_000, frames), nil)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(t2)).
		Return(passedChild(t2, "md5b", "s2", 1280, 720, 7000, 900_000, frames), nil)
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil)

	promoteCalls, supersedeCalls := 0, 0
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.PromoteAndPersistInput) (videoactivity.PromoteAndPersistOutput, error) {
			promoteCalls++
			return videoactivity.PromoteAndPersistOutput{AssetID: uuid.New(), ShareID: "s_" + in.MD5, Inserted: true}, nil
		})
	env.OnActivity("SupersedeAssets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, _ videoactivity.SupersedeAssetsInput) error { supersedeCalls++; return nil }).Maybe()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.AssetsKept != 1 {
		t.Errorf("AssetsKept = %d, want 1 (same-pool perceptual dup collapses)", out.AssetsKept)
	}
	if promoteCalls != 1 {
		t.Errorf("PromoteAndPersist called %d times, want 1 (second collapsed, not promoted)", promoteCalls)
	}
	if supersedeCalls != 0 {
		t.Errorf("SupersedeAssets called %d times, want 0 (equal quality → keep-first)", supersedeCalls)
	}
}

func TestEventWorkflow_Pipeline_DifferentHashVersionsNeverCompare(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidateEnv(&s)
	frames := []uint64{1, 2, 4, 8, 16, 32}

	legacy := passedChild(t1, "md5a", "s1", 1280, 720, 7000, 900_000, frames)
	legacy.HashVersion = dvideo.LegacyFrameHashVersion
	bounded := passedChild(t2, "md5b", "s2", 1280, 720, 7000, 900_000, frames)
	bounded.HashVersion = dvideo.CurrentFrameHashVersion(0.1)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(t1)).Return(legacy, nil)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(t2)).Return(bounded, nil)
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil)

	promoteCalls := 0
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.PromoteAndPersistInput) (videoactivity.PromoteAndPersistOutput, error) {
			promoteCalls++
			return videoactivity.PromoteAndPersistOutput{AssetID: uuid.New(), ShareID: "s_" + in.MD5, Inserted: true}, nil
		})

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.AssetsKept != 2 || promoteCalls != 2 {
		t.Fatalf("assets/promotions = %d/%d, want 2/2 for incomparable versions", out.AssetsKept, promoteCalls)
	}
}

// TestEventWorkflow_Pipeline_QualitySupersede — two perceptually-identical
// VERIFIED clips, different md5, DIFFERENT quality. Child completion order is
// intentionally unconstrained: low-first promotes both then supersedes low;
// high-first promotes high and collapses low. Both paths must keep high only.
func TestEventWorkflow_Pipeline_QualitySupersede(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidateEnv(&s)
	frames := []uint64{1, 2, 4, 8, 16, 32}

	// t1 is low-res and t2 is high-res; Temporal may complete either child first.
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(t1)).
		Return(passedChild(t1, "md5low", "s1", 640, 360, 7000, 400_000, frames), nil)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(t2)).
		Return(passedChild(t2, "md5high", "s2", 1920, 1080, 7000, 2_500_000, frames), nil)
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil)

	promoteCalls, supersedeCalls, loserCount := 0, 0, 0
	var promotedMD5s []string
	promotedIDs := map[string]uuid.UUID{}
	var supersedeWinner uuid.UUID
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.PromoteAndPersistInput) (videoactivity.PromoteAndPersistOutput, error) {
			promoteCalls++
			// Deterministic id per md5 so the winner supersedes the right loser.
			id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(in.EventID.String()+":"+in.MD5))
			promotedMD5s = append(promotedMD5s, in.MD5)
			promotedIDs[in.MD5] = id
			return videoactivity.PromoteAndPersistOutput{AssetID: id, ShareID: "s_" + in.MD5, Inserted: true}, nil
		})
	env.OnActivity("SupersedeAssets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.SupersedeAssetsInput) error {
			supersedeCalls++
			loserCount += len(in.LoserAssetIDs)
			supersedeWinner = in.WinnerAssetID
			return nil
		})

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.AssetsKept != 1 {
		t.Errorf("AssetsKept = %d, want 1 (cluster collapses to the winner)", out.AssetsKept)
	}
	switch promoteCalls {
	case 1:
		if len(promotedMD5s) != 1 || promotedMD5s[0] != "md5high" || supersedeCalls != 0 {
			t.Errorf("high-first path promoted=%v supersedes=%d, want [md5high]/0", promotedMD5s, supersedeCalls)
		}
	case 2:
		if supersedeCalls != 1 || loserCount != 1 || supersedeWinner != promotedIDs["md5high"] {
			t.Errorf("low-first path supersedes=%d losers=%d winner=%s, want 1/1/high",
				supersedeCalls, loserCount, supersedeWinner)
		}
	default:
		t.Errorf("PromoteAndPersist called %d times, want 1 or 2", promoteCalls)
	}
}
