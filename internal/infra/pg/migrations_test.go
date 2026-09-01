// Integration tests for ordered migration adoption, repair, and rollback.
package pg_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/migrations"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

func setupMigrationPool(t *testing.T) (context.Context, *pg.Pool, *testFixture) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	connStr := runTestPostgres(ctx, t)
	fx := newTestFixture()
	pool, err := pg.New(ctx, config.PGConfig{
		DSN: connStr, MaxConns: 5, MinConns: 1, ConnectTimeout: 10 * time.Second,
	}, fx.ins)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool, fx
}

func TestMigrateAdoptsCurrentSchemaAndIsIdempotent(t *testing.T) {
	ctx, pool, fx := setupMigrationPool(t)
	if err := pool.VerifyMigrations(ctx, migrations.FS); err == nil {
		t.Fatal("application verification accepted a missing migration ledger")
	}
	if err := pool.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraPG, vocabulary.ActionMigrationApplied) {
		t.Fatalf("missing migration-applied log: %+v", fx.log.Snapshot())
	}
	var migrationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count migration ledger: %v", err)
	}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	wantMigrationCount := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			wantMigrationCount++
		}
	}
	if migrationCount != wantMigrationCount {
		t.Fatalf("migration rows = %d, want %d", migrationCount, wantMigrationCount)
	}
	var schemaHash string
	if err := pool.QueryRow(ctx, `SELECT schema_hash FROM schema_version WHERE id = 1`).Scan(&schemaHash); err != nil {
		t.Fatalf("read schema stamp: %v", err)
	}
	if schemaHash != pg.SchemaHash() {
		t.Fatalf("schema stamp = %s, want %s", schemaHash, pg.SchemaHash())
	}
	if err := pool.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("idempotent Migrate: %v", err)
	}
	if err := pool.VerifyMigrations(ctx, migrations.FS); err != nil {
		t.Fatalf("application VerifyMigrations: %v", err)
	}
	var retryCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&retryCount); err != nil {
		t.Fatalf("count retry ledger: %v", err)
	}
	if retryCount != migrationCount {
		t.Fatalf("retry migration rows = %d, want %d", retryCount, migrationCount)
	}
}

func migrationPrefixThrough(t *testing.T, through string) fstest.MapFS {
	t.Helper()
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	prefix := fstest.MapFS{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() > through {
			continue
		}
		data, err := fs.ReadFile(migrations.FS, entry.Name())
		if err != nil {
			t.Fatalf("read migration %s: %v", entry.Name(), err)
		}
		prefix[entry.Name()] = &fstest.MapFile{Data: data}
	}
	return prefix
}

// TestMigrateTerminalizesRemovedEventCandidates proves FF-084's bounded data
// repair touches only pending candidates whose owning event is already removed.
func TestMigrateTerminalizesRemovedEventCandidates(t *testing.T) {
	ctx, pool, _ := setupMigrationPool(t)
	if err := pool.Migrate(ctx, migrationPrefixThrough(t, "20260831_02_retain_accepted_video_variants.sql")); err != nil {
		t.Fatalf("apply pre-FF-084 chain: %v", err)
	}
	fixtureID := int64(9403)
	fixture := makeStaging(fixtureID, time.Date(2026, 8, 31, 22, 0, 0, 0, time.UTC))
	if err := fixture.Activate(time.Date(2026, 8, 31, 21, 55, 0, 0, time.UTC)); err != nil {
		t.Fatalf("activate fixture: %v", err)
	}
	if err := pg.NewFixtureRepo(pool).Insert(ctx, fixture); err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	removedEventID := uuid.New()
	liveEventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, detail, team_id,
			team_name, minute, removed, removed_reason, removed_at
		) VALUES
			($2, $1, 'migration_removed_goal', 'goal', 'normal goal', 1,
			 'Home', 30, true, 'var', NOW()),
			($3, $1, 'migration_live_goal', 'goal', 'normal goal', 2,
			 'Away', 40, false, NULL, NULL)
	`, fixtureID, removedEventID, liveEventID); err != nil {
		t.Fatalf("seed migration events: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_search_candidates (
			event_id, fixture_id, search_attempt, query, tweet_url,
			video_page_url, outcome_class
		) VALUES
			($2, $1, 1, 'query', 'https://x.com/removed/status/1', 'video', 'pending'),
			($3, $1, 1, 'query', 'https://x.com/live/status/2', 'video', 'pending')
	`, fixtureID, removedEventID, liveEventID); err != nil {
		t.Fatalf("seed migration candidates: %v", err)
	}

	if err := pool.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("apply FF-084 migration: %v", err)
	}
	var removedOutcome, removedReason, liveOutcome string
	if err := pool.QueryRow(ctx, `
		SELECT outcome_class, reject_reason FROM event_search_candidates
		WHERE event_id = $1
	`, removedEventID).Scan(&removedOutcome, &removedReason); err != nil {
		t.Fatalf("read removed candidate: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT outcome_class FROM event_search_candidates WHERE event_id = $1
	`, liveEventID).Scan(&liveOutcome); err != nil {
		t.Fatalf("read live candidate: %v", err)
	}
	if removedOutcome != "rejected" || removedReason != "event_removed" || liveOutcome != "pending" {
		t.Fatalf("migration outcomes removed=%s/%s live=%s", removedOutcome, removedReason, liveOutcome)
	}
}

func TestMigrateRepairsPartiallyAppliedHistoricalChange(t *testing.T) {
	ctx, pool, _ := setupMigrationPool(t)
	if _, err := pool.Exec(ctx, `
		ALTER TABLE event_search_candidates DROP COLUMN credited_asset_id CASCADE;
		ALTER TABLE fixtures DROP CONSTRAINT fixtures_terminal_observation_state;
		ALTER TABLE event_search_candidates DROP CONSTRAINT event_search_candidates_event_fixture_fkey;
		ALTER TABLE video_shares DROP CONSTRAINT video_shares_asset_event_fkey;
		ALTER TABLE video_assets DROP CONSTRAINT video_assets_superseded_identity_fkey;
		ALTER TABLE video_assets DROP CONSTRAINT video_assets_event_fixture_fkey;
	`); err != nil {
		t.Fatalf("model partial historical schema: %v", err)
	}
	if err := pool.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("repair Migrate: %v", err)
	}
	for _, relation := range []string{"video_shares_event_asset", "event_search_candidates_credited_asset"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, relation).Scan(&exists); err != nil {
			t.Fatalf("check relation %s: %v", relation, err)
		}
		if !exists {
			t.Errorf("relation %s was not repaired", relation)
		}
	}
	var columnExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name='event_search_candidates'
			  AND column_name='credited_asset_id'
		)
	`).Scan(&columnExists); err != nil {
		t.Fatalf("check repaired column: %v", err)
	}
	if !columnExists {
		t.Error("credited_asset_id was not repaired")
	}
}

func TestMigrateRejectsIncompleteBaseline(t *testing.T) {
	ctx, pool, fx := setupMigrationPool(t)
	if _, err := pool.Exec(ctx, `DROP TABLE event_log CASCADE`); err != nil {
		t.Fatalf("remove required baseline table: %v", err)
	}
	if err := pool.Migrate(ctx, migrations.FS); err == nil {
		t.Fatal("Migrate accepted an incomplete baseline")
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraPG, vocabulary.ActionSchemaDrift) {
		t.Fatalf("missing schema-drift log: %+v", fx.log.Snapshot())
	}
	var ledgerExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&ledgerExists); err != nil {
		t.Fatalf("check rolled-back ledger: %v", err)
	}
	if ledgerExists {
		t.Error("failed baseline left a migration ledger behind")
	}
}

func TestMigratePreflightRejectsCrossOwnedHistory(t *testing.T) {
	ctx, pool, _ := setupMigrationPool(t)
	fixtures := pg.NewFixtureRepo(pool)
	completedAt := time.Date(2026, 8, 28, 20, 0, 0, 0, time.UTC)
	first := completedFixture(t, ctx, fixtures, 9401, completedAt)
	second := completedFixture(t, ctx, fixtures, 9402, completedAt)
	eventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, minute
		) VALUES ($1, $2, 'migration_preflight_goal', 'goal', 'normal goal', 1, 'Test', 30)
	`, eventID, first.ID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE video_assets DROP CONSTRAINT video_assets_event_fixture_fkey`); err != nil {
		t.Fatalf("remove correlated identity constraint: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO video_assets (
			id, event_id, fixture_id, s3_bucket, s3_key,
			md5, frame_hashes, width, height, duration_ms, file_size_bytes
		) VALUES ($1, $2, $3, 'test', 'cross-owned.mp4', $4, $5, 1280, 720, 7000, 1000000)
	`, uuid.New(), eventID, second.ID, []byte("0123456789abcdef"), make([]byte, 8)); err != nil {
		t.Fatalf("seed cross-owned history: %v", err)
	}

	err := pool.Migrate(ctx, migrations.FS)
	if err == nil || !strings.Contains(err.Error(), "FF-071 preflight") {
		t.Fatalf("Migrate error = %v, want FF-071 preflight refusal", err)
	}
	var ledgerExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&ledgerExists); err != nil {
		t.Fatalf("check rolled-back ledger: %v", err)
	}
	if ledgerExists {
		t.Error("failed preflight left a migration ledger behind")
	}
}

func TestMigrateRollsBackFailedMigrationAndCanRetry(t *testing.T) {
	ctx, pool, _ := setupMigrationPool(t)
	version := "20990101_01_interrupted_probe.sql"
	header := "-- schema-hash: " + pg.SchemaHash() + "\n"
	broken := fstest.MapFS{
		version: {Data: []byte(header + "CREATE TABLE interrupted_probe (id int);\nSELECT 1 / 0;\n")},
	}
	if err := pool.Migrate(ctx, broken); err == nil {
		t.Fatal("broken migration unexpectedly succeeded")
	}
	var probeExists, ledgerExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.interrupted_probe') IS NOT NULL`).Scan(&probeExists); err != nil {
		t.Fatalf("check probe rollback: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&ledgerExists); err != nil {
		t.Fatalf("check ledger rollback: %v", err)
	}
	if probeExists || ledgerExists {
		t.Fatalf("failed migration leaked state: probe=%v ledger=%v", probeExists, ledgerExists)
	}

	fixed := fstest.MapFS{
		version: {Data: []byte(header + "CREATE TABLE interrupted_probe (id int);\n")},
	}
	if err := pool.Migrate(ctx, fixed); err != nil {
		t.Fatalf("retry fixed migration: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.interrupted_probe') IS NOT NULL`).Scan(&probeExists); err != nil {
		t.Fatalf("check retry probe: %v", err)
	}
	if !probeExists {
		t.Error("fixed retry did not commit migration")
	}
	mutated := fstest.MapFS{
		version: {Data: []byte(header + "CREATE TABLE interrupted_probe (id bigint);\n")},
	}
	if err := pool.VerifyMigrations(ctx, mutated); err == nil {
		t.Fatal("verification accepted an edited applied migration")
	}
}
