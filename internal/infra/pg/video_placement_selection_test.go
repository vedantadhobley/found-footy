// Combined placement regressions exercise real SQL restoration, retries, revocation and legacy gaps.
package pg_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// prepareSelectionRequest models the activity's snapshot and successful object checks.
func prepareSelectionRequest(t *testing.T, repo *pg.PlacementRepo, in *video.ClipPlacement) {
	t.Helper()
	snapshot, err := repo.LoadSelection(t.Context(), in.EventID)
	require.NoError(t, err)
	hash, err := snapshot.Fingerprint()
	require.NoError(t, err)
	request := &video.SelectionRequest{ID: uuid.New(), EventID: in.EventID, FixtureID: in.FixtureID, SnapshotHash: hash,
		Policy: video.SelectionPolicy{MaxHamming: 0, MinRun: 3}}
	for _, node := range snapshot.Nodes {
		request.PreparedAssetIDs = append(request.PreparedAssetIDs, node.Asset.ID)
	}
	if in.Winner != nil {
		request.PreparedAssetIDs = append(request.PreparedAssetIDs, in.Winner.ID)
	}
	in.Selection = request
}

// replacementSelection adds D after the historical A -> C -> B seed.
func replacementSelection(t *testing.T, repo *pg.PlacementRepo, old *video.Asset) video.ClipPlacement {
	t.Helper()
	d := newAsset(old.EventID, old.FixtureID, "selection-D", old.FrameHashes, 2000000)
	proof := assetValidation(d)
	in := video.ClipPlacement{EventID: d.EventID, FixtureID: d.FixtureID, Winner: d, ObservedAssetID: d.ID,
		Verified: true, ExtractedMinute: proof.Evaluation.MatchedMinute, Validation: proof, LoserAssetIDs: []uuid.UUID{old.ID},
		Candidates: []video.PlacementCandidate{{Evidence: placementEvidence(d.EventID, d.FixtureID, "selection-D"), Outcome: discoverycontract.OutcomePromoted}}}
	prepareSelectionRequest(t, repo, &in)
	return in
}

// TestPlacementSelectionAtomicRestoreAndOldRetry replaces cached counts from direct evidence before commit.
func TestPlacementSelectionAtomicRestoreAndOldRetry(t *testing.T) {
	_, repo, assets, shares, _, nodes, oldShare := seedSelection(t, false, 2)
	in := replacementSelection(t, repo, nodes[2])
	out, err := repo.CommitClipPlacement(t.Context(), in)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{nodes[0].ID}, out.Selection.Plan.Restored)
	require.Equal(t, oldShare, out.Selection.ShareIDs[nodes[0].ID])
	live, err := shares.ListLiveForEvent(t.Context(), in.EventID)
	require.NoError(t, err)
	require.Len(t, live, 2)
	require.Equal(t, 13, live[0].Popularity)
	require.Equal(t, 7, live[1].Popularity)

	recurrence := video.ClipPlacement{EventID: in.EventID, FixtureID: in.FixtureID, ObservedAssetID: nodes[0].ID, WinnerAssetID: nodes[0].ID,
		Candidates: []video.PlacementCandidate{{Evidence: placementEvidence(in.EventID, in.FixtureID, "restored-A-again"), Outcome: discoverycontract.OutcomeDuplicate}}}
	prepareSelectionRequest(t, repo, &recurrence)
	_, err = repo.CommitClipPlacement(t.Context(), recurrence)
	require.NoError(t, err)
	// Move D again before retrying its old incoming placement. Its old winner
	// is no longer live, and A's support must remain eight (three exact + five bridge).
	next := replacementSelection(t, repo, in.Winner)
	next.Winner.ID = uuid.New()
	next.Winner.MD5[0]++
	next.ObservedAssetID = next.Winner.ID
	next.Validation = assetValidation(next.Winner)
	next.Candidates[0].Evidence = placementEvidence(in.EventID, in.FixtureID, "selection-E")
	prepareSelectionRequest(t, repo, &next)
	_, err = repo.CommitClipPlacement(t.Context(), next)
	require.NoError(t, err)
	retry, err := repo.CommitClipPlacement(t.Context(), in)
	require.NoError(t, err)
	require.True(t, retry.Selection.Replayed)
	a, err := assets.Get(t.Context(), nodes[0].ID)
	require.NoError(t, err)
	require.Equal(t, 8, a.Popularity)
	d, err := assets.Get(t.Context(), in.Winner.ID)
	require.NoError(t, err)
	require.Equal(t, next.Winner.ID, *d.SupersededBy)
	in.Selection.Policy.MaxHamming++
	_, err = repo.CommitClipPlacement(t.Context(), in)
	require.ErrorContains(t, err, "different content")
}

// TestPlacementDirectSupportBridgeRecurrence updates both keepers from one source without duplicate retry votes.
func TestPlacementDirectSupportBridgeRecurrence(t *testing.T) {
	pool, repo, assets, shares, request, nodes, _ := seedSelection(t, false, 2)
	_, err := repo.CommitSelection(t.Context(), request)
	require.NoError(t, err)
	in := video.ClipPlacement{EventID: request.EventID, FixtureID: request.FixtureID,
		ObservedAssetID: nodes[1].ID, WinnerAssetID: nodes[2].ID,
		Candidates: []video.PlacementCandidate{{Evidence: placementEvidence(request.EventID, request.FixtureID, "bridge-again"), Outcome: discoverycontract.OutcomeDuplicate}}}
	prepareSelectionRequest(t, repo, &in)
	for attempt := range 2 {
		out, err := repo.CommitClipPlacement(t.Context(), in)
		require.NoError(t, err)
		require.Equal(t, attempt > 0, out.Selection.Replayed)
		require.Empty(t, out.Selection.Skipped)
		for index, want := range map[int]int{0: 8, 2: 13} {
			a, err := assets.Get(t.Context(), nodes[index].ID)
			require.NoError(t, err)
			require.Equal(t, want, a.Popularity)
		}
		live, err := shares.ListLiveForEvent(t.Context(), in.EventID)
		require.NoError(t, err)
		require.Len(t, live, 2)
		require.Equal(t, 13, live[0].Popularity)
		require.Equal(t, 8, live[1].Popularity)
	}
	var sources int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM event_search_candidates WHERE event_id=$1`, in.EventID).Scan(&sources))
	require.Equal(t, 15, sources, "21 support memberships still represent only 15 accepted tweets")
}

// TestPlacementDirectSupportDoesNotMergeScores counts union evidence when a bridge replaces both roots.
func TestPlacementDirectSupportDoesNotMergeScores(t *testing.T) {
	_, repo, assets, shares, request, nodes, _ := seedSelection(t, false, 2)
	_, err := repo.CommitSelection(t.Context(), request)
	require.NoError(t, err)
	d := newAsset(request.EventID, request.FixtureID, "stronger-bridge", nodes[1].FrameHashes, 3000000)
	proof := assetValidation(d)
	in := video.ClipPlacement{EventID: request.EventID, FixtureID: request.FixtureID,
		Winner: d, ObservedAssetID: d.ID, Validation: proof, Verified: true, ExtractedMinute: proof.Evaluation.MatchedMinute,
		LoserAssetIDs: []uuid.UUID{nodes[0].ID, nodes[2].ID},
		Candidates:    []video.PlacementCandidate{{Evidence: placementEvidence(request.EventID, request.FixtureID, "stronger-bridge"), Outcome: discoverycontract.OutcomePromoted}}}
	prepareSelectionRequest(t, repo, &in)
	for range 2 {
		out, err := repo.CommitClipPlacement(t.Context(), in)
		require.NoError(t, err)
		require.Empty(t, out.Selection.Plan.Restored)
		a, err := assets.Get(t.Context(), d.ID)
		require.NoError(t, err)
		require.Equal(t, 15, a.Popularity, "recompute 2+5+7+1, never merge overlapping 7+12+1")
		live, err := shares.ListLiveForEvent(t.Context(), in.EventID)
		require.NoError(t, err)
		require.Len(t, live, 1)
		require.Equal(t, 15, live[0].Popularity)
	}
}

// TestPlacementSelectionGuardsAndRollback covers post-placement failures, not just a standalone repair.
func TestPlacementSelectionGuardsAndRollback(t *testing.T) {
	for _, mode := range []string{"receipt failure", "stale", "missing winner bytes", "missing new variant bytes", "missing validation", "revoked", "incomplete credits", "missing hidden bytes"} {
		t.Run(mode, func(t *testing.T) {
			pool, repo, assets, shares, _, nodes, _ := seedSelection(t, false, 2)
			in := replacementSelection(t, repo, nodes[2])
			switch mode {
			case "receipt failure":
				_, err := pool.Exec(t.Context(), `CREATE FUNCTION fail_combined_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'receipt failure'; END $$;
				CREATE TRIGGER fail_receipt BEFORE INSERT ON video_selection_commits FOR EACH ROW EXECUTE FUNCTION fail_combined_receipt()`)
				require.NoError(t, err)
			case "stale":
				_, err := pool.Exec(t.Context(), `UPDATE video_assets SET popularity=popularity+1 WHERE id=$1`, nodes[2].ID)
				require.NoError(t, err)
			case "missing winner bytes":
				in.Selection.PreparedAssetIDs = in.Selection.PreparedAssetIDs[:3]
			case "missing new variant bytes":
				in.Variant, in.Winner = in.Winner, nil
				in.WinnerAssetID = nodes[2].ID
				in.Variant.SupersededBySet(nodes[2].ID)
				in.LoserAssetIDs = nil
				in.Candidates[0].Outcome = discoverycontract.OutcomeDuplicate
				in.Selection.PreparedAssetIDs = in.Selection.PreparedAssetIDs[:3]
			case "missing validation":
				in.Validation = nil
			case "revoked":
				require.NoError(t, shares.RemoveByEvent(t.Context(), in.EventID, video.RemovalAssetGone))
				prepareSelectionRequest(t, repo, &in)
			case "incomplete credits":
				_, err := pool.Exec(t.Context(), `UPDATE event_search_candidates SET observed_asset_id=NULL WHERE observed_asset_id=$1`, nodes[0].ID)
				require.NoError(t, err)
				prepareSelectionRequest(t, repo, &in)
			case "missing hidden bytes":
				var ready []uuid.UUID
				for _, id := range in.Selection.PreparedAssetIDs {
					if id != nodes[0].ID {
						ready = append(ready, id)
					}
				}
				in.Selection.PreparedAssetIDs = ready
			}
			before, err := repo.LoadSelection(t.Context(), in.EventID)
			require.NoError(t, err)
			beforeHash, err := before.Fingerprint()
			require.NoError(t, err)
			out, err := repo.CommitClipPlacement(t.Context(), in)
			if mode == "incomplete credits" || mode == "missing hidden bytes" {
				require.NoError(t, err)
				require.Empty(t, out.Selection.Plan.Restored)
				if mode == "incomplete credits" {
					require.Equal(t, video.SelectionIncompleteCredits, out.Selection.Skipped)
				}
				d, err := assets.Get(t.Context(), in.Winner.ID)
				require.NoError(t, err)
				want := 13
				if mode == "incomplete credits" {
					want = 15 // Explicit legacy fallback, not a fabricated direct score.
				}
				require.Equal(t, want, d.Popularity)
				return
			}
			require.Error(t, err)
			if mode == "stale" {
				require.ErrorIs(t, err, video.ErrSelectionStale)
			}
			after, err := repo.LoadSelection(t.Context(), in.EventID)
			require.NoError(t, err)
			afterHash, err := after.Fingerprint()
			require.NoError(t, err)
			require.Equal(t, beforeHash, afterHash, "incoming asset, proof, source and repair must roll back together")
		})
	}
}

// TestPlacementSelectionConcurrentRevocation permits either lock order, never an escaped new share.
func TestPlacementSelectionConcurrentRevocation(t *testing.T) {
	_, repo, _, shares, _, nodes, _ := seedSelection(t, true, 2)
	in := replacementSelection(t, repo, nodes[2])
	finished := make(chan error, 2)
	go func() { _, err := repo.CommitClipPlacement(t.Context(), in); finished <- err }()
	go func() { finished <- shares.RemoveByEvent(t.Context(), in.EventID, video.RemovalAssetGone) }()
	for range 2 {
		err := <-finished
		require.True(t, err == nil || errors.Is(err, video.ErrSelectionStale) || errors.Is(err, video.ErrSelectionMedia), "%v", err)
	}
	all, err := shares.GetByEvent(t.Context(), in.EventID)
	require.NoError(t, err)
	for _, share := range all {
		require.Equal(t, video.ShareStateRemoved, share.State)
	}
}
