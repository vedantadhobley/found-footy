// Real PostgreSQL tests pin immutable, exact-asset validation and transactional rollback.
package pg_test

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
	"github.com/vedantadhobley/found-footy/migrations"
)

// assetValidation retains this variant's clock even when another asset wins selection.
func assetValidation(asset *video.Asset) *dvision.Evidence {
	clock := "22:12"
	frames := []dvision.FrameObservation{{Soccer: true, Clock: &clock}, {Soccer: true}, {Soccer: true}}
	expected := dvision.Expected{Elapsed: 23}
	return &dvision.Evidence{ID: uuid.New(), EventID: asset.EventID, FixtureID: asset.FixtureID,
		MD5: hex.EncodeToString(asset.MD5), Version: 1, EvaluatedAt: time.Now().UTC(),
		Evaluator: dvision.EvaluatorVersion, Model: "test-model", PromptSHA256: strings.Repeat("a", 64),
		SchemaSHA256: strings.Repeat("b", 64), Expected: expected, ToleranceMinutes: 1,
		FramePositions: []float64{2, 4, 6}, FrameQuality: 3, Frames: frames,
		Evaluation: dvision.Evaluate(frames, expected, 1)}
}

// TestPlacementValidationLivesOnObservedVariant covers losing/promoted variants,
// exact followers, recurrence, repeated evaluations, and rollback on drift.
func TestPlacementValidationLivesOnObservedVariant(t *testing.T) {
	pool, placements, assets, shares, fixtureID, eventID := setupPlacementRepo(t)
	winner := insertPlacementAsset(t, assets, eventID, fixtureID, "validation-winner", 1)
	winnerShare := insertPlacementShare(t, shares, winner, 1)
	variant := newAsset(eventID, fixtureID, "validation-loser", []uint64{1, 2, 3}, 1_000_000)
	variant.SupersededBySet(winner.ID)
	proof := assetValidation(variant)
	in := video.ClipPlacement{EventID: eventID, FixtureID: fixtureID, WinnerAssetID: winner.ID,
		ObservedAssetID: variant.ID, Variant: variant, Verified: true,
		ExtractedMinute: proof.Evaluation.MatchedMinute, Validation: proof,
		Candidates: []video.PlacementCandidate{
			{Evidence: placementEvidence(eventID, fixtureID, "validation-1"), Outcome: discoverycontract.OutcomeDuplicate},
			{Evidence: placementEvidence(eventID, fixtureID, "validation-2"), Outcome: discoverycontract.OutcomeDuplicate},
		}}
	for range 2 {
		out, err := placements.CommitClipPlacement(t.Context(), in)
		require.NoError(t, err)
		require.Equal(t, winnerShare.ID, out.ShareID)
	}
	var storedAsset uuid.UUID
	var data []byte
	var records, followers int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT asset_id, evidence FROM video_asset_validations WHERE id=$1`, proof.ID).Scan(&storedAsset, &data))
	require.Equal(t, variant.ID, storedAsset, "never attach a losing variant's evidence to its winner")
	var stored dvision.Evidence
	require.NoError(t, json.Unmarshal(data, &stored))
	require.Equal(t, proof.ID, stored.ID)
	require.Equal(t, 22, *stored.Evaluation.MatchedMinute)
	require.Equal(t, proof.Frames, stored.Frames)
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_asset_validations WHERE event_id=$1`, eventID).Scan(&records))
	require.Equal(t, 1, records)
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM event_search_candidates WHERE observed_asset_id=$1`, variant.ID).Scan(&followers))
	require.Equal(t, 2, followers)
	var ownShares int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_shares WHERE asset_id=$1`, variant.ID).Scan(&ownShares))
	require.Zero(t, ownShares, "validation must not mint a hidden share")

	// A subsequent exact recurrence uses the old accepted identity; no new
	// validation is invented and the existing record survives credit movement.
	exact := in
	exact.Variant, exact.Validation = nil, nil
	exact.Candidates = []video.PlacementCandidate{{Evidence: placementEvidence(eventID, fixtureID, "validation-3"), Outcome: discoverycontract.OutcomeDuplicate}}
	_, err := placements.CommitClipPlacement(t.Context(), exact)
	require.NoError(t, err)

	// Changed content under one evaluation ID is never an overwrite, and a
	// failed evidence insert cannot leave a credited candidate behind.
	drift := *proof
	drift.Model = "different-model"
	invalid := in
	invalid.Validation = &drift
	invalid.Candidates = []video.PlacementCandidate{{Evidence: placementEvidence(eventID, fixtureID, "validation-conflict"), Outcome: discoverycontract.OutcomeDuplicate}}
	_, err = placements.CommitClipPlacement(t.Context(), invalid)
	require.ErrorContains(t, err, "different evidence")
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM event_search_candidates WHERE tweet_url=$1`, invalid.Candidates[0].Evidence.TweetURL).Scan(&followers))
	require.Zero(t, followers)

	// A genuinely different acknowledged evaluation has a distinct immutable
	// record, instead of erasing earlier evidence for the same bytes.
	drift.ID = uuid.New()
	_, err = placements.CommitClipPlacement(t.Context(), invalid)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_asset_validations WHERE asset_id=$1`, variant.ID).Scan(&records))
	require.Equal(t, 2, records)

	// Metadata reclamation never removes SQL validation history.
	require.NoError(t, assets.MarkObjectReclaimed(t.Context(), variant.ID))
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_asset_validations WHERE asset_id=$1`, variant.ID).Scan(&records))
	require.Equal(t, 2, records)

	// A new promoted variant follows the same evidence path; a transaction
	// failure after the evidence insert rolls that record back with its asset.
	promoted := newAsset(eventID, fixtureID, "validation-promoted", []uint64{4, 5, 6}, 2_000_000)
	accepted := assetValidation(promoted)
	newIn := video.ClipPlacement{EventID: eventID, FixtureID: fixtureID, Winner: promoted,
		ObservedAssetID: promoted.ID, Validation: accepted, Verified: true, ExtractedMinute: accepted.Evaluation.MatchedMinute,
		Candidates: []video.PlacementCandidate{{Evidence: placementEvidence(eventID, fixtureID, "validation-1"), Outcome: discoverycontract.OutcomePromoted}}}
	_, err = placements.CommitClipPlacement(t.Context(), newIn)
	require.Error(t, err, "candidate already belongs to another observed variant")
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_asset_validations WHERE id=$1`, accepted.ID).Scan(&records))
	require.Zero(t, records)
	newIn.Candidates[0].Evidence = placementEvidence(eventID, fixtureID, "validation-promoted")
	_, err = placements.CommitClipPlacement(t.Context(), newIn)
	require.NoError(t, err)

	// Raw SQL cannot detach evidence from its correlated event/asset identity
	// or admit a rejected/oversized/missing-payload evaluation as acceptance.
	for _, bad := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`null`)} {
		_, err = pool.Exec(t.Context(), `INSERT INTO video_asset_validations(id,asset_id,event_id,fixture_id,evidence) VALUES($1,$2,$3,$4,$5)`, uuid.New(), variant.ID, eventID, fixtureID, bad)
		require.Error(t, err)
	}
	for _, mutate := range []func(*dvision.Evidence){
		func(e *dvision.Evidence) { e.EventID = uuid.New() },
		func(e *dvision.Evidence) { e.FixtureID++ },
		func(e *dvision.Evidence) { e.Model = strings.Repeat("x", dvision.MaxEvidenceBytes) },
		func(e *dvision.Evidence) { e.Evaluation.Outcome = dvision.OutcomeRejected },
	} {
		bad := assetValidation(variant)
		mutate(bad)
		body, marshalErr := json.Marshal(bad)
		require.NoError(t, marshalErr)
		_, err = pool.Exec(t.Context(), `INSERT INTO video_asset_validations(id,asset_id,event_id,fixture_id,evidence) VALUES($1,$2,$3,$4,$5)`, bad.ID, variant.ID, bad.EventID, bad.FixtureID, body)
		require.Error(t, err)
	}
}

// TestValidationMigrationDoesNotInventHistory adopts an actual pre-table
// database and checks the new table's startup gate without mutating old assets.
func TestValidationMigrationDoesNotInventHistory(t *testing.T) {
	pool, _, assets, shares, fixtureID, eventID := setupPlacementRepo(t)
	old := insertPlacementAsset(t, assets, eventID, fixtureID, "legacy-validation", 7)
	insertPlacementShare(t, shares, old, 1)
	require.NoError(t, pool.Migrate(t.Context(), migrations.FS))
	_, err := pool.Exec(t.Context(), `DROP TABLE video_asset_validations`)
	require.NoError(t, err)
	// Retain the actual previous migration ledger, not just an unadopted
	// old table set. Only the new unapplied suffix is absent in this test DB.
	_, err = pool.Exec(t.Context(), `DELETE FROM schema_migrations WHERE version>='20260912_01_retain_accepted_validation'`)
	require.NoError(t, err)
	require.NoError(t, pool.Migrate(t.Context(), migrations.FS))
	require.NoError(t, pool.VerifyMigrations(t.Context(), migrations.FS))
	var records int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_asset_validations`).Scan(&records))
	require.Zero(t, records)
	retained, err := assets.Get(t.Context(), old.ID)
	require.NoError(t, err)
	require.Equal(t, 7, retained.Popularity)
	_, err = pool.Exec(t.Context(), `ALTER TABLE video_asset_validations DROP CONSTRAINT video_asset_validations_evidence`)
	require.NoError(t, err)
	require.ErrorContains(t, pool.VerifyMigrations(t.Context(), migrations.FS), "video_asset_validations_evidence")
}

// TestRemovedPlacementDoesNotStoreAcceptance keeps VAR terminalization ahead
// of both validation history and public mutation.
func TestRemovedPlacementDoesNotStoreAcceptance(t *testing.T) {
	pool, placements, _, _, fixtureID, eventID := setupPlacementRepo(t)
	_, err := pool.Exec(t.Context(), `UPDATE events SET removed=true,removed_reason='var',removed_at=NOW() WHERE id=$1`, eventID)
	require.NoError(t, err)
	asset := newAsset(eventID, fixtureID, "removed-validation", []uint64{1, 2, 3}, 1_000_000)
	proof := assetValidation(asset)
	in := video.ClipPlacement{EventID: eventID, FixtureID: fixtureID, Winner: asset, ObservedAssetID: asset.ID,
		Validation: proof, Verified: true, ExtractedMinute: proof.Evaluation.MatchedMinute,
		Candidates: []video.PlacementCandidate{{Evidence: placementEvidence(eventID, fixtureID, "removed-validation"), Outcome: discoverycontract.OutcomePromoted}}}
	out, err := placements.CommitClipPlacement(t.Context(), in)
	require.NoError(t, err)
	require.True(t, out.EventRemoved)
	var records int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_asset_validations WHERE event_id=$1`, eventID).Scan(&records))
	require.Zero(t, records)
}
