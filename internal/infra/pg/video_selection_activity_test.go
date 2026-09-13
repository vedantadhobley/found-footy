// Activity-to-PostgreSQL tests retain real transaction semantics with deterministic object-store failures.
package pg_test

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// selectionObjects makes absence different from HEAD failure and can lose a delete acknowledgement.
type selectionObjects struct {
	mu         sync.Mutex
	keys       map[string]bool
	copyCount  int
	failDelete bool
	failHead   bool
}

// Head is called concurrently by the bounded preparation group.
func (s *selectionObjects) Head(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failHead {
		return false, errors.New("HEAD unavailable")
	}
	return s.keys[key], nil
}

// Copy requires a still-present source, making retry-after-delete bugs observable.
func (s *selectionObjects) Copy(_ context.Context, src, dst string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.keys[src] {
		return errors.New("staging absent")
	}
	s.keys[dst] = true
	s.copyCount++
	return nil
}

// Delete can finish its mutation and still fail the activity's acknowledgement.
func (s *selectionObjects) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, key)
	if s.failDelete {
		s.failDelete = false
		return errors.New("delete acknowledgement lost")
	}
	return nil
}

// TestSelectionActivityRetryReturnsCurrentState includes a never-public restoration and later exact credit.
func TestSelectionActivityRetryReturnsCurrentState(t *testing.T) {
	pool, repo, assets, shares, request, nodes, _ := seedSelection(t, true, 2)
	objects := &selectionObjects{keys: map[string]bool{"staging/incoming": true, nodes[0].S3Key: true}, failDelete: true}
	a := &videoactivity.PersistActivities{Placements: repo, Assets: assets, Shares: shares, S3: objects, Bucket: "found-footy", AssetsPrefix: "assets"}
	md5 := strings.Repeat("df", 16)
	asset := newAsset(request.EventID, request.FixtureID, "activity-D", nodes[2].FrameHashes, 2000000)
	asset.MD5, _ = hex.DecodeString(md5)
	asset.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(request.EventID.String()+":"+md5))
	proof := assetValidation(asset)
	in := videoactivity.CommitClipPlacementInput{EventID: request.EventID, FixtureID: request.FixtureID, CaptureVariant: true, NewWinner: true,
		StagingKey: "staging/incoming", MD5: md5, HashVersion: asset.FrameHashVersion, FrameHashes: asset.FrameHashes,
		Width: asset.Width, Height: asset.Height, DurationMS: asset.DurationMS, FileSizeBytes: asset.FileSizeBytes,
		Verified: true, ExtractedMinute: proof.Evaluation.MatchedMinute, Validation: proof, LoserAssetIDs: []uuid.UUID{nodes[2].ID},
		Candidates: []videoactivity.PlacementCandidateInput{{Evidence: placementEvidence(request.EventID, request.FixtureID, "activity-D"), Outcome: discoverycontract.OutcomePromoted}},
		Selection:  &videoactivity.PlacementSelectionInput{ID: uuid.New(), Policy: request.Policy}}
	_, err := a.CommitClipPlacement(t.Context(), in)
	require.ErrorContains(t, err, "acknowledgement")
	live, err := shares.ListLiveForEvent(t.Context(), request.EventID)
	require.NoError(t, err)
	require.Len(t, live, 2, "whole selection committed before cleanup failed")

	recurrence := video.ClipPlacement{EventID: request.EventID, FixtureID: request.FixtureID, ObservedAssetID: nodes[0].ID, WinnerAssetID: nodes[0].ID,
		Candidates: []video.PlacementCandidate{{Evidence: placementEvidence(request.EventID, request.FixtureID, "activity-A-again"), Outcome: discoverycontract.OutcomeDuplicate}}}
	prepareSelectionRequest(t, repo, &recurrence)
	_, err = repo.CommitClipPlacement(t.Context(), recurrence)
	require.NoError(t, err)
	out, err := a.CommitClipPlacement(t.Context(), in)
	require.NoError(t, err)
	require.True(t, out.Announce)
	require.Equal(t, 1, objects.copyCount)
	current := map[uuid.UUID]int{}
	for _, item := range out.Selection.State.Assets {
		current[item.AssetID] = item.Popularity
	}
	require.Equal(t, map[uuid.UUID]int{nodes[0].ID: 8, asset.ID: 13}, current)
	var records int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_selection_commits WHERE event_id=$1`, request.EventID).Scan(&records))
	require.Equal(t, 2, records)
	recovered, err := a.LoadEventAssets(t.Context(), videoactivity.LoadEventAssetsInput{EventID: request.EventID, ConsistentSelection: true})
	require.NoError(t, err)
	require.Equal(t, out.Selection.State, recovered)

	// Retrying a failed HEAD must not count missing media as an eligible asset.
	objects.failHead = true
	_, err = a.CommitClipPlacement(t.Context(), in)
	require.ErrorContains(t, err, "HEAD unavailable")
	objects.failHead = false
	_, err = a.CommitClipPlacement(t.Context(), in)
	require.NoError(t, err)
	require.Equal(t, 1, objects.copyCount)

	// A removed-event retry owes cleanup, not a copy from already absent staging.
	_, err = pool.Exec(t.Context(), `UPDATE events SET removed=true,removed_reason='var',removed_at=NOW() WHERE id=$1`, request.EventID)
	require.NoError(t, err)
	removed := in
	removed.MD5 = strings.Repeat("ef", 16)
	removed.StagingKey = "staging/already-gone"
	removed.Selection = &videoactivity.PlacementSelectionInput{ID: uuid.New(), Policy: request.Policy}
	removed.Candidates = []videoactivity.PlacementCandidateInput{{Evidence: placementEvidence(request.EventID, request.FixtureID, "removed-incoming"), Outcome: discoverycontract.OutcomePromoted}}
	removedAsset := *asset
	removedAsset.MD5, _ = hex.DecodeString(removed.MD5)
	removed.Validation = assetValidation(&removedAsset)
	objects.failDelete = true
	_, err = a.CommitClipPlacement(t.Context(), removed)
	require.ErrorContains(t, err, "acknowledgement")
	removedOut, err := a.CommitClipPlacement(t.Context(), removed)
	require.NoError(t, err)
	require.True(t, removedOut.EventRemoved)
	require.False(t, removedOut.Announce)
	require.Equal(t, 1, objects.copyCount)
}
