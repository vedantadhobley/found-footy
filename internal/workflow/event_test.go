// discovery_test.go — WorkflowTestSuite tests for EventWorkflow.
// Mirrors ingest_test.go pattern: activity mocks via testify/mock so
// the workflow runs in-process (no worker, no Temporal server, no DB,
// no Twitter service).
//
// Tests focus on control flow — 10-attempt loop, exclude_urls
// accumulation, empty-query early exit, unknown-player early exit,
// candidate dedup within the workflow (same URL from two attempts
// only stores once), StopReason surfacing. Activity internals are
// covered by internal/activity/discovery/activities_test.go (future).
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
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

// newDiscoveryEnv sets up a test env with EventWorkflow +
// discovery activities registered. Individual tests attach OnActivity
// mocks before ExecuteWorkflow.
// baseEventEnv registers the workflows + activities + the always-present
// config/alias/complete stubs, but attaches NO child/pipeline mocks — so a
// test is free to set the child outcome it wants (testify picks the
// first-registered matching mock, so defaults can't be overridden).
func baseEventEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflow.EventWorkflow)
	env.RegisterWorkflow(workflow.VideoWorkflow)
	env.RegisterActivity(&discoveryactivity.Activities{})
	env.RegisterActivity(&visionactivity.Activities{})
	env.RegisterActivity(&videoactivity.PersistActivities{})
	// Default GetDiscoveryConfig stub. MaxAttempts=10 matches the
	// pre-#162 hardcoded value that existing tests were written
	// against (`want 10` assertions in AttemptsRun tests). Tests
	// that need a different value override this mock explicitly.
	// AttemptSpacing stays realistic (60s) but TestWorkflowEnvironment
	// auto-fires timers so real wall-clock waits don't happen.
	env.OnActivity("GetDiscoveryConfig", mock.Anything, mock.Anything).
		Return(discoveryactivity.GetDiscoveryConfigOutput{
			MaxAttempts:    10,
			AttemptSpacing: 60 * time.Second,
			MaxAgeMinutes:  3,
			QueryTimeout:   2 * time.Minute,
		}, nil).Maybe()
	// Default MarkDownstreamComplete + FetchTeamAliases stubs so tests
	// only need to override the interesting cases.
	env.OnActivity("MarkDownstreamComplete", mock.Anything, mock.Anything).
		Return(discoveryactivity.MarkDownstreamCompleteOutput{RowsUpdated: 1}, nil).Maybe()
	env.OnActivity("FetchTeamAliases", mock.Anything, mock.Anything).
		Return(discoveryactivity.FetchTeamAliasesOutput{
			CanonicalName: "Liverpool",
			Aliases:       []string{"liverpool", "reds", "lfc"},
			Found:         true,
		}, nil).Maybe()
	return env
}

// newDiscoveryEnv = base + pipeline defaults that make the SEARCH-LOOP tests
// behave: every spawned VideoWorkflow child returns "rejected" (no downstream
// vision/promote), so those tests exercise only the producer. Pipeline tests
// use baseEventEnv directly and set their own child/vision/promote mocks.
func newDiscoveryEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	env := baseEventEnv(s)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, mock.Anything).
		Return(workflow.VideoWorkflowOutput{Outcome: "rejected", RejectReason: "test-default"}, nil).Maybe()
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "rejected"}, nil).Maybe()
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(videoactivity.PromoteAndPersistOutput{AssetID: uuid.New(), ShareID: "s_test", Inserted: true}, nil).Maybe()
	env.OnActivity("BumpAssetPopularity", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	return env
}

// stdDiscoveryInput — realistic Salah / Liverpool goal event input.
func stdDiscoveryInput() workflow.EventWorkflowInput {
	return workflow.EventWorkflowInput{
		EventID:    uuid.New(),
		FixtureID:  12345,
		PlayerName: "M. Salah",
		TeamName:   "Liverpool",
		TeamID:     40,
		Minute:     15,
	}
}

// TestEventWorkflow_UnknownPlayer — D4b guard. Empty PlayerName
// short-circuits before any activity except MarkDownstreamComplete.
func TestEventWorkflow_UnknownPlayer(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)

	in := stdDiscoveryInput()
	in.PlayerName = ""

	env.ExecuteWorkflow(workflow.EventWorkflow, in)

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow didn't complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.OutcomeClass != "unknown_player" {
		t.Errorf("outcome_class = %q, want unknown_player", out.OutcomeClass)
	}
	if out.AttemptsRun != 0 {
		t.Errorf("attempts_run = %d, want 0 (should skip search loop)", out.AttemptsRun)
	}
	if !out.Completed {
		t.Errorf("expected Completed=true after MarkDownstreamComplete")
	}
}

// TestEventWorkflow_TenAttempts_AccumulatesCandidates — happy
// path. Each attempt returns 2 distinct tweets; the workflow persists
// them via StoreCandidate; final count reflects all 20.
func TestEventWorkflow_TenAttempts_AccumulatesCandidates(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)

	// Track which attempt we're on — return distinct tweet URLs per
	// attempt so we can verify dedup works across attempts.
	attempt := 0
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, _ discoveryactivity.SearchTweetsInput) (discoveryactivity.SearchTweetsOutput, error) {
			attempt++
			return discoveryactivity.SearchTweetsOutput{
				Videos: []twitter.VideoRef{
					{TweetURL: "https://x.com/user/status/" + strAttempt(attempt, 1), TweetText: "t1", VideoPageURL: "vp1", DurationSeconds: 10},
					{TweetURL: "https://x.com/user/status/" + strAttempt(attempt, 2), TweetText: "t2", VideoPageURL: "vp2", DurationSeconds: 15},
				},
				Count:      2,
				StopReason: "age",
			}, nil
		})
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow didn't complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.AttemptsRun != 10 {
		t.Errorf("attempts_run = %d, want 10", out.AttemptsRun)
	}
	if out.CandidatesFound != 20 { // 10 attempts × 2 distinct tweets each
		t.Errorf("candidates_found = %d, want 20", out.CandidatesFound)
	}
	// Default child mock returns "rejected", so candidates surface but no
	// assets → candidates_no_assets (pipeline outcome, not the old search-only one).
	if out.OutcomeClass != "candidates_no_assets" {
		t.Errorf("outcome_class = %q, want candidates_no_assets", out.OutcomeClass)
	}
}

// TestEventWorkflow_DedupSameTweetAcrossAttempts — if the same
// tweet appears in attempts 1, 2, 3, StoreCandidate fires once (the
// workflow's seenTweetIDs map dedups before invoking the activity).
func TestEventWorkflow_DedupSameTweetAcrossAttempts(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)

	// Same URL every attempt.
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{
				{TweetURL: "https://x.com/user/status/999", TweetText: "same tweet", VideoPageURL: "vp", DurationSeconds: 10},
			},
			Count:      1,
			StopReason: "consecutive_seen",
		}, nil)
	storeCalls := 0
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(func(_ context.Context, _ discoveryactivity.StoreCandidateInput) (discoveryactivity.StoreCandidateOutput, error) {
			storeCalls++
			return discoveryactivity.StoreCandidateOutput{Inserted: true}, nil
		})

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.CandidatesFound != 1 {
		t.Errorf("candidates_found = %d, want 1 (dedup)", out.CandidatesFound)
	}
	if storeCalls != 1 {
		t.Errorf("StoreCandidate called %d times, want 1 (workflow-side dedup)", storeCalls)
	}
	if out.AttemptsRun != 10 {
		t.Errorf("attempts_run = %d, want 10", out.AttemptsRun)
	}
}

// TestEventWorkflow_NoResults — every attempt returns zero videos.
// Workflow runs all 10 attempts, marks outcome_class=no_candidates.
func TestEventWorkflow_NoResults(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)

	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: nil, Count: 0, StopReason: "empty",
		}, nil)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.CandidatesFound != 0 {
		t.Errorf("candidates_found = %d, want 0", out.CandidatesFound)
	}
	if out.OutcomeClass != "no_candidates" {
		t.Errorf("outcome_class = %q, want no_candidates", out.OutcomeClass)
	}
	if out.AttemptsRun != 10 {
		t.Errorf("attempts_run = %d, want 10 (full loop even with empty results)", out.AttemptsRun)
	}
}

// TestEventWorkflow_FallbackToTeamName_WhenAliasesUnresolved —
// FetchTeamAliases returns Found=false; workflow falls back to
// in.TeamName as canonical for query building.
func TestEventWorkflow_FallbackToTeamName_WhenAliasesUnresolved(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)

	// Override the default alias stub to return "not found".
	env.OnActivity("FetchTeamAliases", mock.Anything, mock.Anything).
		Return(discoveryactivity.FetchTeamAliasesOutput{Found: false}, nil).Once()

	// Search should still fire — Discovery falls back to TeamName.
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{Videos: nil, Count: 0}, nil)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.AttemptsRun != 10 {
		t.Errorf("attempts_run = %d, want 10 (fallback path still runs full loop)", out.AttemptsRun)
	}
}

// strAttempt builds a synthetic 19-digit snowflake ID (valid per
// MinSnowflakeLen) encoding (attempt, index). Just used to keep
// per-attempt URLs distinct in the accumulator test.
func strAttempt(attempt, index int) string {
	// e.g., "1234567890000000101" for (attempt=1, index=1)
	base := "1234567890000000"
	return base + string(rune('0'+attempt%10)) + string(rune('0'+index%10)) + "0"
}

func pInt(i int) *int { return &i }

// TestEventWorkflow_Pipeline_VerifyAndDedup — the #164c-b consumer path: two
// candidates, both children pass with the SAME md5 (exact dup). The first is
// vision-verified and promoted; the second collapses onto it. Net: one asset
// kept, one duplicate, PromoteAndPersist called exactly once, assets_surfaced.
func TestEventWorkflow_Pipeline_VerifyAndDedup(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := baseEventEnv(&s)
	env.OnActivity("BumpAssetPopularity", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()

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
	promoteCalls := 0
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(func(_ context.Context, _ videoactivity.PromoteAndPersistInput) (videoactivity.PromoteAndPersistOutput, error) {
			promoteCalls++
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
}
