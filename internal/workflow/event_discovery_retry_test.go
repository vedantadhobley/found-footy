// EventWorkflow empty-result, retry, fallback, cancellation, and vision-retry tests.
package workflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

// TestEventWorkflow_NoResults requires every configured empty attempt to end
// with a completed no-candidates outcome.
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

// TestEventWorkflow_SearchRetrySpansBrowserRestart proves FF-017's final
// attempt can outlive a cold Firefox container. Three fast transport failures
// would exhaust the old policy; the fourth activity try must still surface the
// candidate without requiring another outer discovery attempt.
func TestEventWorkflow_SearchRetrySpansBrowserRestart(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s, discoveryactivity.GetDiscoveryConfigOutput{
		MaxAttempts: 1, AttemptSpacing: time.Minute, MaxAgeMinutes: 3,
		QueryTimeout: 2 * time.Minute,
	})
	searchCalls := 0
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, _ discoveryactivity.SearchTweetsInput) (discoveryactivity.SearchTweetsOutput, error) {
			searchCalls++
			if searchCalls < 4 {
				return discoveryactivity.SearchTweetsOutput{}, errors.New("browser restarting")
			}
			return discoveryactivity.SearchTweetsOutput{Videos: []twitter.VideoRef{{
				TweetURL: "https://x.com/u/status/4444444444444444444",
			}}}, nil
		})
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)
	var out workflow.EventWorkflowOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if searchCalls != 4 || out.AttemptsRun != 1 || out.CandidatesFound != 1 {
		t.Fatalf("recovery result = calls %d/attempts %d/candidates %d, want 4/1/1",
			searchCalls, out.AttemptsRun, out.CandidatesFound)
	}
}

// TestEventWorkflow_DefaultVersionPreservesPreRestartRetryPolicy proves old
// histories retain the original three activity attempts. This is the replay
// complement to the four-attempt FF-017 recovery test above.
func TestEventWorkflow_DefaultVersionPreservesPreRestartRetryPolicy(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s, discoveryactivity.GetDiscoveryConfigOutput{
		MaxAttempts: 1, AttemptSpacing: time.Minute, MaxAgeMinutes: 3,
		QueryTimeout: 2 * time.Minute,
	})
	env.OnGetVersion(ff017RestartChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
		Return(sdkworkflow.DefaultVersion).
		Once()
	searchCalls := 0
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, _ discoveryactivity.SearchTweetsInput) (discoveryactivity.SearchTweetsOutput, error) {
			searchCalls++
			return discoveryactivity.SearchTweetsOutput{}, errors.New("browser unavailable")
		})

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)
	if searchCalls != 3 {
		t.Fatalf("default-version SearchTweets calls = %d, want historical 3", searchCalls)
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

// TestEventWorkflow_CancelDuringAttemptSpacing reproduces FF-015's production
// failure point. Canceling the durable timer after attempt 1 must terminate the
// workflow; it must not enter another attempt or finalize a removed event.
func TestEventWorkflow_CancelDuringAttemptSpacing(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)

	searchCalls := 0
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, _ discoveryactivity.SearchTweetsInput) (discoveryactivity.SearchTweetsOutput, error) {
			searchCalls++
			return discoveryactivity.SearchTweetsOutput{StopReason: "empty"}, nil
		})
	env.RegisterDelayedCallback(env.CancelWorkflow, 30*time.Second)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireCanceled(t, env)
	if searchCalls != 1 {
		t.Fatalf("SearchTweets called %d times, want 1 before spacing cancellation", searchCalls)
	}
	env.AssertNotCalled(t, "MarkDownstreamComplete", mock.Anything, mock.Anything)
}

// TestEventWorkflow_CancelDuringSearch covers the consumer's no-future state:
// while the producer is blocked in SearchTweets, the selector has no child or
// activity future. A canceled Await must return once, not spin.
func TestEventWorkflow_CancelDuringSearch(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)

	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		After(10*time.Minute).
		Return(discoveryactivity.SearchTweetsOutput{}, nil).
		Once()
	env.RegisterDelayedCallback(env.CancelWorkflow, 30*time.Second)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireCanceled(t, env)
	env.AssertNumberOfCalls(t, "SearchTweets", 1)
	env.AssertNotCalled(t, "MarkDownstreamComplete", mock.Anything, mock.Anything)
}

// TestEventWorkflow_CancelWithChildPending proves cancellation also terminates
// while the consumer is waiting for a VideoWorkflow future. The child inherits
// cancellation; the parent must neither start another search nor finalize.
func TestEventWorkflow_CancelWithChildPending(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := baseEventEnv(&s)

	tweetURL := "https://x.com/u/status/1111111111111111111"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}},
			Count:  1,
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, mock.Anything).
		After(10*time.Minute).
		Return(workflow.VideoWorkflowOutput{Outcome: "rejected"}, nil).
		Once()
	env.RegisterDelayedCallback(env.CancelWorkflow, 30*time.Second)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireCanceled(t, env)
	env.AssertNumberOfCalls(t, "SearchTweets", 1)
	env.AssertNumberOfCalls(t, "VideoWorkflow", 1)
	env.AssertNotCalled(t, "MarkDownstreamComplete", mock.Anything, mock.Anything)
}

// TestEventWorkflow_CancelWithVisionPending verifies the pipeline does not
// schedule forensic or cleanup activities after its root context is canceled.
// The monitor's destroy path owns cleanup for a removed event.
func TestEventWorkflow_CancelWithVisionPending(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := baseEventEnv(&s)

	tweetURL := "https://x.com/u/status/1111111111111111111"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}},
			Count:  1,
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, mock.Anything).
		Return(passedChild(tweetURL, "md5", "staging/clip.mp4", 1280, 720, 7000, 900_000, []uint64{1}), nil).
		Once()
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		After(10*time.Minute).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified"}, nil).
		Once()
	env.RegisterDelayedCallback(env.CancelWorkflow, 30*time.Second)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireCanceled(t, env)
	env.AssertNumberOfCalls(t, "ValidateClip", 1)
	env.AssertNotCalled(t, "PromoteAndPersist", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "UpsertCandidateOutcome", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "DeleteStaging", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "MarkDownstreamComplete", mock.Anything, mock.Anything)
}

func TestEventWorkflow_PermanentVisionErrorDoesNotRetry(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := visionFailureEnv(&s, temporal.NewNonRetryableApplicationError(
		"model not found", "vision_llm_permanent", nil,
	))

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireDone(t, env)
	env.AssertNumberOfCalls(t, "ValidateClip", 1)
	env.AssertNumberOfCalls(t, "UpsertCandidateOutcome", 1)
	env.AssertNumberOfCalls(t, "DeleteStaging", 1)
}

func TestEventWorkflow_TransientVisionErrorRetriesThreeTimes(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := visionFailureEnv(&s, errors.New("llm unavailable"))

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireDone(t, env)
	env.AssertNumberOfCalls(t, "ValidateClip", 3)
	env.AssertNumberOfCalls(t, "UpsertCandidateOutcome", 1)
	env.AssertNumberOfCalls(t, "DeleteStaging", 1)
}
