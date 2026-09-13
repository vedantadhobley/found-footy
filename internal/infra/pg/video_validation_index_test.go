// Validation-index regressions bound unrelated-history reads and preserve migration/evidence semantics.
package pg_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/migrations"
)

// validationPlan captures work and access paths, not machine-dependent latency thresholds.
type validationPlan struct {
	IndexName  string           `json:"Index Name"`
	IndexCond  string           `json:"Index Cond"`
	Rows       int              `json:"Actual Rows"`
	Filtered   int              `json:"Rows Removed by Filter"`
	HitBlocks  int              `json:"Shared Hit Blocks"`
	ReadBlocks int              `json:"Shared Read Blocks"`
	Plans      []validationPlan `json:"Plans"`
}

// eventLookup reports the index's restriction and any unrelated rows visited by filtering.
func (p validationPlan) eventLookup() (string, int) {
	condition, filtered := "", p.Filtered
	if p.IndexName == "video_asset_validations_event" {
		condition = p.IndexCond
	}
	for _, child := range p.Plans {
		childCondition, childFiltered := child.eventLookup()
		if childCondition != "" {
			condition = childCondition
		}
		filtered += childFiltered
	}
	return condition, filtered
}

// explainValidationLookup exercises the exact DISTINCT ON access pattern in loadSelectionTx.
func explainValidationLookup(t *testing.T, pool *pg.Pool, eventID uuid.UUID) validationPlan {
	t.Helper()
	var body []byte
	err := pool.QueryRow(t.Context(), `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
		SELECT DISTINCT ON (asset_id) asset_id,evidence
		FROM video_asset_validations WHERE event_id=$1 ORDER BY asset_id,recorded_at,id`, eventID).Scan(&body)
	require.NoError(t, err)
	var plans []struct {
		Plan validationPlan
	}
	require.NoError(t, json.Unmarshal(body, &plans))
	require.Len(t, plans, 1)
	return plans[0].Plan
}

// TestValidationEventIndexBoundsHistoryAndMigrates proves both fresh and upgrade paths
// avoid unrelated retained evidence without changing any selection snapshot.
func TestValidationEventIndexBoundsHistoryAndMigrates(t *testing.T) {
	pool, repo, _, _, request, nodes, _ := seedSelection(t, true, 2)
	require.NoError(t, pool.Migrate(t.Context(), migrations.FS))
	otherEvent, otherAsset := uuid.New(), uuid.New()
	_, err := pool.Exec(t.Context(), `INSERT INTO events
		(id,fixture_id,natural_key,event_type,detail,team_id,team_name,minute)
		VALUES($1,$2,'validation-index-unrelated','goal','Normal Goal',40,'Liverpool',23)`, otherEvent, request.FixtureID)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `INSERT INTO video_assets
		(id,event_id,fixture_id,s3_bucket,s3_key,md5,hash_version,frame_hashes,width,height,duration_ms,file_size_bytes,first_seen_at)
		SELECT $1,$2,fixture_id,s3_bucket,s3_key,md5,hash_version,frame_hashes,width,height,duration_ms,file_size_bytes,first_seen_at
		FROM video_assets WHERE id=$3`, otherAsset, otherEvent, nodes[0].ID)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `INSERT INTO video_asset_validations(id,asset_id,event_id,fixture_id,evidence)
		SELECT generated.id,$1,$2,$3,
		  evidence || jsonb_build_object('id',generated.id::text,'event_id',($2::uuid)::text)
		FROM (SELECT gen_random_uuid() AS id FROM generate_series(1,20000)) generated
		CROSS JOIN (SELECT evidence FROM video_asset_validations WHERE asset_id=$4 LIMIT 1) original`,
		otherAsset, otherEvent, request.FixtureID, nodes[0].ID)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `ANALYZE video_asset_validations`)
	require.NoError(t, err)
	assertLocalLookup := func() {
		t.Helper()
		var definition string
		require.NoError(t, pool.QueryRow(t.Context(), `SELECT pg_get_indexdef('video_asset_validations_event'::regclass)`).Scan(&definition))
		require.Contains(t, definition, "(event_id, asset_id, recorded_at, id)")
		plan := explainValidationLookup(t, pool, request.EventID)
		condition, filtered := plan.eventLookup()
		require.Contains(t, condition, request.EventID.String(), "event equality must be an index condition, not a post-scan filter")
		require.Zero(t, filtered, "the lookup must not scan unrelated retained evaluations")
		require.Equal(t, 3, plan.Rows)
		t.Logf("indexed lookup: rows=%d filtered=%d shared_blocks=%d", plan.Rows, filtered, plan.HitBlocks+plan.ReadBlocks)
	}
	assertLocalLookup()
	before, err := repo.LoadSelection(t.Context(), request.EventID)
	require.NoError(t, err)
	beforeHash, err := before.Fingerprint()
	require.NoError(t, err)

	// Only the disposable database is rolled back to the prior migration prefix.
	_, err = pool.Exec(t.Context(), `DROP INDEX video_asset_validations_event;
		DELETE FROM schema_migrations WHERE version>='20260912_03_index_event_validation_lookup'`)
	require.NoError(t, err)
	oldPlan := explainValidationLookup(t, pool, request.EventID)
	_, filtered := oldPlan.eventLookup()
	t.Logf("previous lookup: rows=%d filtered=%d shared_blocks=%d", oldPlan.Rows, filtered, oldPlan.HitBlocks+oldPlan.ReadBlocks)
	require.NoError(t, pool.Migrate(t.Context(), migrations.FS))
	require.NoError(t, pool.Migrate(t.Context(), migrations.FS), "index migration must be retry-safe")
	require.NoError(t, pool.VerifyMigrations(t.Context(), migrations.FS))
	assertLocalLookup()
	after, err := repo.LoadSelection(t.Context(), request.EventID)
	require.NoError(t, err)
	afterHash, err := after.Fingerprint()
	require.NoError(t, err)
	require.Equal(t, beforeHash, afterHash, "index creation must not change evidence, selection or votes")
	var evaluations int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM video_asset_validations`).Scan(&evaluations))
	require.Equal(t, 20003, evaluations)
	_, err = pool.Exec(t.Context(), `DROP INDEX video_asset_validations_event`)
	require.NoError(t, err)
	require.ErrorContains(t, pool.VerifyMigrations(t.Context(), migrations.FS), "video_asset_validations_event")
}
