// Fixed-window workflow tests cover probe transitions, failed-run recovery, and old histories.
package workflow_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

// TestEventWorkflowFixedWindowTransitions verifies actual serialized activity
// inputs and progress checkpoints, including a 429 that preserves the logical slot.
func TestEventWorkflowFixedWindowTransitions(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&suite, discoveryactivity.GetDiscoveryConfigOutput{
		MaxAttempts: 7, MaxUnavailableAttempts: 2, MaxAgeMinutes: 3,
		AttemptSpacing: 5 * time.Minute, QueryTimeout: time.Minute,
	})
	stops := []string{"max_scrolls", "age", "consecutive_seen", "feed_timeout", "max_scrolls", "explicit_empty", "age", "consecutive_seen"}
	wantAllowed := []bool{false, false, true, true, false, false, false, true}
	wantNext := []bool{false, true, true, false, false, false, true, true}
	var calls int
	var floor time.Time
	var progress []discoveryactivity.RecordDiscoveryProgressInput
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
		if info.ActivityType.Name == "RecordDiscoveryProgress" {
			var in discoveryactivity.RecordDiscoveryProgressInput
			require.NoError(t, args.Get(&in))
			progress = append(progress, in)
		}
	})
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in discoveryactivity.SearchTweetsInput) (discoveryactivity.SearchTweetsOutput, error) {
			require.Less(t, calls, len(stops))
			require.NotNil(t, in.Window)
			require.Zero(t, in.MaxAgeMinutes)
			require.Equal(t, wantAllowed[calls], in.Window.AllowSeenStop)
			if floor.IsZero() {
				floor = in.Window.EarliestTweetAt
			}
			require.True(t, floor.Equal(in.Window.EarliestTweetAt), "window moved after spacing/outage")
			out := discoveryactivity.SearchTweetsOutput{ResultState: twittercontract.ResultRendered, StopReason: stops[calls]}
			if calls == 3 {
				out.ResultState = twittercontract.ResultUpstreamError
				out.Evidence.TimelineStatus = 429
			}
			if calls == 5 {
				out.ResultState = twittercontract.ResultExplicitEmpty
			}
			calls++
			return out, nil
		})
	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)
	require.Equal(t, len(stops), calls)
	require.Len(t, progress, len(stops))
	for i, checkpoint := range progress {
		require.NotNil(t, checkpoint.Window)
		require.True(t, floor.Equal(checkpoint.Window.EarliestTweetAt))
		require.Equal(t, wantNext[i], checkpoint.Window.AllowSeenStop)
	}
	require.Equal(t, 3, progress[3].Attempt)
	require.Equal(t, 1, progress[3].UnavailableAttempts)
	require.Equal(t, 429, progress[3].LastSearchEvidence.TimelineStatus)
}

// TestEventWorkflowFixedWindowRecoveryDistrustsPartialURLs preserves the old
// floor despite changed config, but cannot join known IDs from a failed run.
func TestEventWorkflowFixedWindowRecoveryDistrustsPartialURLs(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	floor := time.Date(2026, 9, 9, 19, 57, 0, 0, time.UTC)
	env := baseEventEnvWithRecovery(&suite, discoveryactivity.LoadEventRecoveryStateOutput{
		AttemptsCompleted: 4,
		Window:            &twittercontract.SearchWindow{EarliestTweetAt: floor, AllowSeenStop: true},
		Candidates:        []discoveryactivity.RecoveryCandidate{{TweetURL: "https://x.com/u/status/111111111111111111"}},
	}, videoactivity.LoadEventAssetsOutput{}, discoveryactivity.GetDiscoveryConfigOutput{
		MaxAttempts: 5, MaxUnavailableAttempts: 2, MaxAgeMinutes: 9, AttemptSpacing: time.Minute, QueryTimeout: time.Minute,
	})
	// The existing durability contract needs terminal evidence to seed exclusions.
	env.OnGetVersion(ff034DurabilityChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).Return(sdkworkflow.DefaultVersion)
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in discoveryactivity.SearchTweetsInput) (discoveryactivity.SearchTweetsOutput, error) {
			require.NotNil(t, in.Window)
			require.True(t, floor.Equal(in.Window.EarliestTweetAt))
			require.False(t, in.Window.AllowSeenStop)
			require.Len(t, in.ExcludeURLs, 1)
			return discoveryactivity.SearchTweetsOutput{ResultState: twittercontract.ResultRendered, StopReason: "age"}, nil
		}).Once()
	input := stdDiscoveryInput()
	input.FirstSeenAt = time.Time{} // Real activity uses the durable event timestamp.
	env.ExecuteWorkflow(workflow.EventWorkflow, input)
	requireDone(t, env)
	env.AssertNumberOfCalls(t, "SearchTweets", 1)
}

// TestEventWorkflowLegacySearchWindow keeps pre-FF-091 activity inputs relative.
func TestEventWorkflowLegacySearchWindow(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := newDiscoveryEnv(&suite, discoveryactivity.GetDiscoveryConfigOutput{
		MaxAttempts: 1, MaxAgeMinutes: 3, QueryTimeout: time.Minute,
	})
	env.OnGetVersion("ff-091-fixed-search-window", sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).Return(sdkworkflow.DefaultVersion)
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
		switch info.ActivityType.Name {
		case "LoadEventRecoveryState":
			var in discoveryactivity.LoadEventRecoveryStateInput
			require.NoError(t, args.Get(&in))
			require.Zero(t, in.SearchLookbackMinutes)
		case "RecordDiscoveryProgress":
			var in discoveryactivity.RecordDiscoveryProgressInput
			require.NoError(t, args.Get(&in))
			require.Nil(t, in.Window)
		}
	})
	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in discoveryactivity.SearchTweetsInput) (discoveryactivity.SearchTweetsOutput, error) {
			require.Nil(t, in.Window)
			require.Equal(t, 3, in.MaxAgeMinutes)
			return discoveryactivity.SearchTweetsOutput{ResultState: twittercontract.ResultRendered}, nil
		}).Once()
	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)
}
