// EventWorkflow discovery, recovery, and candidate-durability tests.
package workflow_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

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
	in := stdDiscoveryInput()
	const (
		terminalURL = "https://x.com/u/status/1111111111111111111"
		pendingURL  = "https://x.com/u/status/2222222222222222222"
		newURL      = "https://x.com/u/status/3333333333333333333"
	)
	env := baseEventEnvWithRecovery(&s,
		discoveryactivity.LoadEventRecoveryStateOutput{
			AttemptsCompleted: 9,
			Candidates: []discoveryactivity.RecoveryCandidate{
				{
					Evidence: discoverycontract.CandidateEvidence{
						EventID: in.EventID, FixtureID: in.FixtureID,
						SearchAttempt: 3, Query: "query", TweetURL: terminalURL,
						VideoPageURL: terminalURL,
					},
					State: ddiscovery.CandidateTerminal, TweetURL: terminalURL,
				},
				{
					Evidence: discoverycontract.CandidateEvidence{
						EventID: in.EventID, FixtureID: in.FixtureID,
						SearchAttempt: 4, Query: "query", TweetURL: pendingURL,
						VideoPageURL: pendingURL,
					},
					State: ddiscovery.CandidateObserved, TweetURL: pendingURL, Pending: true,
				},
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
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()

	env.ExecuteWorkflow(workflow.EventWorkflow, in)
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

// TestEventWorkflow_ObservationFailureDoesNotGateClipLaunch proves FF-034's
// critical-path boundary. Every candidate starts processing before the
// observation inserts are awaited. A failed insert leaves the attempt
// uncheckpointed and fails the execution only after the launched clip reaches
// its durable terminal state.
func TestEventWorkflow_ObservationFailureDoesNotGateClipLaunch(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := baseEventEnvWithRecovery(&s,
		discoveryactivity.LoadEventRecoveryStateOutput{},
		videoactivity.LoadEventAssetsOutput{},
		discoveryactivity.GetDiscoveryConfigOutput{
			MaxAttempts: 1, AttemptSpacing: time.Minute, MaxAgeMinutes: 3,
			QueryTimeout: 2 * time.Minute,
		},
	)
	const tweetURL = "https://x.com/u/status/1111111111111111111"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{Videos: []twitter.VideoRef{{
			TweetURL: tweetURL, TweetText: "goal clip", VideoPageURL: tweetURL,
			DurationSeconds: 12, Username: "u", AgeMinutes: 0.5,
		}}, Count: 1}, nil).Once()
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{}, errors.New("postgres unavailable"))
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(tweetURL)).
		Return(workflow.VideoWorkflowOutput{Outcome: workflow.VideoOutcomeRejected, RejectReason: "test"}, nil).Once()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.MatchedBy(
		func(in discoveryactivity.UpsertCandidateOutcomeInput) bool {
			return in.Evidence.TweetURL == tweetURL &&
				in.Evidence.TweetText == "goal clip" &&
				in.Evidence.Username == "u" &&
				in.Evidence.SearchAttempt == 1 &&
				in.Outcome == discoveryactivity.OutcomeRejected
		},
	)).Return(nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not close after observation failure")
	}
	if err := env.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "persist observed candidate") {
		t.Fatalf("workflow error = %v, want observation durability failure", err)
	}
	env.AssertNumberOfCalls(t, "VideoWorkflow", 1)
	env.AssertNumberOfCalls(t, "StoreCandidate", discoveryPGRetryAttemptsForTest)
	env.AssertNumberOfCalls(t, "UpsertCandidateOutcome", 1)
	env.AssertNotCalled(t, "RecordDiscoveryProgress", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "MarkDownstreamComplete", mock.Anything, mock.Anything)
}

// TestEventWorkflow_TerminalPersistenceFailureBlocksParentCompletion proves
// the parent cannot report success after a candidate finished only in workflow
// memory. The terminal UPSERT exhausts its retries and the checklist remains
// open for failed-run recovery.
func TestEventWorkflow_TerminalPersistenceFailureBlocksParentCompletion(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := baseEventEnvWithRecovery(&s,
		discoveryactivity.LoadEventRecoveryStateOutput{},
		videoactivity.LoadEventAssetsOutput{},
		discoveryactivity.GetDiscoveryConfigOutput{
			MaxAttempts: 1, AttemptSpacing: time.Minute, MaxAgeMinutes: 3,
			QueryTimeout: 2 * time.Minute,
		},
	)
	const tweetURL = "https://x.com/u/status/2222222222222222222"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{Videos: []twitter.VideoRef{{
			TweetURL: tweetURL, VideoPageURL: tweetURL,
		}}, Count: 1}, nil).Once()
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil).Once()
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(tweetURL)).
		Return(workflow.VideoWorkflowOutput{Outcome: workflow.VideoOutcomeRejected, RejectReason: "test"}, nil).Once()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).
		Return(errors.New("postgres unavailable"))

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not close after terminal persistence failure")
	}
	if err := env.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "persist terminal candidate") {
		t.Fatalf("workflow error = %v, want terminal durability failure", err)
	}
	env.AssertNumberOfCalls(t, "UpsertCandidateOutcome", discoveryPGRetryAttemptsForTest)
	env.AssertNotCalled(t, "MarkDownstreamComplete", mock.Anything, mock.Anything)
}

// TestEventWorkflow_PreFF034HistoryKeepsLegacyCandidateWrites proves a running
// history created before FF-034 retains StoreCandidate-before-child and the
// legacy RecordCandidateOutcome activity rather than gaining a new command.
func TestEventWorkflow_PreFF034HistoryKeepsLegacyCandidateWrites(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := baseEventEnv(&s)
	env.OnGetVersion(ff034DurabilityChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
		Return(sdkworkflow.DefaultVersion).Once()
	const tweetURL = "https://x.com/u/status/3333333333333333333"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{Videos: []twitter.VideoRef{{
			TweetURL: tweetURL, VideoPageURL: tweetURL,
		}}, Count: 1}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil).Once()
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, tweetIs(tweetURL)).
		Return(workflow.VideoWorkflowOutput{Outcome: workflow.VideoOutcomeRejected, RejectReason: "test"}, nil).Once()
	env.OnActivity("RecordCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)

	env.AssertNumberOfCalls(t, "RecordCandidateOutcome", 1)
	env.AssertNotCalled(t, "UpsertCandidateOutcome", mock.Anything, mock.Anything)
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
	env.OnGetVersion(ff034DurabilityChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
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
