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
	"sync"
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
	ff007RecoveryChangeIDForTest     = "ff-007-failed-run-recovery"
	ff017RestartChangeIDForTest      = "ff-017-browser-restart-retry"
	ff022PreHashChangeIDForTest      = "ff-022-pre-hash-md5-claim"
	ff034DurabilityChangeIDForTest   = "ff-034-candidate-durability"
	ff060DownloadFailureIDForTest    = "ff-060-download-failure-detail"
	ff061AvailabilityChangeIDForTest = "ff-061-search-availability"
	ff065ExactFollowerIDForTest      = "ff-065-exact-follower-outcome"
	discoveryPGRetryAttemptsForTest  = 5
)

type workflowLogCapture struct {
	mu      sync.Mutex
	entries [][]interface{}
}

func (*workflowLogCapture) Debug(string, ...interface{}) {}
func (l *workflowLogCapture) Info(_ string, fields ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, append([]interface{}(nil), fields...))
}
func (*workflowLogCapture) Warn(string, ...interface{})  {}
func (*workflowLogCapture) Error(string, ...interface{}) {}

func (l *workflowLogCapture) actionPhases() (map[string]bool, map[string]bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	actions := make(map[string]bool)
	phases := make(map[string]bool)
	for _, entry := range l.entries {
		for i := 0; i+1 < len(entry); i += 2 {
			key, ok := entry[i].(string)
			if !ok {
				continue
			}
			value, _ := entry[i+1].(string)
			switch key {
			case "action":
				actions[value] = true
			case "phase":
				phases[value] = true
			}
		}
	}
	return actions, phases
}

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
	return baseEventEnvWithOptions(s, recovery, assets, false, false, discoveryConfig...)
}

// preHashEventEnv activates FF-022 for tests that exercise parent-owned
// download/stage and exact-MD5 claims. The normal base keeps the old child
// command sequence so the existing suite also guards replay compatibility.
func preHashEventEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	return baseEventEnvWithOptions(s,
		discoveryactivity.LoadEventRecoveryStateOutput{},
		videoactivity.LoadEventAssetsOutput{},
		true,
		true,
	)
}

// preFF065PreHashEventEnv keeps FF-022's parent-owned hash path while forcing
// FF-065 to DefaultVersion, which guards replay of the former immediate
// follower-duplicate command sequence.
func preFF065PreHashEventEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	return baseEventEnvWithOptions(s,
		discoveryactivity.LoadEventRecoveryStateOutput{},
		videoactivity.LoadEventAssetsOutput{},
		true,
		false,
	)
}

func baseEventEnvWithOptions(
	s *testsuite.WorkflowTestSuite,
	recovery discoveryactivity.LoadEventRecoveryStateOutput,
	assets videoactivity.LoadEventAssetsOutput,
	preHashMD5 bool,
	deferExactFollowerOutcomes bool,
	discoveryConfig ...discoveryactivity.GetDiscoveryConfigOutput,
) *testsuite.TestWorkflowEnvironment {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflow.EventWorkflow)
	env.RegisterWorkflow(workflow.VideoWorkflow)
	env.RegisterActivity(&discoveryactivity.Activities{})
	env.RegisterActivity(&visionactivity.Activities{})
	env.RegisterActivity(&videoactivity.Activities{})
	env.RegisterActivity(&videoactivity.PersistActivities{})
	env.RegisterActivity(&livefeedactivity.Activities{})
	version := sdkworkflow.DefaultVersion
	if preHashMD5 {
		version = sdkworkflow.Version(1)
	}
	env.OnGetVersion(ff022PreHashChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
		Return(version).Maybe()
	exactFollowerVersion := sdkworkflow.DefaultVersion
	if deferExactFollowerOutcomes {
		exactFollowerVersion = sdkworkflow.Version(1)
	}
	env.OnGetVersion(ff065ExactFollowerIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
		Return(exactFollowerVersion).Maybe()
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
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()
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

func visionFailureEnv(s *testsuite.WorkflowTestSuite, visionErr error) *testsuite.TestWorkflowEnvironment {
	env := baseEventEnv(s)
	tweetURL := "https://x.com/u/status/1111111111111111111"
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}},
			Count:  1, StopReason: "age",
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)
	env.OnWorkflow(workflow.VideoWorkflow, mock.Anything, mock.Anything).
		Return(passedChild(tweetURL, "md5", "staging/clip.mp4", 1280, 720, 7000, 900_000, []uint64{1}), nil).
		Once()
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{}, visionErr)
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Once()
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Once()
	return env
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
	return mock.MatchedBy(func(in discoveryactivity.UpsertCandidateOutcomeInput) bool {
		return in.Evidence.TweetURL == tweetURL &&
			in.Outcome == discoveryactivity.OutcomeFailed &&
			in.RejectReason == string(reason)
	})
}

// TestEventWorkflow_DownloadFailureStampsCandidate covers the no-staging
// failure branch and proves vision and cleanup are not scheduled.

func tweetIs(url string) interface{} {
	return mock.MatchedBy(func(in workflow.VideoWorkflowInput) bool { return in.TweetURL == url })
}
func stagingIs(key string) interface{} {
	return mock.MatchedBy(func(in visionactivity.ValidateClipInput) bool { return in.StagingKey == key })
}

func downloadTweetIs(url string) interface{} {
	return mock.MatchedBy(func(in videoactivity.DownloadAndStageInput) bool { return in.TweetURL == url })
}

func hashStagingIs(key string) interface{} {
	return mock.MatchedBy(func(in videoactivity.HashVideoInput) bool { return in.StagingKey == key })
}

func candidateOutcomeIs(url string, outcome discoveryactivity.CandidateOutcome) interface{} {
	return mock.MatchedBy(func(in discoveryactivity.UpsertCandidateOutcomeInput) bool {
		return in.Evidence.TweetURL == url && in.Outcome == outcome
	})
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
func twoCandidateEnv(
	s *testsuite.WorkflowTestSuite,
	discoveryConfig ...discoveryactivity.GetDiscoveryConfigOutput,
) (*testsuite.TestWorkflowEnvironment, string, string) {
	env := baseEventEnvWithRecovery(s,
		discoveryactivity.LoadEventRecoveryStateOutput{},
		videoactivity.LoadEventAssetsOutput{},
		discoveryConfig...,
	)
	env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity("BumpAssetPopularity", mock.Anything, mock.Anything).Return(nil).Maybe()
	t1, t2 := wireTwoCandidateSearch(env)
	return env, t1, t2
}

func twoCandidatePreHashEnv(s *testsuite.WorkflowTestSuite) (*testsuite.TestWorkflowEnvironment, string, string) {
	env := preHashEventEnv(s)
	t1, t2 := wireTwoCandidateSearch(env)
	return env, t1, t2
}

func wireTwoCandidateSearch(env *testsuite.TestWorkflowEnvironment) (string, string) {
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
	return t1, t2
}
