// Tests for the FixtureRepo — real Postgres via testcontainers with
// the app schema loaded (same runTestPostgres helper as pool_test.go).
package pg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// helpers ---------------------------------------------------------

// setupRepo spins up a fresh Postgres via runTestPostgres (shared
// helper from pool_test.go), builds a Pool + FixtureRepo, and hands
// them back. Also returns a ctx bounded by the test's 2-min budget.
func setupRepo(t *testing.T) (context.Context, *pg.Pool, *pg.FixtureRepo) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	connStr := runTestPostgres(ctx, t)
	fx := newTestFixture()
	pool, err := pg.New(ctx, config.PGConfig{
		DSN:            connStr,
		MaxConns:       5,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	}, fx.ins)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool, pg.NewFixtureRepo(pool)
}

// makeStaging returns a fresh staging fixture — helper mirroring the
// one in the domain package's tests so the repo test cases read the
// same way.
func makeStaging(id int64, kickoff time.Time) *fixture.Fixture {
	return fixture.New(
		id,
		fixture.APIStatus{Short: "NS", Long: "Not Started"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Name: "Premier League", Season: 2026},
	)
}

// Get ------------------------------------------------------------

func TestFixtureRepo_Get_NotFound(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	_, err := repo.Get(ctx, 999_999)
	if !errors.Is(err, fixture.ErrNotFound) {
		t.Errorf("Get non-existent returned %v, want fixture.ErrNotFound", err)
	}
}

func TestFixtureRepo_UpsertThenGet(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	original := makeStaging(1001, time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC))

	if err := repo.Upsert(ctx, original); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := repo.Get(ctx, 1001)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != 1001 || got.State != fixture.StateStaging {
		t.Errorf("Get returned wrong fixture: %+v", got)
	}
	if got.APIStatus.Short != "NS" || got.APIStatus.Long != "Not Started" {
		t.Errorf("APIStatus roundtrip = %+v", got.APIStatus)
	}
	if got.Home.ID != 40 || got.Home.Name != "Liverpool" {
		t.Errorf("Home roundtrip = %+v", got.Home)
	}
	if got.League.Season != 2026 {
		t.Errorf("League.Season = %d, want 2026", got.League.Season)
	}
	if !got.Kickoff.Equal(original.Kickoff) {
		t.Errorf("Kickoff = %v, want %v", got.Kickoff, original.Kickoff)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not populated: CreatedAt=%v UpdatedAt=%v", got.CreatedAt, got.UpdatedAt)
	}
}

// Upsert on existing row updates fields but preserves created_at (via
// the ON CONFLICT DO UPDATE not listing created_at + updated_at,
// which the trg_fixtures_updated_at trigger maintains automatically).
func TestFixtureRepo_Upsert_UpdatesExisting_PreservesCreatedAt(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	f := makeStaging(2001, kickoff)
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	first, err := repo.Get(ctx, 2001)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	origCreated := first.CreatedAt

	// Transition to active, upsert again.
	if err := first.Activate(kickoff.Add(-15 * time.Minute)); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := repo.Upsert(ctx, first); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	after, err := repo.Get(ctx, 2001)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if after.State != fixture.StateActive {
		t.Errorf("State = %q, want active", after.State)
	}
	if after.ActivatedAt == nil {
		t.Fatal("ActivatedAt not persisted")
	}
	if !after.CreatedAt.Equal(origCreated) {
		t.Errorf("CreatedAt changed on Upsert-update: was %v, now %v", origCreated, after.CreatedAt)
	}
	if !after.UpdatedAt.After(origCreated) {
		t.Errorf("UpdatedAt should have advanced past CreatedAt: got %v vs %v", after.UpdatedAt, origCreated)
	}
}

// The domain's ValidateInvariants runs at Upsert time; a state ↔
// timestamp mismatch should be caught before the SQL executes.
func TestFixtureRepo_Upsert_RejectsInvariantViolation(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	f := makeStaging(3001, time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC))
	// Bypass domain methods — set State=active but leave ActivatedAt nil.
	f.State = fixture.StateActive
	err := repo.Upsert(ctx, f)
	if err == nil {
		t.Fatal("expected invariant-mismatch error, got nil")
	}
	if !errors.Is(err, fixture.ErrStateTimestampMismatch) {
		t.Errorf("Upsert returned %v, want ErrStateTimestampMismatch chain", err)
	}
}

// ListByState -----------------------------------------------------

func TestFixtureRepo_ListByState(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)

	staging := makeStaging(4001, kickoff)
	if err := repo.Upsert(ctx, staging); err != nil {
		t.Fatalf("staging Upsert: %v", err)
	}
	// Second fixture in active state.
	active := makeStaging(4002, kickoff)
	if err := active.Activate(kickoff.Add(-15 * time.Minute)); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := repo.Upsert(ctx, active); err != nil {
		t.Fatalf("active Upsert: %v", err)
	}

	stagingList, err := repo.ListByState(ctx, fixture.StateStaging)
	if err != nil {
		t.Fatalf("ListByState staging: %v", err)
	}
	if len(stagingList) != 1 || stagingList[0].ID != 4001 {
		t.Errorf("staging list = %+v, want single fixture 4001", stagingList)
	}
	activeList, err := repo.ListByState(ctx, fixture.StateActive)
	if err != nil {
		t.Fatalf("ListByState active: %v", err)
	}
	if len(activeList) != 1 || activeList[0].ID != 4002 {
		t.Errorf("active list = %+v, want single fixture 4002", activeList)
	}
	completedList, err := repo.ListByState(ctx, fixture.StateCompleted)
	if err != nil {
		t.Fatalf("ListByState completed: %v", err)
	}
	if len(completedList) != 0 {
		t.Errorf("completed list should be empty, got %+v", completedList)
	}
}

// ListStagingBeforeKickoff ---------------------------------------

func TestFixtureRepo_ListStagingBeforeKickoff(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)

	// Three staging fixtures at different kickoff times.
	near := makeStaging(5001, base.Add(10*time.Minute))   // 10 min from base
	within := makeStaging(5002, base.Add(25*time.Minute)) // 25 min from base
	far := makeStaging(5003, base.Add(2*time.Hour))       // 2 hours from base
	for _, f := range []*fixture.Fixture{near, within, far} {
		if err := repo.Upsert(ctx, f); err != nil {
			t.Fatalf("Upsert %d: %v", f.ID, err)
		}
	}

	threshold := base.Add(30 * time.Minute)
	got, err := repo.ListStagingBeforeKickoff(ctx, threshold)
	if err != nil {
		t.Fatalf("ListStagingBeforeKickoff: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("returned %d fixtures, want 2", len(got))
	}
	// Ordered by kickoff ascending.
	if got[0].ID != 5001 || got[1].ID != 5002 {
		t.Errorf("ordering wrong: got IDs %d, %d; want 5001, 5002", got[0].ID, got[1].ID)
	}
}

// PruneCompleted --------------------------------------------------

// completedFixture inserts a fixture that traveled staging → active →
// completed, all via the domain state transitions. Timestamps land
// consistent with the schema invariants.
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
		VALUES ($1, $2, '40_234_Goal_1', 'Goal', 'Normal Goal', 40, 'Liverpool', 23)
	`, eventID, f.ID); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	assetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO video_assets (id, fixture_id, s3_bucket, s3_key,
			perceptual_hash, perceptual_hash_prefix, md5,
			width, height, duration_ms, file_size_bytes)
		VALUES ($1, $2, 'test', $3, $4, $5, $6, 1920, 1080, 45000, 15000000)
	`, assetID, f.ID, "test/asset.mp4", []byte{0xab, 0xcd, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		0xabcd, []byte("md5md5md5md5md5m")); err != nil {
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
