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
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	livefeedactivity "github.com/vedantadhobley/found-footy/internal/activity/livefeed"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

const (
	ff007RecoveryChangeIDForTest = "ff-007-failed-run-recovery"
	ff017RestartChangeIDForTest  = "ff-017-browser-restart-retry"
)

// newDiscoveryEnv sets up a test env with EventWorkflow +
// discovery activities registered. Individual tests attach OnActivity
// mocks before ExecuteWorkflow.
// baseEventEnv registers the workflows + activities + the always-present
// config/alias/complete stubs, but attaches NO child/pipeline mocks — so a
// test is free to set the child outcome it wants (testify picks the
// first-registered matching mock, so defaults can't be overridden).
func baseEventEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	return baseEventEnvWithRecovery(s,
		discoveryactivity.LoadEventRecoveryStateOutput{},
		videoactivity.LoadEventAssetsOutput{},
	)
}

// baseEventEnvWithRecovery lets restart tests seed the durable state a new
// EventWorkflow execution restores. Ordinary tests use baseEventEnv's empty
// projections and retain their original first-run shape.
func baseEventEnvWithRecovery(
	s *testsuite.WorkflowTestSuite,
	recovery discoveryactivity.LoadEventRecoveryStateOutput,
	assets videoactivity.LoadEventAssetsOutput,
	discoveryConfig ...discoveryactivity.GetDiscoveryConfigOutput,
) *testsuite.TestWorkflowEnvironment {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflow.EventWorkflow)
	env.RegisterWorkflow(workflow.VideoWorkflow)
	env.RegisterActivity(&discoveryactivity.Activities{})
	env.RegisterActivity(&visionactivity.Activities{})
	env.RegisterActivity(&videoactivity.PersistActivities{})
	env.RegisterActivity(&livefeedactivity.Activities{})
	// Default GetDiscoveryConfig stub. MaxAttempts=10 matches the
	// pre-#162 hardcoded value that existing tests were written
	// against (`want 10` assertions in AttemptsRun tests). Tests that need a
	// different value inject it before the mock is registered.
	// AttemptSpacing stays realistic (60s) but TestWorkflowEnvironment
	// auto-fires timers so real wall-clock waits don't happen.
	config := discoveryactivity.GetDiscoveryConfigOutput{
		MaxAttempts:    10,
		AttemptSpacing: 60 * time.Second,
		MaxAgeMinutes:  3,
		QueryTimeout:   2 * time.Minute,
	}
	if len(discoveryConfig) > 0 {
		config = discoveryConfig[0]
	}
	env.OnActivity("GetDiscoveryConfig", mock.Anything, mock.Anything).
		Return(config, nil).Maybe()
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
	env.OnActivity("LoadEventRecoveryState", mock.Anything, mock.Anything).
		Return(recovery, nil).Maybe()
	env.OnActivity("RecordDiscoveryProgress", mock.Anything, mock.Anything).
		Return(nil).Maybe()
	env.OnActivity("LoadEventAssets", mock.Anything, mock.Anything).
		Return(assets, nil).Maybe()
	// Default event.video publish stub — the pipeline fires it after a
	// promote/supersede changes the clip set; .Maybe() so tests that never
	// promote don't need it. Tests asserting the ping override explicitly.
	env.OnActivity("PublishEventVideo", mock.Anything, mock.Anything).Return(nil).Maybe()
	return env
}

// newDiscoveryEnv = base + pipeline defaults that make the SEARCH-LOOP tests
// behave: every spawned VideoWorkflow child returns "rejected" (no downstream
// vision/promote), so those tests exercise only the producer. Pipeline tests
// use baseEventEnv directly and set their own child/vision/promote mocks.
func newDiscoveryEnv(
	s *testsuite.WorkflowTestSuite,
	discoveryConfig ...discoveryactivity.GetDiscoveryConfigOutput,
) *testsuite.TestWorkflowEnvironment {
	env := baseEventEnvWithRecovery(s,
		discoveryactivity.LoadEventRecoveryStateOutput{},
		videoactivity.LoadEventAssetsOutput{},
		discoveryConfig...,
	)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, mock.Anything).
		Return(workflow.VideoWorkflowOutput{Outcome: "rejected", RejectReason: "test-default"}, nil).Maybe()
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "rejected"}, nil).Maybe()
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(videoactivity.PromoteAndPersistOutput{AssetID: uuid.New(), ShareID: "s_test", Inserted: true}, nil).Maybe()
	env.OnActivity("BumpAssetPopularity", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("RecordCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()
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

// TestEventWorkflow_FailedRunRestoresDurableProgress covers FF-007's
// replacement-execution boundary. Nine completed searches resume at ten; a
// pending candidate is re-driven, a terminal candidate is excluded, and the
// existing live asset remains in the dedup/output pool.
func TestEventWorkflow_FailedRunRestoresDurableProgress(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	const (
		terminalURL = "https://x.com/u/status/1111111111111111111"
		pendingURL  = "https://x.com/u/status/2222222222222222222"
		newURL      = "https://x.com/u/status/3333333333333333333"
	)
	env := baseEventEnvWithRecovery(&s,
		discoveryactivity.LoadEventRecoveryStateOutput{
			AttemptsCompleted: 9,
			Candidates: []discoveryactivity.RecoveryCandidate{
				{TweetURL: terminalURL, Pending: false},
				{TweetURL: pendingURL, Pending: true},
			},
		},
		videoactivity.LoadEventAssetsOutput{Assets: []videoactivity.RestoredEventAsset{{
			AssetID: uuid.New(), MD5: "existing-md5", FrameHashes: []uint64{1, 2, 3},
			Width: 1280, Height: 720, DurationMS: 10_000, FileSizeBytes: 1_000_000,
			Popularity: 2, Verified: true,
		}}},
	)

	env.OnActivity("SearchTweets", mock.Anything, mock.MatchedBy(func(in discoveryactivity.SearchTweetsInput) bool {
		return len(in.ExcludeURLs) == 2 && in.ExcludeURLs[0] == terminalURL && in.ExcludeURLs[1] == pendingURL
	})).Return(discoveryactivity.SearchTweetsOutput{
		// The service may still echo excluded rows. Workflow ownership must
		// suppress both and process only the genuinely new URL.
		Videos: []twitter.VideoRef{
			{TweetURL: terminalURL}, {TweetURL: pendingURL}, {TweetURL: newURL},
		},
		Count: 3,
	}, nil).Once()
	env.OnActivity("StoreCandidate", mock.Anything, mock.MatchedBy(func(in discoveryactivity.StoreCandidateInput) bool {
		return in.SearchAttempt == 10 && in.TweetURL == newURL
	})).Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil).Once()
	for _, url := range []string{pendingURL, newURL} {
		env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(url)).
			Return(workflow.VideoWorkflowOutput{Outcome: workflow.VideoOutcomeRejected, RejectReason: "test"}, nil).Once()
	}
	env.OnActivity("RecordCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	var out workflow.EventWorkflowOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if out.AttemptsRun != 10 || out.CandidatesFound != 3 || out.AssetsKept != 1 {
		t.Errorf("recovered output = attempts %d/candidates %d/assets %d, want 10/3/1",
			out.AttemptsRun, out.CandidatesFound, out.AssetsKept)
	}
	env.AssertNumberOfCalls(t, "VideoWorkflow", 2)
}

// TestEventWorkflow_DefaultVersionPreservesPreRecoveryCommandSequence proves
// a workflow started before FF-007 does not insert recovery activities or
// progress writes into its existing Temporal history during replay.
func TestEventWorkflow_DefaultVersionPreservesPreRecoveryCommandSequence(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&s)
	env.OnGetVersion(ff007RecoveryChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
		Return(sdkworkflow.DefaultVersion).
		Once()
	env.OnGetVersion(ff017RestartChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
		Return(sdkworkflow.DefaultVersion).
		Once()
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{}, nil)

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	env.AssertNotCalled(t, "LoadEventAssets", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "LoadEventRecoveryState", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "RecordDiscoveryProgress", mock.Anything, mock.Anything)
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
	env.AssertNotCalled(t, "RecordCandidateOutcome", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "DeleteStaging", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "MarkDownstreamComplete", mock.Anything, mock.Anything)
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

func requireCanceled(t *testing.T, env *testsuite.TestWorkflowEnvironment) {
	t.Helper()
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete after cancellation")
	}
	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("workflow error = %v, want canceled", err)
	}
}

// failedCandidateEnv wires one persisted candidate to one child result. The
// producer sees the same URL on later attempts, so workflow-local dedup keeps
// this a single child while the normal discovery loop still completes.
func failedCandidateEnv(
	s *testsuite.WorkflowTestSuite,
	childOut workflow.VideoWorkflowOutput,
	childErr error,
) (*testsuite.TestWorkflowEnvironment, string) {
	env := baseEventEnv(s)
	tweetURL := "https://x.com/u/status/1111111111111111111"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}},
			Count:  1,
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil).
		Once()
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(tweetURL)).
		Return(childOut, childErr).
		Once()
	return env, tweetURL
}

// failedCandidateIs matches the durable terminal update for one candidate.
func failedCandidateIs(tweetURL string, reason workflow.VideoWorkflowFailureReason) interface{} {
	return mock.MatchedBy(func(in discoveryactivity.RecordCandidateOutcomeInput) bool {
		return in.TweetURL == tweetURL &&
			in.Outcome == discoveryactivity.OutcomeFailed &&
			in.RejectReason == string(reason)
	})
}

// TestEventWorkflow_DownloadFailureStampsCandidate covers the no-staging
// failure branch and proves vision and cleanup are not scheduled.
func TestEventWorkflow_DownloadFailureStampsCandidate(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, tweetURL := failedCandidateEnv(&s, workflow.VideoWorkflowOutput{
		Outcome:       workflow.VideoOutcomeFailed,
		FailureReason: workflow.VideoFailureDownload,
	}, nil)
	env.OnActivity("RecordCandidateOutcome", mock.Anything,
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
	env.OnActivity("RecordCandidateOutcome", mock.Anything,
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
	env.OnActivity("RecordCandidateOutcome", mock.Anything,
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

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	requireDone(t, env)
	env.AssertNotCalled(t, "RecordCandidateOutcome", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "DeleteStaging", mock.Anything, mock.Anything)
}

// TestEventWorkflow_Pipeline_VerifyAndDedup — the #164c-b consumer path: two
// candidates, both children pass with the SAME md5 (exact dup). The first is
// vision-verified and promoted; the second collapses onto it. Net: one asset
// kept, one duplicate, PromoteAndPersist called exactly once, assets_surfaced.
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
	env.OnActivity("RecordCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()

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
	env.OnActivity("RecordCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()
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

// ─── #171: post-vision category-scoped dedup + quality winner-selection ──────
//
// These use two candidates with DIFFERENT md5 (so the gate's md5 check never
// fires) but perceptually-matching frame hashes, and drive vision + quality per
// candidate to exercise the post-vision path. With the test config's
// MaxHamming=0 / MinRunFrames→1, identical frame slices match and the consumer
// logic (not the Match algorithm, covered in match_test.go) is what's under test.

// tweetIs / stagingIs route per-candidate mocks by the field the workflow sets.
func tweetIs(url string) interface{} {
	return mock.MatchedBy(func(in workflow.VideoWorkflowInput) bool { return in.TweetURL == url })
}
func stagingIs(key string) interface{} {
	return mock.MatchedBy(func(in visionactivity.ValidateClipInput) bool { return in.StagingKey == key })
}

// passedChild builds a "passed" VideoWorkflow result for one candidate.
func passedChild(url, md5, staging string, w, h, durMS int, size int64, frames []uint64) workflow.VideoWorkflowOutput {
	return workflow.VideoWorkflowOutput{
		Outcome: "passed", TweetURL: url, MD5: md5, StagingKey: staging,
		FrameHashes: frames, Width: w, Height: h, DurationMS: durMS, SizeBytes: size,
	}
}

func requireDone(t *testing.T, env *testsuite.TestWorkflowEnvironment) {
	t.Helper()
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow didn't complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow errored: %v", err)
	}
}

// twoCandidateEnv wires the search → two-child scaffolding shared by the
// post-vision tests: SearchTweets returns t1+t2, StoreCandidate + the noise
// activities are stubbed. Callers attach the per-candidate child/vision/promote
// mocks. Returns the env plus the two tweet URLs.
func twoCandidateEnv(s *testsuite.WorkflowTestSuite) (*testsuite.TestWorkflowEnvironment, string, string) {
	env := baseEventEnv(s)
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("RecordCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("BumpAssetPopularity", mock.Anything, mock.Anything).Return(nil).Maybe()
	t1 := "https://x.com/u/status/1111111111111111111"
	t2 := "https://x.com/u/status/2222222222222222222"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{
				{TweetURL: t1, VideoPageURL: "vp1", DurationSeconds: 7},
				{TweetURL: t2, VideoPageURL: "vp2", DurationSeconds: 7},
			}, Count: 2, StopReason: "age",
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)
	return env, t1, t2
}

// TestEventWorkflow_Pipeline_CategoryScopedDedup — the load-bearing #171 fix.
// Two perceptually-identical clips (same frame hashes, different md5) land in
// DIFFERENT vision pools: one verified, one unverified. They must NOT collapse
// — pools never compare — so both are kept and nothing is superseded. Under the
// old pre-vision, category-blind gate these would have collapsed to one.
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

// TestEventWorkflow_Pipeline_QualitySupersede — two perceptually-identical
// VERIFIED clips, different md5, DIFFERENT quality. The lower-res clip is
// processed first (spawn order) and promoted; the higher-res clip then wins the
// pool and supersedes it. Net: both promoted, one supersede, one asset kept.
func TestEventWorkflow_Pipeline_QualitySupersede(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env, t1, t2 := twoCandidateEnv(&s)
	frames := []uint64{1, 2, 4, 8, 16, 32}

	// t1 low-res (processed first → incumbent), t2 high-res (upgrade → winner).
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(t1)).
		Return(passedChild(t1, "md5low", "s1", 640, 360, 7000, 400_000, frames), nil)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(t2)).
		Return(passedChild(t2, "md5high", "s2", 1920, 1080, 7000, 2_500_000, frames), nil)
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: pInt(71)}, nil)

	promoteCalls, supersedeCalls, loserCount := 0, 0, 0
	env.OnActivity("PromoteAndPersist", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.PromoteAndPersistInput) (videoactivity.PromoteAndPersistOutput, error) {
			promoteCalls++
			// Deterministic id per md5 so the winner supersedes the right loser.
			id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(in.EventID.String()+":"+in.MD5))
			return videoactivity.PromoteAndPersistOutput{AssetID: id, ShareID: "s_" + in.MD5, Inserted: true}, nil
		})
	env.OnActivity("SupersedeAssets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.SupersedeAssetsInput) error {
			supersedeCalls++
			loserCount += len(in.LoserAssetIDs)
			return nil
		})

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	var out workflow.EventWorkflowOutput
	_ = env.GetWorkflowResult(&out)
	if out.AssetsKept != 1 {
		t.Errorf("AssetsKept = %d, want 1 (cluster collapses to the winner)", out.AssetsKept)
	}
	if promoteCalls != 2 {
		t.Errorf("PromoteAndPersist called %d times, want 2 (both promoted; loser then superseded)", promoteCalls)
	}
	if supersedeCalls != 1 || loserCount != 1 {
		t.Errorf("SupersedeAssets calls=%d losers=%d, want 1/1 (higher-res supersedes lower)", supersedeCalls, loserCount)
	}
}
