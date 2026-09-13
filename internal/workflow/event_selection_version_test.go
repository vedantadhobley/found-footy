// Full EventWorkflow tests pin versioned recovery payloads and completion notifications.
package workflow

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	livefeedactivity "github.com/vedantadhobley/found-footy/internal/activity/livefeed"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
)

// TestEventSelectionVersionAndRecovery uses the real entrypoint with a recovered exact source.
func TestEventSelectionVersionAndRecovery(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterWorkflow(EventWorkflow)
			env.RegisterActivity(&discoveryactivity.Activities{})
			env.RegisterActivity(&videoactivity.Activities{})
			env.RegisterActivity(&videoactivity.PersistActivities{})
			env.RegisterActivity(&livefeedactivity.Activities{})
			version := sdkworkflow.DefaultVersion
			if enabled {
				version = 1
			}
			env.OnGetVersion(ff081ReversibleSelectionChangeID, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).Return(version)
			eventID, assetID := uuid.New(), uuid.New()
			md5 := strings.Repeat("aa", 16)
			asset := videoactivity.RestoredEventAsset{AssetID: assetID, MD5: md5, FrameHashes: []uint64{1, 2, 3}, Width: 1280, Height: 720,
				DurationMS: 10000, FileSizeBytes: 1000000, Popularity: 2, Verified: true}
			env.OnActivity("GetDiscoveryConfig", mock.Anything, mock.Anything).Return(discoveryactivity.GetDiscoveryConfigOutput{
				MaxAttempts: 1, MaxUnavailableAttempts: 1, AttemptSpacing: time.Minute, MaxAgeMinutes: 3, QueryTimeout: time.Minute,
				MaxHamming: 12, MinRunFrames: 30, MaxGapFrames: 3, LongMaxHamming: 16, LongMinRunFrames: 50, LongMaxGapFrames: 5}, nil)
			env.OnActivity("FetchTeamAliases", mock.Anything, mock.Anything).Return(discoveryactivity.FetchTeamAliasesOutput{CanonicalName: "Liverpool", Found: true}, nil)
			env.OnActivity("LoadEventAssets", mock.Anything, mock.Anything).Return(func(_ context.Context, in videoactivity.LoadEventAssetsInput) (videoactivity.LoadEventAssetsOutput, error) {
				require.Equal(t, enabled, in.ConsistentSelection)
				return videoactivity.LoadEventAssetsOutput{Assets: []videoactivity.RestoredEventAsset{asset}}, nil
			}).Once()
			env.OnActivity("LoadEventRecoveryState", mock.Anything, mock.Anything).Return(discoveryactivity.LoadEventRecoveryStateOutput{
				AttemptsCompleted: 1, Window: &twittercontract.SearchWindow{EarliestTweetAt: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)},
				Candidates: []discoveryactivity.RecoveryCandidate{{Pending: true, TweetURL: "exact", State: ddiscovery.CandidateObserved,
					Evidence: discoverycontract.CandidateEvidence{EventID: eventID, FixtureID: 1, SearchAttempt: 1, Query: "query", TweetURL: "exact", VideoPageURL: "page"}}}}, nil)
			env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).Return(videoactivity.DownloadAndStageOutput{
				Outcome: videoactivity.OutcomePassed, MD5: md5, StagingKey: "staging/exact", Width: 1280, Height: 720, DurationMS: 10000, SizeBytes: 1000000}, nil).Once()
			env.OnActivity("CommitClipPlacement", mock.Anything, mock.Anything).Return(func(_ context.Context, in videoactivity.CommitClipPlacementInput) (videoactivity.CommitClipPlacementOutput, error) {
				require.Equal(t, enabled, in.Selection != nil)
				require.Nil(t, in.Validation, "exact recurrence must not synthesize vision evidence")
				out := videoactivity.CommitClipPlacementOutput{WinnerAssetID: assetID, Announce: true}
				if enabled {
					require.Equal(t, 50, in.Selection.Policy.LongMinRun)
					current := asset
					current.Popularity = 3
					out.Selection = &videoactivity.PlacementSelectionOutput{State: videoactivity.LoadEventAssetsOutput{Assets: []videoactivity.RestoredEventAsset{current}}}
				}
				return out, nil
			}).Once()
			env.OnActivity("MarkDownstreamComplete", mock.Anything, mock.Anything).Return(discoveryactivity.MarkDownstreamCompleteOutput{RowsUpdated: 1}, nil).Once()
			env.OnActivity("PublishEventUpdate", mock.Anything, mock.Anything).Return(nil).Twice()
			env.ExecuteWorkflow(EventWorkflow, EventWorkflowInput{EventID: eventID, FixtureID: 1, TeamID: 40, TeamName: "Liverpool", PlayerName: "Salah", Minute: 23})
			require.NoError(t, env.GetWorkflowError())
			var out EventWorkflowOutput
			require.NoError(t, env.GetWorkflowResult(&out))
			require.True(t, out.Completed)
			require.Equal(t, 1, out.AssetsKept)
			env.AssertExpectations(t)
		})
	}
}
