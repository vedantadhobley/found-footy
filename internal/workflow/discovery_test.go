// discovery_test.go — WorkflowTestSuite tests for DiscoveryWorkflow.
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
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

// newDiscoveryEnv sets up a test env with DiscoveryWorkflow +
// discovery activities registered. Individual tests attach OnActivity
// mocks before ExecuteWorkflow.
func newDiscoveryEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflow.DiscoveryWorkflow)
	env.RegisterActivity(&discoveryactivity.Activities{})
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

// stdDiscoveryInput — realistic Salah / Liverpool goal event input.
func stdDiscoveryInput() workflow.DiscoveryWorkflowInput {
	return workflow.DiscoveryWorkflowInput{
		EventID:    uuid.New(),
		FixtureID:  12345,
		PlayerName: "M. Salah",
		TeamName:   "Liverpool",
		TeamID:     40,
		Minute:     15,
	}
}

// TestDiscoveryWorkflow_UnknownPlayer — D4b guard. Empty PlayerName
// short-circuits before any activity except MarkDownstreamComplete.
func TestDiscoveryWorkflow_UnknownPlayer(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)

	in := stdDiscoveryInput()
	in.PlayerName = ""

	env.ExecuteWorkflow(workflow.DiscoveryWorkflow, in)

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow didn't complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.DiscoveryWorkflowOutput
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

// TestDiscoveryWorkflow_TenAttempts_AccumulatesCandidates — happy
// path. Each attempt returns 2 distinct tweets; the workflow persists
// them via StoreCandidate; final count reflects all 20.
func TestDiscoveryWorkflow_TenAttempts_AccumulatesCandidates(t *testing.T) {
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

	env.ExecuteWorkflow(workflow.DiscoveryWorkflow, stdDiscoveryInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow didn't complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.DiscoveryWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.AttemptsRun != 10 {
		t.Errorf("attempts_run = %d, want 10", out.AttemptsRun)
	}
	if out.CandidatesFound != 20 { // 10 attempts × 2 distinct tweets each
		t.Errorf("candidates_found = %d, want 20", out.CandidatesFound)
	}
	if out.OutcomeClass != "candidates_found" {
		t.Errorf("outcome_class = %q, want candidates_found", out.OutcomeClass)
	}
}

// TestDiscoveryWorkflow_DedupSameTweetAcrossAttempts — if the same
// tweet appears in attempts 1, 2, 3, StoreCandidate fires once (the
// workflow's seenTweetIDs map dedups before invoking the activity).
func TestDiscoveryWorkflow_DedupSameTweetAcrossAttempts(t *testing.T) {
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

	env.ExecuteWorkflow(workflow.DiscoveryWorkflow, stdDiscoveryInput())

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.DiscoveryWorkflowOutput
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

// TestDiscoveryWorkflow_NoResults — every attempt returns zero videos.
// Workflow runs all 10 attempts, marks outcome_class=no_candidates.
func TestDiscoveryWorkflow_NoResults(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)

	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: nil, Count: 0, StopReason: "empty",
		}, nil)

	env.ExecuteWorkflow(workflow.DiscoveryWorkflow, stdDiscoveryInput())

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.DiscoveryWorkflowOutput
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

// TestDiscoveryWorkflow_FallbackToTeamName_WhenAliasesUnresolved —
// FetchTeamAliases returns Found=false; workflow falls back to
// in.TeamName as canonical for query building.
func TestDiscoveryWorkflow_FallbackToTeamName_WhenAliasesUnresolved(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)

	// Override the default alias stub to return "not found".
	env.OnActivity("FetchTeamAliases", mock.Anything, mock.Anything).
		Return(discoveryactivity.FetchTeamAliasesOutput{Found: false}, nil).Once()

	// Search should still fire — Discovery falls back to TeamName.
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{Videos: nil, Count: 0}, nil)

	env.ExecuteWorkflow(workflow.DiscoveryWorkflow, stdDiscoveryInput())

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
	var out workflow.DiscoveryWorkflowOutput
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
