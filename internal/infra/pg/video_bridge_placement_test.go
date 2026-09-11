// Losing-bridge placement preserves independent public roots and durable credit.
package pg_test

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// TestPlacementRepo_LosingBridgeKeepsIndependentCredit exercises the exact
// FF-092 placement shape in real Postgres. Workflow tests own hash selection;
// this boundary proves retry, share retention, alias restoration and recurrence.
func TestPlacementRepo_LosingBridgeKeepsIndependentCredit(t *testing.T) {
	pool, placements, assets, shares, fixtureID, eventID := setupPlacementRepo(t)
	a := insertPlacementAsset(t, assets, eventID, fixtureID, "bridge-A", 1)
	b := insertPlacementAsset(t, assets, eventID, fixtureID, "bridge-B", 1)
	aShare := insertPlacementShare(t, shares, a, 1)
	bShare := insertPlacementShare(t, shares, b, 2)
	c := newAsset(eventID, fixtureID, "bridge-C", []uint64{1, 2, 3}, 2_000_000)
	c.S3Key = "9200/bridge-C.mp4"
	c.SupersededBySet(a.ID)
	candidate := func(suffix string) video.PlacementCandidate {
		return video.PlacementCandidate{Evidence: placementEvidence(eventID, fixtureID, suffix), Outcome: discoverycontract.OutcomeDuplicate}
	}
	when := time.Date(2026, 9, 11, 19, 23, 16, 0, time.UTC)
	// B already has enough independent source evidence to remain visible when
	// A reaches the FF-078 threshold. Its earlier credit must never move to A.
	_, err := placements.CommitClipPlacement(t.Context(), video.ClipPlacement{
		EventID: eventID, FixtureID: fixtureID, WinnerAssetID: b.ID, ObservedAssetID: b.ID,
		Verified: true, Candidates: []video.PlacementCandidate{candidate("5101"), candidate("5102")}, CommittedAt: when,
	})
	require.NoError(t, err)
	beforeB, err := assets.Get(t.Context(), b.ID)
	require.NoError(t, err)
	input := video.ClipPlacement{
		EventID: eventID, FixtureID: fixtureID, WinnerAssetID: a.ID, ObservedAssetID: c.ID,
		Variant: c, Verified: true, Candidates: []video.PlacementCandidate{candidate("5201"), candidate("5202")},
		CommittedAt: when,
		// No other keeper loses when the incoming C loses. In particular, no B.
		LoserAssetIDs: nil,
	}
	first, err := placements.CommitClipPlacement(t.Context(), input)
	require.NoError(t, err)
	retry, err := placements.CommitClipPlacement(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, aShare.ID, first.ShareID)
	require.Equal(t, first.WinnerAssetID, retry.WinnerAssetID)
	require.Equal(t, first.ShareID, retry.ShareID)
	afterB, err := assets.Get(t.Context(), b.ID)
	require.NoError(t, err)
	require.Equal(t, beforeB, afterB, "losing C must not mutate B at all")
	afterA, err := assets.Get(t.Context(), a.ID)
	require.NoError(t, err)
	require.Equal(t, 3, afterA.Popularity, "C and its exact follower each count once")
	afterC, err := assets.Get(t.Context(), c.ID)
	require.NoError(t, err)
	require.Equal(t, &a.ID, afterC.SupersededBy)

	// The actual recovery projection must retain A and B as roots, with only
	// C's exact bytes aliased to A. This feeds a subsequent fresh execution.
	activities := videoactivity.PersistActivities{Assets: assets, Shares: shares}
	recovered, err := activities.LoadEventAssets(t.Context(), videoactivity.LoadEventAssetsInput{EventID: eventID})
	require.NoError(t, err)
	require.Len(t, recovered.Assets, 2)
	roots := make(map[uuid.UUID]bool)
	for _, asset := range recovered.Assets {
		roots[asset.AssetID] = true
	}
	require.True(t, roots[a.ID])
	require.True(t, roots[b.ID])
	aliases := make(map[string]uuid.UUID)
	for _, alias := range recovered.ExactAliases {
		aliases[alias.MD5] = alias.AssetID
	}
	require.Equal(t, a.ID, aliases[hex.EncodeToString(c.MD5)])
	require.Equal(t, b.ID, aliases[hex.EncodeToString(b.MD5)])

	recurrence := video.ClipPlacement{
		EventID: eventID, FixtureID: fixtureID, ObservedAssetID: b.ID, WinnerAssetID: aliases[hex.EncodeToString(b.MD5)],
		Verified: true, Candidates: []video.PlacementCandidate{candidate("5301")}, CommittedAt: when.Add(time.Minute),
	}
	for range 2 {
		_, err := placements.CommitClipPlacement(t.Context(), recurrence)
		require.NoError(t, err)
	}
	afterB, err = assets.Get(t.Context(), b.ID)
	require.NoError(t, err)
	require.Nil(t, afterB.SupersededBy)
	require.Equal(t, 4, afterB.Popularity)
	afterA, err = assets.Get(t.Context(), a.ID)
	require.NoError(t, err)
	require.Equal(t, 3, afterA.Popularity)
	live, err := shares.ListLiveForEvent(t.Context(), eventID)
	require.NoError(t, err)
	require.Len(t, live, 2)
	require.Equal(t, bShare.ID, live[0].ShareID, "B's independent vote must affect its own rank")
	require.Equal(t, aShare.ID, live[1].ShareID)
	var sharesCount, correctCredits int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_shares WHERE event_id = $1`, eventID).Scan(&sharesCount))
	require.Equal(t, 2, sharesCount, "C must not gain a public share")
	require.NoError(t, pool.QueryRow(t.Context(), `
		SELECT count(*) FROM event_search_candidates WHERE event_id = $1
		  AND ((observed_asset_id = $2 AND credited_asset_id = $2)
		    OR (observed_asset_id = $3 AND credited_asset_id = $4))
	`, eventID, b.ID, c.ID, a.ID).Scan(&correctCredits))
	require.Equal(t, 5, correctCredits, "three B observations stay with B; two C observations credit A")
}
