// Workflow regressions keep reversible selection, exact credit and notification ordering together.
package workflow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	livefeedactivity "github.com/vedantadhobley/found-footy/internal/activity/livefeed"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
)

// TestSelectionPipelineRestoresBeforePublication includes exact A and bridge recurrences before publication.
func TestSelectionPipelineRestoresBeforePublication(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity(&videoactivity.PersistActivities{})
	env.RegisterActivity(&livefeedactivity.Activities{})
	eventID := uuid.New()
	var items []clip
	for i, hashes := range [][]uint64{{0, 0, 0}, {0, 0, 0, ^uint64(0), ^uint64(0), ^uint64(0)}, {^uint64(0), ^uint64(0), ^uint64(0)}} {
		md5 := strings.Repeat(fmt.Sprintf("%02x", i+1), 16)
		items = append(items, clip{md5: md5, assetID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(eventID.String()+":"+md5)),
			hashVersion: "test-v1", frameHashes: hashes, width: 1280, height: 720, durationMS: (i + 1) * 10000,
			fileSizeBytes: 1000000, verified: true, popularity: []int{2, 5, 7}[i], stagingKey: "staging/" + md5, tweetURL: fmt.Sprint(i)})
	}
	project := func(c clip, votes int) videoactivity.RestoredEventAsset {
		return videoactivity.RestoredEventAsset{AssetID: c.assetID, MD5: c.md5, HashVersion: c.hashVersion, FrameHashes: c.frameHashes,
			Width: c.width, Height: c.height, DurationMS: c.durationMS, FileSizeBytes: c.fileSizeBytes, Verified: true, Popularity: votes}
	}
	calls, published := 0, 0
	var p *pipeline
	env.OnActivity("CommitClipPlacement", mock.Anything, mock.Anything).Return(func(_ context.Context, in videoactivity.CommitClipPlacementInput) (videoactivity.CommitClipPlacementOutput, error) {
		require.NotNil(t, in.Selection)
		require.Equal(t, 3, in.Selection.Policy.MinRun)
		require.True(t, in.CaptureVariant)
		calls++
		out := videoactivity.CommitClipPlacementOutput{Announce: true, Selection: &videoactivity.PlacementSelectionOutput{}}
		switch calls {
		case 1:
			out.WinnerAssetID = items[0].assetID
			out.Selection.State.Assets = []videoactivity.RestoredEventAsset{project(items[0], 2)}
		case 2:
			require.Equal(t, []uuid.UUID{items[0].assetID}, in.LoserAssetIDs)
			out.WinnerAssetID = items[1].assetID
			out.Selection.State.Assets = []videoactivity.RestoredEventAsset{project(items[1], 7)}
			out.Selection.State.ExactAliases = []videoactivity.RestoredExactAlias{{MD5: items[0].md5, AssetID: items[1].assetID}}
		case 3, 4, 5:
			out.WinnerAssetID = items[2].assetID
			aVotes, bVotes := 7, 12
			switch calls {
			case 3:
				require.Equal(t, []uuid.UUID{items[1].assetID}, in.LoserAssetIDs)
				out.Selection.Restored = []uuid.UUID{items[0].assetID}
			case 4:
				require.False(t, in.NewWinner)
				require.Equal(t, items[0].assetID, in.WinnerAssetID)
				out.WinnerAssetID, aVotes = items[0].assetID, 8
			default:
				require.False(t, in.NewWinner)
				require.Equal(t, items[2].assetID, in.WinnerAssetID, "bridge alias still has one destination")
				aVotes, bVotes = 9, 13
			}
			// Deliberately reverse public set order: it must not reorder the tournament.
			out.Selection.State.Assets = []videoactivity.RestoredEventAsset{project(items[0], aVotes), project(items[2], bVotes)}
			out.Selection.State.ExactAliases = []videoactivity.RestoredExactAlias{{MD5: items[1].md5, AssetID: items[2].assetID}}
		default:
			t.Fatalf("unexpected placement %d", calls)
		}
		return out, nil
	})
	env.OnActivity("PublishEventUpdate", mock.Anything, mock.Anything).Return(func(context.Context, livefeedactivity.EventUpdateInput) error {
		published++
		if calls >= 3 {
			require.Len(t, p.assets, 2)
			require.Equal(t, items[2].assetID, p.assets[0].assetID)
			require.Equal(t, items[0].assetID, p.exactRoots[items[0].md5])
			if calls == 5 {
				require.Equal(t, 13, p.assets[0].popularity)
				require.Equal(t, 9, p.assets[1].popularity)
			}
		}
		return nil
	})
	env.ExecuteWorkflow(func(ctx sdkworkflow.Context) error {
		p = newPipeline(ctx, EventWorkflowInput{EventID: eventID, FixtureID: 1}, pipelineConfig{minRun: 3, atomicPlacement: true,
			variantEvidence: true, canonicalExactAliases: true, preserveIncumbentsOnLoss: true, reversibleSelection: true, eventUpdateContract: true}, sdkworkflow.GetLogger(ctx))
		own := func(c *clip) {
			for n := 0; n < c.popularity; n++ {
				url := c.tweetURL
				if n > 0 {
					url = fmt.Sprintf("%s-%d", c.tweetURL, n)
					c.exactFollowers = append(c.exactFollowers, url)
				}
				p.candidates[url] = candidateOwnership{state: ddiscovery.CandidateInFlight, evidence: discoverycontract.CandidateEvidence{
					EventID: eventID, FixtureID: 1, SearchAttempt: 1, Query: "query", TweetURL: url}}
			}
		}
		for _, item := range items {
			own(&item)
			p.dedupAndCommit(item, visionactivity.ValidateClipOutput{})
			if p.terminalErr != nil {
				return p.terminalErr
			}
		}
		repeat := clip{md5: items[0].md5, tweetURL: "recurrence", popularity: 1, stagingKey: "staging/recurrence"}
		own(&repeat)
		index, isAsset, found := p.matchMD5(repeat)
		require.True(t, found)
		require.True(t, isAsset)
		p.collapseExact(repeat, index, isAsset)
		require.Equal(t, 8, p.assets[1].popularity, "returned durable count must not be incremented a second time")
		bridge := clip{md5: items[1].md5, tweetURL: "bridge-recurrence", popularity: 1, stagingKey: "staging/bridge-recurrence"}
		own(&bridge)
		index, isAsset, found = p.matchMD5(bridge)
		require.True(t, found)
		require.True(t, isAsset)
		p.collapseExact(bridge, index, isAsset)
		for _, candidate := range p.candidates {
			require.Equal(t, ddiscovery.CandidateTerminal, candidate.state)
		}
		return p.terminalErr
	})
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 5, calls)
	require.Equal(t, 5, published)
}

// TestSelectionReplacementRejectsBadAliases preserves old memory on an invalid committed response.
func TestSelectionReplacementRejectsBadAliases(t *testing.T) {
	p := &pipeline{assets: []clip{{assetID: uuid.New(), md5: "old"}}}
	before := p.assets[0]
	err := p.replaceSelection(&videoactivity.PlacementSelectionOutput{State: videoactivity.LoadEventAssetsOutput{
		ExactAliases: []videoactivity.RestoredExactAlias{{MD5: "alias", AssetID: uuid.New()}}}}, uuid.Nil)
	require.Error(t, err)
	require.Equal(t, before, p.assets[0])
}
