// Bridge regressions pin direct placement, alias recovery, and old-history commands.
package workflow_test

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

const ff092BridgeChangeIDForTest = "ff-092-preserve-incumbents-on-loss"

// mastantuonoBridgeAssets decodes the exact incident hashes without converting
// uint64 values through floating point. Tests supply their own live identities.
func mastantuonoBridgeAssets(t *testing.T, eventID uuid.UUID) map[string]videoactivity.RestoredEventAsset {
	t.Helper()
	f, err := os.Open("testdata/mastantuono-bridge.csv")
	require.NoError(t, err)
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 4)
	cols := make(map[string]int)
	for i, name := range rows[0] {
		cols[name] = i
	}
	labels := map[string]string{
		"731621c93d168bdfac320d60a77482c6": "A",
		"6e1003b5a45f3c44f012f29b2eecd213": "B",
		"bf1f217e900aba00b75d0575b2235ad4": "C",
	}
	out := make(map[string]videoactivity.RestoredEventAsset)
	for _, row := range rows[1:] {
		number := func(key string) int {
			n, err := strconv.Atoi(row[cols[key]])
			require.NoError(t, err)
			return n
		}
		md5 := row[cols["md5_hex"]]
		label, ok := labels[md5]
		require.True(t, ok)
		raw, err := hex.DecodeString(row[cols["frame_hashes_hex"]])
		require.NoError(t, err)
		require.Zero(t, len(raw)%8)
		hashes := make([]uint64, len(raw)/8)
		for i := range hashes {
			hashes[i] = binary.BigEndian.Uint64(raw[i*8:])
		}
		bitrate := number("bitrate")
		out[label] = videoactivity.RestoredEventAsset{
			AssetID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(eventID.String()+":"+md5)),
			MD5:     md5, FrameHashes: hashes, HashVersion: dvideo.FrameHashVersion(row[cols["hash_version"]]),
			Width: number("width"), Height: number("height"), DurationMS: number("duration_ms"),
			Bitrate: &bitrate, FileSizeBytes: int64(number("file_size_bytes")), Popularity: 1, Verified: true,
		}
	}
	return out
}

// TestMastantuonoBridgeTriangle prevents a synthetic approximation from hiding
// the two-route topology that caused the production removal.
func TestMastantuonoBridgeTriangle(t *testing.T) {
	assets := mastantuonoBridgeAssets(t, uuid.New())
	for _, pair := range []struct {
		left, right        string
		primary, sustained bool
	}{{"A", "B", false, false}, {"C", "A", false, true}, {"C", "B", true, true}} {
		for _, reverse := range []bool{false, true} {
			a, b := assets[pair.left], assets[pair.right]
			if reverse {
				a, b = b, a
			}
			require.Equal(t, pair.primary, dvideo.Match(a.FrameHashes, b.FrameHashes, 12, 30, 3), pair)
			require.Equal(t, pair.sustained, dvideo.Match(a.FrameHashes, b.FrameHashes, 16, 50, 5), pair)
		}
	}
}

// TestEventWorkflow_LosingBridge preserves independent keepers and subsequent
// exact-byte credit. DefaultVersion deliberately reproduces the old commands.
// C-first orders still exercise the unchanged winning-candidate policy; this
// bounded fix does not claim arrival-independent content coverage.
func TestEventWorkflow_LosingBridge(t *testing.T) {
	for _, tc := range []struct {
		name, restored string
		arrivals       []string
		legacy         bool
		retry          bool
		winningBridge  bool
		compatibility  bool
		wantAssets     int
	}{
		{name: "ABC_then_exact_B_C", arrivals: []string{"A", "B", "CC", "B", "C"}, retry: true, wantAssets: 2},
		{name: "BAC", arrivals: []string{"B", "A", "C"}, wantAssets: 2},
		{name: "ACB", arrivals: []string{"A", "C", "B"}, wantAssets: 2},
		{name: "BCA_winning_path_unchanged", arrivals: []string{"B", "C", "A"}, wantAssets: 1},
		{name: "CAB_winning_path_unchanged", arrivals: []string{"C", "A", "B"}, wantAssets: 2},
		{name: "CBA_winning_path_unchanged", arrivals: []string{"C", "B", "A"}, wantAssets: 1},
		{name: "recovered_AB", restored: "AB", arrivals: []string{"CC", "B", "C"}, retry: true, wantAssets: 2},
		{name: "recovered_BA", restored: "BA", arrivals: []string{"C", "B"}, wantAssets: 2},
		{name: "old_history", restored: "AB", arrivals: []string{"C", "B"}, legacy: true, wantAssets: 1},
		{name: "winning_bridge", restored: "AB", arrivals: []string{"C"}, winningBridge: true, wantAssets: 1},
		{name: "compatibility_fixed", restored: "AB", arrivals: []string{"C", "B"}, compatibility: true, wantAssets: 2},
		{name: "compatibility_old_history", restored: "AB", arrivals: []string{"C", "B"}, compatibility: true, legacy: true, wantAssets: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := stdDiscoveryInput()
			assets := mastantuonoBridgeAssets(t, in.EventID)
			if tc.winningBridge {
				c := assets["C"]
				c.DurationMS = 30_000
				assets["C"] = c
			}
			var restored videoactivity.LoadEventAssetsOutput
			for _, label := range tc.restored {
				restored.Assets = append(restored.Assets, assets[string(label)])
			}
			var suite testsuite.WorkflowTestSuite
			env := baseEventEnvWithOptions(&suite, discoveryactivity.LoadEventRecoveryStateOutput{}, restored,
				true, true, !tc.compatibility, true, true, true, discoveryactivity.GetDiscoveryConfigOutput{
					MaxAttempts: len(tc.arrivals), AttemptSpacing: time.Minute, MaxAgeMinutes: 3, QueryTimeout: time.Minute,
					MaxHamming: 12, MinRunFrames: 30, MaxGapFrames: 3,
					LongMaxHamming: 16, LongMinRunFrames: 50, LongMaxGapFrames: 5,
				})
			if tc.legacy {
				env.OnGetVersion(ff092BridgeChangeIDForTest, sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).
					Return(sdkworkflow.DefaultVersion).Once()
			}
			env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
				Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)
			env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
			labelsByURL := make(map[string]string)
			for i, batch := range tc.arrivals {
				var refs []twitter.VideoRef
				for j, label := range batch {
					a := assets[string(label)]
					url := fmt.Sprintf("https://x.com/example/status/%d%d", i+1, j+1)
					labelsByURL[url] = string(label)
					key := fmt.Sprintf("staging/%d-%d.mp4", i, j)
					refs = append(refs, twitter.VideoRef{TweetURL: url, VideoPageURL: url, DurationSeconds: 12})
					env.OnActivity("DownloadAndStage", mock.Anything, downloadTweetIs(url)).Return(videoactivity.DownloadAndStageOutput{
						Outcome: videoactivity.OutcomePassed, MD5: a.MD5, StagingKey: key,
						Width: a.Width, Height: a.Height, DurationMS: a.DurationMS, SizeBytes: a.FileSizeBytes, Bitrate: *a.Bitrate,
					}, nil).Once()
					env.OnActivity("HashVideo", mock.Anything, hashStagingIs(key)).Return(videoactivity.HashVideoOutput{
						FrameHashes: a.FrameHashes, HashVersion: a.HashVersion,
					}, nil).After(time.Second).Maybe()
					env.OnActivity("ValidateClip", mock.Anything, stagingIs(key)).Return(visionactivity.ValidateClipOutput{
						Outcome: "verified", MatchedMinute: pInt(29),
					}, nil).After(2 * time.Second).Maybe()
				}
				env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
					Return(discoveryactivity.SearchTweetsOutput{Videos: refs, Count: len(refs), StopReason: "age"}, nil).Once()
			}
			var commits []videoactivity.CommitClipPlacementInput
			var bumps []videoactivity.BumpAssetPopularityInput
			var supersessions []videoactivity.SupersedeAssetsInput
			if tc.compatibility {
				env.OnActivity("UpsertCandidateOutcome", mock.Anything, mock.Anything).Return(nil)
				env.OnActivity("BumpAssetPopularity", mock.Anything, mock.Anything).
					Return(func(_ context.Context, in videoactivity.BumpAssetPopularityInput) error {
						bumps = append(bumps, in)
						return nil
					})
				env.OnActivity("SupersedeAssets", mock.Anything, mock.Anything).
					Return(func(_ context.Context, in videoactivity.SupersedeAssetsInput) error {
						supersessions = append(supersessions, in)
						return nil
					}).Maybe()
			}
			var failed *videoactivity.CommitClipPlacementInput
			env.OnActivity("CommitClipPlacement", mock.Anything, mock.Anything).
				Return(func(_ context.Context, placement videoactivity.CommitClipPlacementInput) (videoactivity.CommitClipPlacementOutput, error) {
					if tc.retry && placement.MD5 == assets["C"].MD5 && failed == nil {
						failed = &placement
						return videoactivity.CommitClipPlacementOutput{}, errors.New("uncertain activity completion")
					}
					commits = append(commits, placement)
					id := placement.WinnerAssetID
					if placement.NewWinner {
						id = uuid.NewSHA1(uuid.NameSpaceOID, []byte(in.EventID.String()+":"+placement.MD5))
					}
					return videoactivity.CommitClipPlacementOutput{WinnerAssetID: id, WinnerCreated: placement.NewWinner, Announce: true}, nil
				})
			env.ExecuteWorkflow(workflow.EventWorkflow, in)
			requireDone(t, env)
			var out workflow.EventWorkflowOutput
			require.NoError(t, env.GetWorkflowResult(&out))
			require.Equal(t, tc.wantAssets, out.AssetsKept)
			if tc.compatibility {
				require.Empty(t, commits)
				require.Len(t, bumps, 2)
				require.Equal(t, assets["A"].AssetID, bumps[0].AssetID)
				require.Equal(t, 1, bumps[0].Count)
				if tc.legacy {
					require.Len(t, supersessions, 1)
					require.Equal(t, []uuid.UUID{assets["B"].AssetID}, supersessions[0].LoserAssetIDs)
					require.Equal(t, assets["A"].AssetID, bumps[1].AssetID)
				} else {
					require.Empty(t, supersessions)
					require.Equal(t, assets["B"].AssetID, bumps[1].AssetID)
				}
				return
			}
			if tc.retry {
				require.NotNil(t, failed)
			}
			votes := make(map[string]int)
			for _, placement := range commits {
				if !placement.NewWinner && !tc.legacy {
					require.Empty(t, placement.LoserAssetIDs, "a losing candidate cannot retire keepers")
				}
				if failed != nil && placement.MD5 == assets["C"].MD5 {
					require.Equal(t, *failed, placement, "retry must repeat the exact placement")
					failed = nil
				}
				for _, c := range placement.Candidates {
					votes[c.Evidence.TweetURL]++
					if tc.wantAssets == 2 && labelsByURL[c.Evidence.TweetURL] == "B" && !placement.NewWinner {
						require.Equal(t, assets["B"].AssetID, placement.WinnerAssetID, "B recurrence must not credit A")
					}
					if !placement.NewWinner {
						require.Equal(t, discoverycontract.OutcomeDuplicate, c.Outcome)
					}
				}
			}
			require.Nil(t, failed)
			require.Len(t, votes, len(labelsByURL))
			for _, n := range votes {
				require.Equal(t, 1, n, "each source belongs to one completed placement")
			}
			if tc.legacy {
				require.Equal(t, []uuid.UUID{assets["B"].AssetID}, commits[0].LoserAssetIDs)
			}
			if tc.winningBridge {
				require.ElementsMatch(t, []uuid.UUID{assets["A"].AssetID, assets["B"].AssetID}, commits[0].LoserAssetIDs)
			}
			env.AssertNumberOfCalls(t, "PublishEventUpdate", len(commits)+1)
			env.AssertNumberOfCalls(t, "BumpAssetPopularity", 0)
			env.AssertNumberOfCalls(t, "SupersedeAssets", 0)
			hashed := make(map[string]bool)
			for _, label := range tc.restored {
				hashed[string(label)] = true
			}
			wantHashCalls := 0
			for _, batch := range tc.arrivals {
				for _, label := range batch {
					if !hashed[string(label)] {
						wantHashCalls++
						hashed[string(label)] = true
					}
				}
			}
			env.AssertNumberOfCalls(t, "HashVideo", wantHashCalls)
			env.AssertNumberOfCalls(t, "ValidateClip", wantHashCalls)
		})
	}
}
