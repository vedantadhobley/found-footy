// Real database selection tests cover atomic restoration, auditability and unchanged public reads.
package pg_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/migrations"
)

// seedSelection uses the actual placement transaction to build an attributed bridge chain.
func seedSelection(t *testing.T, neverPublic bool, aVotes int) (*pg.Pool, *pg.PlacementRepo, *pg.AssetRepo, *pg.ShareRepo, video.SelectionRequest, []*video.Asset, string) {
	t.Helper()
	pool, repo, assets, shares, fixtureID, eventID := setupPlacementRepo(t)
	var nodes []*video.Asset
	for i, hashes := range [][]uint64{{0, 0, 0}, {0, 0, 0, ^uint64(0), ^uint64(0), ^uint64(0)}, {^uint64(0), ^uint64(0), ^uint64(0)}} {
		a := newAsset(eventID, fixtureID, fmt.Sprintf("selection-%d", i), hashes, 1000000)
		a.FirstSeenAt = time.Date(2026, 9, 12, 12, i, 0, 0, time.UTC)
		nodes = append(nodes, a)
	}
	candidates := func(index, count int) []video.PlacementCandidate {
		var list []video.PlacementCandidate
		for i := range count {
			list = append(list, video.PlacementCandidate{Evidence: placementEvidence(eventID, fixtureID, fmt.Sprintf("selection-%d-%d", index, i)), Outcome: discoverycontract.OutcomeDuplicate})
		}
		return list
	}
	promote := func(index, count int, losers ...uuid.UUID) string {
		list := candidates(index, count)
		list[0].Outcome = discoverycontract.OutcomePromoted
		proof := assetValidation(nodes[index])
		out, err := repo.CommitClipPlacement(t.Context(), video.ClipPlacement{EventID: eventID, FixtureID: fixtureID, Winner: nodes[index], ObservedAssetID: nodes[index].ID,
			Verified: true, ExtractedMinute: proof.Evaluation.MatchedMinute, Validation: proof, LoserAssetIDs: losers, Candidates: list})
		require.NoError(t, err)
		return out.ShareID
	}
	aShare := ""
	if neverPublic {
		promote(1, 5)
		variant := *nodes[0]
		variant.SupersededBySet(nodes[1].ID)
		proof := assetValidation(&variant)
		_, err := repo.CommitClipPlacement(t.Context(), video.ClipPlacement{EventID: eventID, FixtureID: fixtureID, Variant: &variant, WinnerAssetID: nodes[1].ID, ObservedAssetID: variant.ID,
			Verified: true, ExtractedMinute: proof.Evaluation.MatchedMinute, Validation: proof, Candidates: candidates(0, aVotes)})
		require.NoError(t, err)
	} else {
		aShare = promote(0, aVotes)
		promote(1, 5, nodes[0].ID)
	}
	promote(2, 7, nodes[1].ID)
	snapshot, err := repo.LoadSelection(t.Context(), eventID)
	require.NoError(t, err)
	fingerprint, err := snapshot.Fingerprint()
	require.NoError(t, err)
	request := video.SelectionRequest{ID: uuid.New(), EventID: eventID, FixtureID: fixtureID, SnapshotHash: fingerprint,
		Policy: video.SelectionPolicy{MaxHamming: 0, MinRun: 3, MaxGaps: 0}, PreparedAssetIDs: []uuid.UUID{nodes[0].ID, nodes[1].ID, nodes[2].ID}}
	return pool, repo, assets, shares, request, nodes, aShare
}

// TestSelectionConcurrentSnapshots permits one plan and rejects the stale competing plan.
func TestSelectionConcurrentSnapshots(t *testing.T) {
	pool, repo, _, _, request, _, _ := seedSelection(t, false, 2)
	errorsOut := make(chan error, 2)
	for range 2 {
		other := request
		other.ID = uuid.New()
		go func() { _, err := repo.CommitSelection(t.Context(), other); errorsOut <- err }()
	}
	passed, stale := 0, 0
	for range 2 {
		err := <-errorsOut
		switch {
		case err == nil:
			passed++
		case errors.Is(err, video.ErrSelectionStale):
			stale++
		default:
			t.Fatal(err)
		}
	}
	require.Equal(t, 1, passed)
	require.Equal(t, 1, stale)
	var rows int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_selection_commits WHERE event_id=$1`, request.EventID).Scan(&rows))
	require.Equal(t, 1, rows)
}

// isolateSelectionSingleton removes bridge overlap with A to isolate FF-078 from direct support.
func isolateSelectionSingleton(t *testing.T, pool *pg.Pool, repo *pg.PlacementRepo, request *video.SelectionRequest, nodes []*video.Asset) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `UPDATE video_assets SET frame_hashes=(SELECT frame_hashes FROM video_assets WHERE id=$2) WHERE id=$1`, nodes[1].ID, nodes[2].ID)
	require.NoError(t, err)
	snapshot, err := repo.LoadSelection(t.Context(), request.EventID)
	require.NoError(t, err)
	request.SnapshotHash, err = snapshot.Fingerprint()
	require.NoError(t, err)
}

// TestSelectionVisibilityAsymmetry applies the unchanged FF-078 truth table to direct scores.
func TestSelectionVisibilityAsymmetry(t *testing.T) {
	for _, aVerified := range []bool{false, true} {
		for _, bVerified := range []bool{false, true} {
			t.Run(fmt.Sprintf("A=%t/B=%t", aVerified, bVerified), func(t *testing.T) {
				pool, repo, _, shares, request, nodes, _ := seedSelection(t, false, 1)
				isolateSelectionSingleton(t, pool, repo, &request, nodes)
				// Synthetic historical verification categories isolate the read-policy
				// truth table; this is not revalidation of the saved incident.
				_, err := pool.Exec(t.Context(), `UPDATE video_shares SET timestamp_verified=CASE WHEN asset_id=$2 THEN $3::boolean ELSE $4::boolean END WHERE event_id=$1`, request.EventID, nodes[0].ID, aVerified, bVerified)
				require.NoError(t, err)
				snapshot, err := repo.LoadSelection(t.Context(), request.EventID)
				require.NoError(t, err)
				request.SnapshotHash, err = snapshot.Fingerprint()
				require.NoError(t, err)
				_, err = repo.CommitSelection(t.Context(), request)
				require.NoError(t, err)
				live, err := shares.ListLiveForEvent(t.Context(), request.EventID)
				require.NoError(t, err)
				want := 1
				if aVerified && !bVerified {
					want = 2
				}
				require.Len(t, live, want)
			})
		}
	}
}

// TestSelectionTransactionRestoresSharesCreditAndAliases verifies the entire write/read boundary.
func TestSelectionTransactionRestoresSharesCreditAndAliases(t *testing.T) {
	for _, neverPublic := range []bool{false, true} {
		t.Run(fmt.Sprint(neverPublic), func(t *testing.T) {
			pool, repo, assets, shares, request, nodes, oldShare := seedSelection(t, neverPublic, 2)
			out, err := repo.CommitSelection(t.Context(), request)
			require.NoError(t, err)
			require.Equal(t, []uuid.UUID{nodes[0].ID}, out.Plan.Restored)
			require.NotEmpty(t, out.ShareIDs[nodes[0].ID])
			if oldShare != "" {
				require.Equal(t, oldShare, out.ShareIDs[nodes[0].ID])
			}
			own, err := shares.Get(t.Context(), out.ShareIDs[nodes[0].ID])
			require.NoError(t, err)
			require.Equal(t, 22, *own.ExtractedMinute)
			a, err := assets.Get(t.Context(), nodes[0].ID)
			require.NoError(t, err)
			require.Nil(t, a.SupersededBy)
			require.Equal(t, 7, a.Popularity)
			b, err := assets.Get(t.Context(), nodes[2].ID)
			require.NoError(t, err)
			require.Equal(t, 12, b.Popularity)
			live, err := shares.ListLiveForEvent(t.Context(), request.EventID)
			require.NoError(t, err)
			require.Len(t, live, 2)
			require.Equal(t, 12, live[0].Popularity)
			require.Equal(t, 7, live[1].Popularity)
			activities := videoactivity.PersistActivities{Assets: assets, Shares: shares}
			recovered, err := activities.LoadEventAssets(t.Context(), videoactivity.LoadEventAssetsInput{EventID: request.EventID})
			require.NoError(t, err)
			aliases := map[string]uuid.UUID{}
			for _, alias := range recovered.ExactAliases {
				aliases[alias.MD5] = alias.AssetID
			}
			require.Equal(t, nodes[0].ID, aliases[hex.EncodeToString(nodes[0].MD5)])
			require.Equal(t, nodes[2].ID, aliases[hex.EncodeToString(nodes[1].MD5)])
			retry, err := repo.CommitSelection(t.Context(), request)
			require.NoError(t, err)
			require.True(t, retry.Replayed)
			require.Equal(t, out.Plan, retry.Plan)
			var records, credits int
			require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_selection_commits WHERE event_id=$1`, request.EventID).Scan(&records))
			require.Equal(t, 1, records)
			require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM event_search_candidates WHERE observed_asset_id=$1 AND credited_asset_id=$1`, nodes[0].ID).Scan(&credits))
			require.Equal(t, 2, credits)
			request.Policy.MaxHamming = 1
			_, err = repo.CommitSelection(t.Context(), request)
			require.ErrorContains(t, err, "different content")
		})
	}
}

// TestSelectionSingletonVisibilityAndRecurrence pins the existing one-to-two popularity boundary.
func TestSelectionSingletonVisibilityAndRecurrence(t *testing.T) {
	pool, repo, assets, shares, request, nodes, _ := seedSelection(t, false, 1)
	isolateSelectionSingleton(t, pool, repo, &request, nodes)
	out, err := repo.CommitSelection(t.Context(), request)
	require.NoError(t, err)
	live, err := shares.ListLiveForEvent(t.Context(), request.EventID)
	require.NoError(t, err)
	require.Len(t, live, 1)
	share, err := shares.Get(t.Context(), out.ShareIDs[nodes[0].ID])
	require.NoError(t, err)
	require.Equal(t, video.ShareStateActive, share.State)
	recurrence := video.ClipPlacement{EventID: request.EventID, FixtureID: request.FixtureID, ObservedAssetID: nodes[0].ID, WinnerAssetID: nodes[0].ID, Verified: true,
		Candidates: []video.PlacementCandidate{{Evidence: placementEvidence(request.EventID, request.FixtureID, "restored-recurrence"), Outcome: discoverycontract.OutcomeDuplicate}}}
	prepareSelectionRequest(t, repo, &recurrence)
	for range 2 {
		_, err = repo.CommitClipPlacement(t.Context(), recurrence)
		require.NoError(t, err)
	}
	live, err = shares.ListLiveForEvent(t.Context(), request.EventID)
	require.NoError(t, err)
	require.Len(t, live, 2)
	require.Equal(t, 2, live[1].Popularity)
	_, err = repo.CommitSelection(t.Context(), request)
	require.NoError(t, err)
	a, err := assets.Get(t.Context(), nodes[0].ID)
	require.NoError(t, err)
	require.Equal(t, 2, a.Popularity, "receipt retry must not revert a later exact source")
}

// TestSelectionStaleRemovalAndRollback protects every mutation and its immutable receipt together.
func TestSelectionStaleRemovalAndRollback(t *testing.T) {
	pool, repo, _, _, request, nodes, _ := seedSelection(t, false, 2)
	_, err := pool.Exec(t.Context(), `UPDATE video_assets SET popularity=popularity+1 WHERE id=$1`, nodes[2].ID)
	require.NoError(t, err)
	_, err = repo.CommitSelection(t.Context(), request)
	require.ErrorIs(t, err, video.ErrSelectionStale)
	_, err = pool.Exec(t.Context(), `UPDATE video_assets SET popularity=popularity-1 WHERE id=$1`, nodes[2].ID)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `CREATE FUNCTION fail_selection_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'receipt failure'; END $$;
		CREATE TRIGGER fail_receipt BEFORE INSERT ON video_selection_commits FOR EACH ROW EXECUTE FUNCTION fail_selection_receipt()`)
	require.NoError(t, err)
	_, err = repo.CommitSelection(t.Context(), request)
	require.ErrorContains(t, err, "receipt failure")
	after, err := repo.LoadSelection(t.Context(), request.EventID)
	require.NoError(t, err)
	hash, err := after.Fingerprint()
	require.NoError(t, err)
	require.Equal(t, request.SnapshotHash, hash, "failed receipt rolls back shares, pointers, counts and sources")
	_, err = pool.Exec(t.Context(), `UPDATE events SET removed=true,removed_reason='var',removed_at=NOW() WHERE id=$1`, request.EventID)
	require.NoError(t, err)
	_, err = repo.CommitSelection(t.Context(), request)
	require.ErrorIs(t, err, video.ErrSelectionRemoved)
}

// TestSelectionMigrationAdoptsPreviousLedger proves an additive empty receipt store, not a historical repair.
func TestSelectionMigrationAdoptsPreviousLedger(t *testing.T) {
	pool, _, _, _, _, _, _ := seedSelection(t, false, 2)
	require.NoError(t, pool.Migrate(t.Context(), migrations.FS))
	_, err := pool.Exec(t.Context(), `DROP TABLE video_selection_commits; DELETE FROM schema_migrations WHERE version>='20260912_02_add_reversible_selection'`)
	require.NoError(t, err)
	require.NoError(t, pool.Migrate(t.Context(), migrations.FS))
	require.NoError(t, pool.VerifyMigrations(t.Context(), migrations.FS))
	var rows int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_selection_commits`).Scan(&rows))
	require.Zero(t, rows)
}

// TestDirectSupportMigration accepts new receipts without changing existing evidence, receipts or scores.
func TestDirectSupportMigration(t *testing.T) {
	pool, repo, _, _, request, _, _ := seedSelection(t, false, 2)
	before, err := repo.LoadSelection(t.Context(), request.EventID)
	require.NoError(t, err)
	require.NoError(t, pool.Migrate(t.Context(), migrations.FS))
	// Rehearse the exact prior constraint rather than relying on a fresh schema.
	prior, err := os.ReadFile("../../../migrations/20260912_02_add_reversible_selection.sql")
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `COMMENT ON COLUMN video_assets.popularity IS NULL;
		COMMENT ON COLUMN event_search_candidates.credited_asset_id IS NULL;
		DROP TABLE video_selection_commits;
		DELETE FROM schema_migrations WHERE version='20260913_01_enable_direct_support'`)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), string(prior))
	require.NoError(t, err)
	oldID := uuid.New()
	_, err = pool.Exec(t.Context(), `INSERT INTO video_selection_commits(id,event_id,fixture_id,request_hash,snapshot_hash,policy,before_state,result)
		VALUES($1,$2,$3,repeat('a',64),repeat('b',64),'{}','[]','{"Plan":{"Version":"direct-restoration-v1"}}')`, oldID, request.EventID, request.FixtureID)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, pool.Migrate(t.Context(), migrations.FS))
		require.NoError(t, pool.VerifyMigrations(t.Context(), migrations.FS))
	}
	var oldVersion string
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT result#>>'{Plan,Version}' FROM video_selection_commits WHERE id=$1`, oldID).Scan(&oldVersion))
	require.Equal(t, "direct-restoration-v1", oldVersion)
	for _, field := range []struct{ table, column, description string }{
		{"video_assets", "popularity", "Scores overlap across clips"},
		{"event_search_candidates", "credited_asset_id", "not exclusive ownership of direct support"},
	} {
		var comment string
		require.NoError(t, pool.QueryRow(t.Context(), `SELECT col_description(attrelid,attnum)
			FROM pg_attribute WHERE attrelid=$1::regclass AND attname=$2`, field.table, field.column).Scan(&comment))
		require.Contains(t, comment, field.description)
	}
	after, err := repo.LoadSelection(t.Context(), request.EventID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	out, err := repo.CommitSelection(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, video.SelectionVersion, out.Plan.Version)
	_, err = pool.Exec(t.Context(), `UPDATE video_selection_commits SET result=jsonb_set(result,'{Plan,Version}','"invalid"') WHERE id=$1`, request.ID)
	require.ErrorContains(t, err, "video_selection_commits_record", "unknown receipt versions must still fail closed")
}
