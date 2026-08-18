// video_repo_test.go — testcontainers tests for AssetRepo + ShareRepo
// (#164a). Real Postgres with the app schema; reuses runTestPostgres +
// completedFixture from the sibling pg_test files. Covers InsertAsset
// idempotency (ON CONFLICT md5), frame-hash BYTEA round-trip,
// BumpPopularity, and the atomic RebalanceRanks reorder.
package pg_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// setupVideoRepos spins a fresh Postgres and returns the two video repos
// plus a seeded (fixtureID, eventID) to satisfy the FK parents.
func setupVideoRepos(t *testing.T) (context.Context, *pg.AssetRepo, *pg.ShareRepo, int64, uuid.UUID) {
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

	fixtureRepo := pg.NewFixtureRepo(pool)
	f := completedFixture(t, ctx, fixtureRepo, 9100, time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))

	eventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, minute)
		VALUES ($1, $2, '517_26438_Goal_1', 'goal', 'Normal Goal', 517, 'Galatasaray', 71)
	`, eventID, f.ID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	return ctx, pg.NewAssetRepo(pool), pg.NewShareRepo(pool), f.ID, eventID
}

func newAsset(eventID uuid.UUID, fixtureID int64, md5 string, frames []uint64, size int64) *video.Asset {
	return video.NewAsset(
		eventID, fixtureID, "found-footy", "9100/asset.mp4",
		[]byte(md5), video.CurrentFrameHashVersion(0.1), frames, 1280, 720, 6677, size,
		time.Date(2026, 8, 3, 12, 5, 0, 0, time.UTC),
	)
}

func TestAssetRepo_InsertIdempotentAndBump(t *testing.T) {
	ctx, assets, _, fixtureID, eventID := setupVideoRepos(t)

	a := newAsset(eventID, fixtureID, "md5aaaaaaaaaaaaa1", []uint64{1, 2, 4, 8, 0xdeadbeefcafef00d}, 1_000_000)

	inserted, err := assets.InsertAsset(ctx, a)
	if err != nil {
		t.Fatalf("InsertAsset: %v", err)
	}
	if !inserted {
		t.Fatal("first InsertAsset should report inserted=true")
	}

	// Same (event_id, md5) — ON CONFLICT DO NOTHING → not inserted.
	dup := newAsset(eventID, fixtureID, "md5aaaaaaaaaaaaa1", []uint64{9, 9, 9}, 2_000_000)
	inserted, err = assets.InsertAsset(ctx, dup)
	if err != nil {
		t.Fatalf("InsertAsset dup: %v", err)
	}
	if inserted {
		t.Error("duplicate md5 InsertAsset should report inserted=false")
	}

	got, err := assets.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got.FrameHashes, a.FrameHashes) {
		t.Errorf("FrameHashes round-trip = %v, want %v", got.FrameHashes, a.FrameHashes)
	}
	if got.FrameHashVersion != a.FrameHashVersion {
		t.Errorf("FrameHashVersion round-trip = %q, want %q", got.FrameHashVersion, a.FrameHashVersion)
	}
	if got.Popularity != 1 {
		t.Errorf("Popularity = %d, want 1", got.Popularity)
	}

	if err := assets.AddPopularity(ctx, a.ID, 1); err != nil {
		t.Fatalf("AddPopularity +1: %v", err)
	}
	got, _ = assets.Get(ctx, a.ID)
	if got.Popularity != 2 {
		t.Errorf("Popularity after +1 = %d, want 2", got.Popularity)
	}
	// add-N: a clip that absorbed gate md5-dups transfers them in one write (#180).
	if err := assets.AddPopularity(ctx, a.ID, 3); err != nil {
		t.Fatalf("AddPopularity +3: %v", err)
	}
	got, _ = assets.Get(ctx, a.ID)
	if got.Popularity != 5 {
		t.Errorf("Popularity after +3 = %d, want 5", got.Popularity)
	}

	if err := assets.AddPopularity(ctx, uuid.New(), 1); err != video.ErrNotFound {
		t.Errorf("AddPopularity on missing = %v, want ErrNotFound", err)
	}
}

func TestFrameHashVersionMigrationBackfillsLegacyRows(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	connStr := runTestPostgres(ctx, t)
	fx := newTestFixture()
	pool, err := pg.New(ctx, config.PGConfig{
		DSN: connStr, MaxConns: 5, MinConns: 1, ConnectTimeout: 10 * time.Second,
	}, fx.ins)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}
	defer pool.Close()

	fixtureRepo := pg.NewFixtureRepo(pool)
	fixture := completedFixture(t, ctx, fixtureRepo, 9101, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	eventID, assetID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, minute)
		VALUES ($1, $2, 'migration_goal_1', 'goal', 'Normal Goal', 1, 'Test', 1)
	`, eventID, fixture.ID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE video_assets DROP COLUMN hash_version"); err != nil {
		t.Fatalf("model pre-migration schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO video_assets (
			id, event_id, fixture_id, s3_bucket, s3_key, md5, frame_hashes,
			width, height, duration_ms, file_size_bytes, popularity, first_seen_at
		) VALUES ($1,$2,$3,'found-footy','legacy.mp4',$4,$5,1280,720,7000,1000,1,NOW())
	`, assetID, eventID, fixture.ID, []byte("0123456789abcdef"), make([]byte, 8)); err != nil {
		t.Fatalf("seed legacy asset: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_version (
			id int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			schema_hash text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		);
		INSERT INTO schema_version (id, schema_hash) VALUES (1, 'old-schema')
		ON CONFLICT (id) DO UPDATE SET schema_hash = EXCLUDED.schema_hash
	`); err != nil {
		t.Fatalf("seed schema stamp: %v", err)
	}

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	migrationPath := filepath.Join(filepath.Dir(filename), "..", "..", "..", "migrations",
		"20260817_01_add_video_asset_hash_version.sql")
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	var version, schemaHash string
	if err := pool.QueryRow(ctx,
		"SELECT hash_version FROM video_assets WHERE id = $1", assetID).Scan(&version); err != nil {
		t.Fatalf("read migrated asset: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"SELECT schema_hash FROM schema_version WHERE id = 1").Scan(&schemaHash); err != nil {
		t.Fatalf("read migrated stamp: %v", err)
	}
	if version != string(video.LegacyFrameHashVersion) {
		t.Errorf("legacy version = %q, want %q", version, video.LegacyFrameHashVersion)
	}
	if schemaHash != pg.SchemaHash() {
		t.Errorf("schema stamp = %q, want %q", schemaHash, pg.SchemaHash())
	}
}

func TestShareRepo_RebalanceRanks(t *testing.T) {
	ctx, assets, shares, fixtureID, eventID := setupVideoRepos(t)

	// Three verified assets, differing popularity: a1=1, a2=3, a3=2.
	// CompareShares (popularity desc) ⇒ correct order a2, a3, a1.
	mk := func(md5 string, pop int) *video.Asset {
		a := newAsset(eventID, fixtureID, md5, []uint64{1, 2, 3}, 1_000_000)
		a.Popularity = pop
		if _, err := assets.InsertAsset(ctx, a); err != nil {
			t.Fatalf("InsertAsset %s: %v", md5, err)
		}
		return a
	}
	a1 := mk("md5-share-aaaaaa1", 1)
	a2 := mk("md5-share-aaaaaa2", 3)
	a3 := mk("md5-share-aaaaaa3", 2)

	now := time.Date(2026, 8, 3, 12, 10, 0, 0, time.UTC)
	// Insert shares in a deliberately non-final rank order: a1=1, a2=2, a3=3.
	for i, a := range []*video.Asset{a1, a2, a3} {
		s, err := video.NewShare(a.ID, eventID, true, nil, i+1, now)
		if err != nil {
			t.Fatalf("NewShare: %v", err)
		}
		if err := shares.Insert(ctx, s); err != nil {
			t.Fatalf("Insert share: %v", err)
		}
	}

	repositioned, err := shares.RebalanceRanks(ctx, eventID)
	if err != nil {
		t.Fatalf("RebalanceRanks: %v", err)
	}
	if repositioned == 0 {
		t.Error("expected some shares to move (a2 should rise to rank 1)")
	}

	got, err := shares.GetByEvent(ctx, eventID)
	if err != nil {
		t.Fatalf("GetByEvent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("GetByEvent len = %d, want 3", len(got))
	}
	// GetByEvent is rank-ordered → [0]=rank1, [1]=rank2, [2]=rank3.
	wantOrder := []uuid.UUID{a2.ID, a3.ID, a1.ID} // popularity 3,2,1
	for i, s := range got {
		if s.Rank != i+1 {
			t.Errorf("share[%d].Rank = %d, want %d", i, s.Rank, i+1)
		}
		if s.AssetID != wantOrder[i] {
			t.Errorf("rank %d asset = %s, want %s", i+1, s.AssetID, wantOrder[i])
		}
	}
}

// TestAssetRepo_Supersede verifies the atomic superseded_by + popularity-merge
// CTE (#171): the merge happens exactly once even on a retry, the loser leaves
// the live set, and loser==winner is a no-op guard (no self-supersede cycle).
func TestAssetRepo_Supersede(t *testing.T) {
	ctx, assets, _, fixtureID, eventID := setupVideoRepos(t)

	winner := newAsset(eventID, fixtureID, "md5-winnerxxxxx1", []uint64{1, 2, 3}, 2_000_000)
	loser := newAsset(eventID, fixtureID, "md5-loserxxxxxx1", []uint64{4, 5, 6}, 1_000_000)
	if _, err := assets.InsertAsset(ctx, winner); err != nil {
		t.Fatalf("insert winner: %v", err)
	}
	if _, err := assets.InsertAsset(ctx, loser); err != nil {
		t.Fatalf("insert loser: %v", err)
	}
	// Loser popularity → 3 so the merge is distinguishable from a +1 bump.
	_ = assets.AddPopularity(ctx, loser.ID, 2)

	if err := assets.Supersede(ctx, loser.ID, winner.ID); err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	// Retry must be a no-op — the loser is already superseded, so popularity
	// must NOT be merged a second time.
	if err := assets.Supersede(ctx, loser.ID, winner.ID); err != nil {
		t.Fatalf("Supersede retry: %v", err)
	}

	gotLoser, _ := assets.Get(ctx, loser.ID)
	if gotLoser.SupersededBy == nil || *gotLoser.SupersededBy != winner.ID {
		t.Errorf("loser.SupersededBy = %v, want %v", gotLoser.SupersededBy, winner.ID)
	}
	gotWinner, _ := assets.Get(ctx, winner.ID)
	if gotWinner.Popularity != 4 { // 1 + merged 3, exactly once
		t.Errorf("winner.Popularity = %d, want 4 (1 + 3 merged once)", gotWinner.Popularity)
	}
	if gotWinner.SupersededBy != nil {
		t.Errorf("winner must stay live, SupersededBy = %v", gotWinner.SupersededBy)
	}

	// loser==winner is a guarded no-op (never set an asset as its own successor).
	if err := assets.Supersede(ctx, winner.ID, winner.ID); err != nil {
		t.Fatalf("self-supersede: %v", err)
	}
	gotWinner, _ = assets.Get(ctx, winner.ID)
	if gotWinner.SupersededBy != nil || gotWinner.Popularity != 4 {
		t.Errorf("self-supersede mutated winner: superseded_by=%v pop=%d", gotWinner.SupersededBy, gotWinner.Popularity)
	}
}

// TestShareRepo_MarkSuperseded verifies the 'superseded' share state transition
// (schema enum + widened CHECK, #171) and its guard: a 'removed' (VAR) share is
// never clobbered to 'superseded'.
func TestShareRepo_MarkSuperseded(t *testing.T) {
	ctx, assets, shares, fixtureID, eventID := setupVideoRepos(t)

	a := newAsset(eventID, fixtureID, "md5-sharexxxxxx1", []uint64{1, 2, 3}, 1_000_000)
	if _, err := assets.InsertAsset(ctx, a); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	now := time.Date(2026, 8, 3, 12, 15, 0, 0, time.UTC)

	live, err := video.NewShare(a.ID, eventID, true, nil, 1, now)
	if err != nil {
		t.Fatalf("NewShare live: %v", err)
	}
	if err := shares.Insert(ctx, live); err != nil {
		t.Fatalf("Insert live: %v", err)
	}
	if err := shares.MarkSuperseded(ctx, live.ID); err != nil {
		t.Fatalf("MarkSuperseded: %v", err)
	}
	got, err := shares.Get(ctx, live.ID)
	if err != nil {
		t.Fatalf("Get live: %v", err)
	}
	if got.State != video.ShareStateSuperseded {
		t.Errorf("state = %q, want superseded", got.State)
	}

	// A removed share must survive MarkSuperseded unchanged (guard on active).
	rm, _ := video.NewShare(a.ID, eventID, true, nil, 2, now)
	if err := shares.Insert(ctx, rm); err != nil {
		t.Fatalf("Insert rm: %v", err)
	}
	_ = rm.Remove(video.RemovalVAR, now)
	if err := shares.Upsert(ctx, rm); err != nil {
		t.Fatalf("Upsert removed: %v", err)
	}
	if err := shares.MarkSuperseded(ctx, rm.ID); err != nil {
		t.Fatalf("MarkSuperseded on removed: %v", err)
	}
	got, _ = shares.Get(ctx, rm.ID)
	if got.State != video.ShareStateRemoved {
		t.Errorf("removed share clobbered to %q, want removed (guard)", got.State)
	}
}

// TestShareRepo_ReadPath covers the #167 read model: ListLiveForEvent returns
// only active shares of LIVE assets (rank-ordered), and ResolveShare follows
// the superseded_by chain so an old share URL keeps resolving to the current
// best clip (URL stability). A never-minted id is ErrNotFound (→ 404).
func TestShareRepo_ReadPath(t *testing.T) {
	ctx, assets, shares, fixtureID, eventID := setupVideoRepos(t)
	when := time.Date(2026, 8, 3, 12, 20, 0, 0, time.UTC)
	minute := 67

	// Asset A (live) + its active share.
	a := newAsset(eventID, fixtureID, "md5-read-aaaaaa1", []uint64{1, 2, 3}, 1_000_000)
	a.Popularity = 2 // A.S3Key = newAsset's default "9100/asset.mp4"
	if _, err := assets.InsertAsset(ctx, a); err != nil {
		t.Fatalf("insert A: %v", err)
	}
	sA, err := video.NewShare(a.ID, eventID, true, &minute, 1, when)
	if err != nil {
		t.Fatalf("NewShare A: %v", err)
	}
	if err := shares.Insert(ctx, sA); err != nil {
		t.Fatalf("insert share A: %v", err)
	}

	// Live list = [A]; resolve = active → A's key.
	live, err := shares.ListLiveForEvent(ctx, eventID)
	if err != nil {
		t.Fatalf("ListLiveForEvent: %v", err)
	}
	if len(live) != 1 || live[0].ShareID != sA.ID || live[0].Popularity != 2 ||
		live[0].Rank != 1 || !live[0].Verified || live[0].ExtractedMinute == nil || *live[0].ExtractedMinute != 67 {
		t.Fatalf("live = %+v, want one clip (A, rank1, pop2, verified, min67)", live)
	}
	rs, err := shares.ResolveShare(ctx, sA.ID)
	if err != nil {
		t.Fatalf("ResolveShare A: %v", err)
	}
	if rs.State != video.ShareStateActive || rs.Key != a.S3Key {
		t.Errorf("resolve A = %+v, want active + key %q", rs, a.S3Key)
	}

	// Higher-quality B supersedes A; A's share retired.
	b := newAsset(eventID, fixtureID, "md5-read-bbbbbb1", []uint64{4, 5, 6}, 3_000_000)
	b.S3Key = "9100/asset-b.mp4"
	if _, err := assets.InsertAsset(ctx, b); err != nil {
		t.Fatalf("insert B: %v", err)
	}
	sB, err := video.NewShare(b.ID, eventID, true, &minute, 2, when)
	if err != nil {
		t.Fatalf("NewShare B: %v", err)
	}
	if err := shares.Insert(ctx, sB); err != nil {
		t.Fatalf("insert share B: %v", err)
	}
	if err := assets.Supersede(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("Supersede A→B: %v", err)
	}
	if err := shares.MarkSuperseded(ctx, sA.ID); err != nil {
		t.Fatalf("retire share A: %v", err)
	}

	// Live list now = [B] only (A gone from the live set).
	live, _ = shares.ListLiveForEvent(ctx, eventID)
	if len(live) != 1 || live[0].ShareID != sB.ID {
		t.Fatalf("live after supersede = %+v, want [B]", live)
	}
	// URL stability: the OLD share sA still resolves — through the chain to B's bytes.
	rs, err = shares.ResolveShare(ctx, sA.ID)
	if err != nil {
		t.Fatalf("ResolveShare sA after supersede: %v", err)
	}
	if rs.State != video.ShareStateSuperseded || rs.Key != b.S3Key {
		t.Errorf("resolve superseded sA = %+v, want superseded + B key %q", rs, b.S3Key)
	}

	// Never-minted id → ErrNotFound (handler maps to 404).
	if _, err := shares.ResolveShare(ctx, "s_deadbeef0000"); err != video.ErrNotFound {
		t.Errorf("resolve missing = %v, want ErrNotFound", err)
	}
}

// TestShareRepo_RemoveByEvent covers the VAR destroy repo primitives (#172):
// ListObjectKeysByEvent returns every asset object (live + superseded), and
// RemoveByEvent revokes ALL the event's shares → 'removed'/var, after which
// ResolveShare returns 'removed' (the redirect 410s).
func TestShareRepo_RemoveByEvent(t *testing.T) {
	ctx, assets, shares, fixtureID, eventID := setupVideoRepos(t)
	when := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	a1 := newAsset(eventID, fixtureID, "md5-destroy-aaa1", []uint64{1, 2, 3}, 1_000_000)
	a2 := newAsset(eventID, fixtureID, "md5-destroy-bbb1", []uint64{4, 5, 6}, 2_000_000)
	a2.S3Key = "9100/asset-2.mp4"
	if _, err := assets.InsertAsset(ctx, a1); err != nil {
		t.Fatalf("insert a1: %v", err)
	}
	if _, err := assets.InsertAsset(ctx, a2); err != nil {
		t.Fatalf("insert a2: %v", err)
	}
	if err := assets.Supersede(ctx, a2.ID, a1.ID); err != nil { // a2 → superseded
		t.Fatalf("supersede: %v", err)
	}

	s1, _ := video.NewShare(a1.ID, eventID, true, nil, 1, when)
	s2, _ := video.NewShare(a2.ID, eventID, true, nil, 2, when)
	if err := shares.Insert(ctx, s1); err != nil {
		t.Fatalf("insert s1: %v", err)
	}
	if err := shares.Insert(ctx, s2); err != nil {
		t.Fatalf("insert s2: %v", err)
	}
	if err := shares.MarkSuperseded(ctx, s2.ID); err != nil {
		t.Fatalf("mark s2 superseded: %v", err)
	}

	// Both assets' objects are returned (live + superseded).
	keys, err := assets.ListObjectKeysByEvent(ctx, eventID)
	if err != nil {
		t.Fatalf("ListObjectKeysByEvent: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("object keys = %d, want 2 (live + superseded)", len(keys))
	}

	// Revoke everything for the event.
	if err := shares.RemoveByEvent(ctx, eventID, video.RemovalVAR); err != nil {
		t.Fatalf("RemoveByEvent: %v", err)
	}
	for _, id := range []string{s1.ID, s2.ID} {
		got, err := shares.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if got.State != video.ShareStateRemoved || got.RemovedReason == nil || *got.RemovedReason != video.RemovalVAR {
			t.Errorf("share %s = %q/%v, want removed/var", id, got.State, got.RemovedReason)
		}
	}
	// The redirect now 410s.
	rs, err := shares.ResolveShare(ctx, s1.ID)
	if err != nil {
		t.Fatalf("ResolveShare: %v", err)
	}
	if rs.State != video.ShareStateRemoved {
		t.Errorf("resolve after remove = %q, want removed (→ 410)", rs.State)
	}
}
