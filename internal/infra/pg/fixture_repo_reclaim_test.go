// FixtureRepo clip-reclamation projection integration tests.
package pg_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// seedGoalWithShare inserts an event, asset, and active or removed share for a
// fixture and returns the event ID.
func seedGoalWithShare(t *testing.T, ctx context.Context, pool *pg.Pool, fixtureID int64, shareID, shareState string) uuid.UUID {
	t.Helper()
	eventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, minute)
		VALUES ($1, $2, $3, 'goal', 'Normal Goal', 40, 'Liverpool', 23)
	`, eventID, fixtureID, fmt.Sprintf("40_%d_Goal_1", fixtureID)); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	assetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO video_assets (id, event_id, fixture_id, s3_bucket, s3_key,
			md5, frame_hashes, width, height, duration_ms, file_size_bytes)
		VALUES ($1, $2, $3, 'test', $4, $5, $6, 1920, 1080, 45000, 15000000)
	`, assetID, eventID, fixtureID, fmt.Sprintf("test/%s.mp4", shareID),
		[]byte(fmt.Sprintf("%-16.16s", shareID)),
		[]byte{0xab, 0xcd, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	if shareState == "removed" {
		if _, err := pool.Exec(ctx, `
			INSERT INTO video_shares (id, asset_id, event_id, timestamp_verified, rank, state, removed_reason, removed_at)
			VALUES ($1, $2, $3, true, 1, 'removed', 'policy', now())
		`, shareID, assetID, eventID); err != nil {
			t.Fatalf("insert removed share: %v", err)
		}
		return eventID
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO video_shares (id, asset_id, event_id, timestamp_verified, rank)
		VALUES ($1, $2, $3, true, 1)
	`, shareID, assetID, eventID); err != nil {
		t.Fatalf("insert active share: %v", err)
	}
	return eventID
}

// TestFixtureRepo_ListReclaimableEventIDs covers the byte-reclaim
// worklist for retention's clip-bearing half (#176 option B): it must
// return events of AGED completed fixtures that still have a LIVE share,
// and exclude recent fixtures, already-'removed' shares (idempotency —
// already reclaimed), and clipless fixtures.
func TestFixtureRepo_ListReclaimableEventIDs(t *testing.T) {
	ctx, pool, repo := setupRepo(t)
	oldCompleted := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	recentCompleted := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	// aged + live share → SHOULD be reclaimable.
	agedF := completedFixture(t, ctx, repo, 8001, oldCompleted)
	wantEvent := seedGoalWithShare(t, ctx, pool, agedF.ID, "s_aged00000001", "active")

	// recent + live share → excluded (not past threshold).
	recentF := completedFixture(t, ctx, repo, 8002, recentCompleted)
	seedGoalWithShare(t, ctx, pool, recentF.ID, "s_recent000001", "active")

	// aged + already-removed share → excluded (idempotency: reclaimed).
	agedRemoved := completedFixture(t, ctx, repo, 8003, oldCompleted)
	seedGoalWithShare(t, ctx, pool, agedRemoved.ID, "s_agedremoved1", "removed")

	// aged + clipless → excluded (never minted a share).
	completedFixture(t, ctx, repo, 8004, oldCompleted)

	threshold := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	got, err := repo.ListReclaimableEventIDs(ctx, threshold)
	if err != nil {
		t.Fatalf("ListReclaimableEventIDs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d reclaimable events, want 1 (%v)", len(got), got)
	}
	if got[0] != wantEvent {
		t.Errorf("reclaimable event = %s, want %s (aged live-share event)", got[0], wantEvent)
	}
}
