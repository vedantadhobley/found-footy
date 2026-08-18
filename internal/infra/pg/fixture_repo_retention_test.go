// FixtureRepo completed-fixture retention integration tests.
package pg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// completedFixture inserts a fixture through the real staging, active, and
// completed domain transitions.
func completedFixture(t *testing.T, ctx context.Context, repo *pg.FixtureRepo, id int64, completedAt time.Time) *fixture.Fixture {
	t.Helper()
	kickoff := completedAt.Add(-2 * time.Hour)
	f := makeStaging(id, kickoff)
	if err := f.Activate(kickoff); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := f.Complete(completedAt); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("Upsert completed: %v", err)
	}
	return f
}

func TestFixtureRepo_PruneCompleted_NoShares_Deletes(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	oldCompleted := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	recentCompleted := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	completedFixture(t, ctx, repo, 6001, oldCompleted)
	completedFixture(t, ctx, repo, 6002, recentCompleted)

	// Prune with threshold = 2026-07-01 → only 6001 (June) qualifies.
	threshold := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	n, err := repo.PruneCompleted(ctx, threshold)
	if err != nil {
		t.Fatalf("PruneCompleted: %v", err)
	}
	if n != 1 {
		t.Errorf("PruneCompleted deleted %d, want 1", n)
	}
	if _, err := repo.Get(ctx, 6001); !errors.Is(err, fixture.ErrNotFound) {
		t.Errorf("6001 should be gone, got err=%v", err)
	}
	if _, err := repo.Get(ctx, 6002); err != nil {
		t.Errorf("6002 should still exist, got err=%v", err)
	}
}

// The load-bearing URL-stability guarantee: even a completed +
// past-threshold fixture must NOT prune if any of its events has a
// video_share. This test inserts one event + one video_asset + one
// video_share to trigger the NOT EXISTS guard.
func TestFixtureRepo_PruneCompleted_WithShares_Retains(t *testing.T) {
	ctx, pool, repo := setupRepo(t)
	oldCompleted := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	f := completedFixture(t, ctx, repo, 7001, oldCompleted)

	// Insert one event + one asset + one active share for this fixture.
	// Uses raw SQL because we haven't built the event / video repos yet;
	// once they exist, this setup migrates to their domain constructors.
	eventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, minute)
		VALUES ($1, $2, '40_234_Goal_1', 'goal', 'Normal Goal', 40, 'Liverpool', 23)
	`, eventID, f.ID); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	assetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO video_assets (id, event_id, fixture_id, s3_bucket, s3_key,
			md5, frame_hashes,
			width, height, duration_ms, file_size_bytes)
		VALUES ($1, $2, $3, 'test', $4, $5, $6, 1920, 1080, 45000, 15000000)
	`, assetID, eventID, f.ID, "test/asset.mp4", []byte("md5md5md5md5md5m"),
		[]byte{0xab, 0xcd, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO video_shares (id, asset_id, event_id, timestamp_verified, rank)
		VALUES ('s_share000001', $1, $2, true, 1)
	`, assetID, eventID); err != nil {
		t.Fatalf("insert share: %v", err)
	}

	// Prune — old completed + no share = would delete, but share exists.
	threshold := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	n, err := repo.PruneCompleted(ctx, threshold)
	if err != nil {
		t.Fatalf("PruneCompleted: %v", err)
	}
	if n != 0 {
		t.Errorf("PruneCompleted deleted %d, want 0 (share should retain fixture)", n)
	}
	if _, err := repo.Get(ctx, 7001); err != nil {
		t.Errorf("fixture with active share should still exist, got err=%v", err)
	}
}
