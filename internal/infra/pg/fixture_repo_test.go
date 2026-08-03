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

// ListActiveIDs --------------------------------------------------

func TestFixtureRepo_ListActiveIDs(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)

	// Seed a mix: staging, active x2, completed. Only the actives
	// should come back.
	staging := makeStaging(6001, kickoff)
	if err := repo.Upsert(ctx, staging); err != nil {
		t.Fatalf("staging Upsert: %v", err)
	}
	activeA := makeStaging(6002, kickoff)
	if err := activeA.Activate(kickoff.Add(-15 * time.Minute)); err != nil {
		t.Fatalf("Activate A: %v", err)
	}
	if err := repo.Upsert(ctx, activeA); err != nil {
		t.Fatalf("active A Upsert: %v", err)
	}
	activeB := makeStaging(6003, kickoff)
	if err := activeB.Activate(kickoff.Add(-15 * time.Minute)); err != nil {
		t.Fatalf("Activate B: %v", err)
	}
	if err := repo.Upsert(ctx, activeB); err != nil {
		t.Fatalf("active B Upsert: %v", err)
	}
	completed := makeStaging(6004, kickoff)
	if err := completed.Activate(kickoff); err != nil {
		t.Fatalf("Activate for completion: %v", err)
	}
	if err := completed.Complete(kickoff.Add(100 * time.Minute)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := repo.Upsert(ctx, completed); err != nil {
		t.Fatalf("completed Upsert: %v", err)
	}

	ids, err := repo.ListActiveIDs(ctx)
	if err != nil {
		t.Fatalf("ListActiveIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d IDs, want 2; ids=%v", len(ids), ids)
	}
	// Ordering: ORDER BY id, so [6002, 6003] deterministically.
	if ids[0] != 6002 || ids[1] != 6003 {
		t.Errorf("ids = %v, want [6002, 6003]", ids)
	}
}

func TestFixtureRepo_ListActiveIDs_Empty(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	ids, err := repo.ListActiveIDs(ctx)
	if err != nil {
		t.Fatalf("ListActiveIDs on empty: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("empty pg returned %v", ids)
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

// FixtureReadyToComplete ------------------------------------------

// TestFixtureRepo_FixtureReadyToComplete_TruthTable exercises each
// leg of the completion query against real Postgres. The mapping:
//   - Terminal status + counter=3 + no events + no in-flight
//     downstream workflows → ready
//   - Non-Terminal status → not ready (regardless of counter)
//   - Terminal + counter=0 + no winner → not ready
//   - Terminal + winner fast-path → ready
//   - Terminal + counter=3 + an event in mid-debounce → not ready
//   - Terminal + counter=3 + an in-flight downstream workflow → not ready
func TestFixtureRepo_FixtureReadyToComplete_TruthTable(t *testing.T) {
	ctx, pool, repo := setupRepo(t)
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)

	// Fresh fixture in staging — not eligible (not Terminal).
	f := makeStaging(9101, base)
	f.APIStatus = fixture.APIStatus{Short: "ns", Long: "Not Started"}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert staging: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || ready {
		t.Errorf("staging fixture ready = %v (err=%v), want false", ready, err)
	}

	// Move to active, set counter to 3 + Terminal status. No events, no downstream.
	if err := f.Activate(base); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// Simulate 3 Terminal polls to prime the counter.
	for i := 0; i < 3; i++ {
		f.UpdateFromPoll(
			fixture.APIStatus{Short: "ft", Long: "Match Finished"},
			nil, nil, nil, nil, base.Add(time.Duration(i)*30*time.Second),
		)
	}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert active-terminal: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("terminal+counter=3+no-events ready = %v (err=%v), want true", ready, err)
	}

	// Winner fast-path: reset counter, set winner. Should still be ready.
	f.CompletionCounter = 0
	trueBool := true
	f.HomeWinner = &trueBool
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert winner: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("terminal+winner+counter=0 ready = %v (err=%v), want true", ready, err)
	}

	// Add an event in mid-debounce (removed=false, downstream_triggered=false).
	// Directly INSERT bypassing repo since we need to control debounce_count.
	eventID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, team_id, team_name,
			player_id, player_name, detail, minute,
			debounce_count, downstream_triggered, removed, first_seen_at
		) VALUES (
			$1, $2, '40_111_goal_1', 'goal', 40, 'Liverpool',
			111, 'M.Salah', 'normal goal', 42,
			2, false, false, $3
		)
	`, eventID, 9101, base.Add(42*time.Minute))
	if err != nil {
		t.Fatalf("insert mid-debounce event: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || ready {
		t.Errorf("terminal+event-mid-debounce ready = %v (err=%v), want false", ready, err)
	}

	// Settle the event (downstream_triggered = true).
	_, err = pool.Exec(ctx, `
		UPDATE events SET debounce_count = 3, downstream_triggered = true
		WHERE id = $1
	`, eventID)
	if err != nil {
		t.Fatalf("settle event: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("terminal+event-settled+no-downstream ready = %v (err=%v), want true", ready, err)
	}

	// Register an in-flight downstream workflow — completion should
	// block until the row's completed_at fills in.
	_, err = pool.Exec(ctx, `
		INSERT INTO event_downstream_workflows (event_id, workflow_type, workflow_id)
		VALUES ($1, 'discovery', 'discovery-9101-1')
	`, eventID)
	if err != nil {
		t.Fatalf("register downstream workflow: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || ready {
		t.Errorf("terminal+downstream-in-flight ready = %v (err=%v), want false", ready, err)
	}

	// Mark the downstream workflow completed.
	_, err = pool.Exec(ctx, `
		UPDATE event_downstream_workflows SET completed_at = NOW(), outcome_class = 'success'
		WHERE event_id = $1 AND workflow_id = 'discovery-9101-1'
	`, eventID)
	if err != nil {
		t.Fatalf("complete downstream workflow: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("terminal+downstream-completed ready = %v (err=%v), want true", ready, err)
	}
}

// TestFixtureRepo_FixtureReadyToComplete_NotFound
func TestFixtureRepo_FixtureReadyToComplete_NotFound(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	_, err := repo.FixtureReadyToComplete(ctx, 999_999)
	if !errors.Is(err, fixture.ErrNotFound) {
		t.Errorf("Ready for non-existent fixture returned %v, want ErrNotFound", err)
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
