// AssetRepo durable object-reclamation integration tests.
package pg_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// seedGoalWithAsset inserts an event and asset for a fixture and returns both
// durable IDs. Shares are intentionally absent: storage lifecycle must not
// depend on public URL state.
func seedGoalWithAsset(t *testing.T, ctx context.Context, pool *pg.Pool, fixtureID int64, suffix string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	eventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, minute)
		VALUES ($1, $2, $3, 'goal', 'Normal Goal', 40, 'Liverpool', 23)
	`, eventID, fixtureID, fmt.Sprintf("40_%d_Goal_%s", fixtureID, suffix)); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	assetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO video_assets (id, event_id, fixture_id, s3_bucket, s3_key,
			md5, frame_hashes, width, height, duration_ms, file_size_bytes)
		VALUES ($1, $2, $3, 'test', $4, $5, $6, 1920, 1080, 45000, 15000000)
	`, assetID, eventID, fixtureID, fmt.Sprintf("test/%s.mp4", suffix),
		uuidMD5(assetID), []byte{0xab, 0xcd, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	return eventID, assetID
}

func uuidMD5(id uuid.UUID) []byte {
	out := make([]byte, 16)
	copy(out, id[:])
	return out
}

func TestAssetRepo_DurableRetentionWorklist(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	assets := pg.NewAssetRepo(pool)
	oldCompleted := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	recentCompleted := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	old := completedFixture(t, ctx, fixtures, 8001, oldCompleted)
	wantEvent, wantAsset := seedGoalWithAsset(t, ctx, pool, old.ID, "old")
	recent := completedFixture(t, ctx, fixtures, 8002, recentCompleted)
	seedGoalWithAsset(t, ctx, pool, recent.ID, "recent")

	ids, err := assets.ListUnreclaimedEventIDsBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListUnreclaimedEventIDsBefore: %v", err)
	}
	if len(ids) != 1 || ids[0] != wantEvent {
		t.Fatalf("retention worklist = %v, want [%s]", ids, wantEvent)
	}

	objects, err := assets.ListUnreclaimedObjectsByEvent(ctx, wantEvent)
	if err != nil {
		t.Fatalf("ListUnreclaimedObjectsByEvent: %v", err)
	}
	if len(objects) != 1 || objects[0].AssetID != wantAsset || objects[0].Key != "test/old.mp4" {
		t.Fatalf("unreclaimed objects = %+v", objects)
	}

	if err := assets.MarkObjectReclaimed(ctx, wantAsset); err != nil {
		t.Fatalf("MarkObjectReclaimed: %v", err)
	}
	firstMark, err := assets.Get(ctx, wantAsset)
	if err != nil || firstMark.ObjectReclaimedAt == nil {
		t.Fatalf("Get first reclaim marker: %+v, %v", firstMark, err)
	}
	markedAt := *firstMark.ObjectReclaimedAt
	// A retry preserves the first successful time.
	if err := assets.MarkObjectReclaimed(ctx, wantAsset); err != nil {
		t.Fatalf("MarkObjectReclaimed retry: %v", err)
	}
	stored, err := assets.Get(ctx, wantAsset)
	if err != nil {
		t.Fatalf("Get reclaimed asset: %v", err)
	}
	if stored.ObjectReclaimedAt == nil || !stored.ObjectReclaimedAt.Equal(markedAt) {
		t.Fatalf("object_reclaimed_at = %v, want %v", stored.ObjectReclaimedAt, markedAt)
	}

	ids, err = assets.ListUnreclaimedEventIDsBefore(ctx, cutoff)
	if err != nil || len(ids) != 0 {
		t.Fatalf("post-reclaim worklist = %v, %v; want empty", ids, err)
	}
	objects, err = assets.ListUnreclaimedObjectsByEvent(ctx, wantEvent)
	if err != nil || len(objects) != 0 {
		t.Fatalf("post-reclaim objects = %+v, %v; want empty", objects, err)
	}

	// Reclamation keeps the complete SQL audit chain.
	if _, err := fixtures.Get(ctx, old.ID); err != nil {
		t.Fatalf("old fixture was deleted: %v", err)
	}
	var eventCount, assetCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id = $1`, wantEvent).Scan(&eventCount); err != nil {
		t.Fatalf("count event: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM video_assets WHERE id = $1`, wantAsset).Scan(&assetCount); err != nil {
		t.Fatalf("count asset: %v", err)
	}
	if eventCount != 1 || assetCount != 1 {
		t.Fatalf("durable rows event=%d asset=%d, want 1/1", eventCount, assetCount)
	}
}
