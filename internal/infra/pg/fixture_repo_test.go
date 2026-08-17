// Tests for the FixtureRepo — real Postgres via testcontainers with
// the app schema loaded (same runTestPostgres helper as pool_test.go).
package pg_test

import (
	"context"
	"errors"
	"fmt"
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
	// Vendor display + shootout fields (round-2 DTO gaps): must roundtrip.
	original.League.Country = "World"
	original.League.Round = "Group Stage"
	ph, pa, wh := 5, 6, false
	original.HomePenalty, original.AwayPenalty = &ph, &pa
	original.HomeWinner = &wh

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
	if got.League.Country != "World" || got.League.Round != "Group Stage" {
		t.Errorf("League country/round roundtrip = %q/%q", got.League.Country, got.League.Round)
	}
	if got.HomePenalty == nil || *got.HomePenalty != 5 || got.AwayPenalty == nil || *got.AwayPenalty != 6 {
		t.Errorf("penalty roundtrip = %v/%v, want 5/6", got.HomePenalty, got.AwayPenalty)
	}
	if got.HomeWinner == nil || *got.HomeWinner != false {
		t.Errorf("HomeWinner roundtrip = %v, want false", got.HomeWinner)
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
//   - Terminal + counter=0 → not ready, even with winner data
//   - Terminal + counter=3 + an event in mid-debounce → not ready
//   - Terminal + counter=3 + an in-flight downstream workflow → not ready
//   - Terminal + an unknown-scorer placeholder (debounce_count=0) → ready
//     (the placeholder never triggers downstream, so it must not block —
//     G1 / audit-2026-08-05 Tier-1 #2)
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
	zero := 0
	f.HomeScore, f.AwayScore = &zero, &zero
	// Simulate 3 Terminal polls to prime the counter.
	for i := 0; i < 3; i++ {
		f.UpdateFromPoll(
			fixture.APIStatus{Short: "ft", Long: "Match Finished"},
			nil, nil, nil, nil, true, base.Add(time.Duration(i)*30*time.Second),
		)
	}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert active-terminal: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("terminal+counter=3+no-events ready = %v (err=%v), want true", ready, err)
	}

	// Winner data cannot bypass the coherent three-poll counter.
	f.CompletionCounter = 0
	trueBool := true
	f.HomeWinner = &trueBool
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert winner: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || ready {
		t.Errorf("terminal+winner+counter=0 ready = %v (err=%v), want false", ready, err)
	}
	f.CompletionCounter = 3

	// Add an event in mid-debounce (removed=false, downstream_triggered=false).
	// Directly INSERT bypassing repo since we need to control debounce_count.
	one := 1
	f.HomeScore = &one
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert 1-0 score: %v", err)
	}
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

	// Unknown-scorer placeholder that survived to full-time: debounce_count=0,
	// removed=false, downstream_triggered=false, no player attributed. It never
	// triggers downstream, so pre-G1 it matched the event-settled NOT EXISTS
	// clause and blocked completion forever. It still counts as a scoring event
	// for score parity, so make it the second goal in a 2-0 result. It must NOT
	// block once inventory and score agree. (G1 / audit-2026-08-05 Tier-1 #2)
	two := 2
	f.HomeScore = &two
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert 2-0 score: %v", err)
	}
	placeholderID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, team_id, team_name,
			player_id, player_name, detail, minute,
			debounce_count, downstream_triggered, removed, first_seen_at
		) VALUES (
			$1, $2, '40_0_goal_1', 'goal', 40, 'Liverpool',
			NULL, NULL, 'normal goal', 88,
			0, false, false, $3
		)
	`, placeholderID, 9101, base.Add(88*time.Minute))
	if err != nil {
		t.Fatalf("insert unknown-scorer placeholder: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("terminal+unknown-placeholder ready = %v (err=%v), want true", ready, err)
	}

	// Played terminal result with more goals in the score than in surviving
	// storage must remain active. Winner data cannot bypass this parity gate.
	three := 3
	f.HomeScore = &three
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert mismatched 3-0 score: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || ready {
		t.Errorf("terminal+score/event mismatch ready = %v (err=%v), want false", ready, err)
	}

	// Exceptional terminal statuses do not promise a played-match event/score
	// inventory (walkovers and abandoned fixtures are common), so they bypass
	// score parity while retaining every other completion predicate.
	f.APIStatus = fixture.APIStatus{Short: "canc", Long: "Match Cancelled"}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert cancelled status: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("cancelled+score/event mismatch ready = %v (err=%v), want true", ready, err)
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

// SearchFixtures ---------------------------------------------------

// SearchFixtures matches the free-text query across competition (league) name,
// either team name, and event scorer/assist names — case-insensitively, over
// any state. Exercises each of the four arms + case-insensitivity + no-match.
func TestFixtureRepo_SearchFixtures(t *testing.T) {
	ctx, pool, repo := setupRepo(t)

	// Fixture A: La Liga · Barcelona vs Real Madrid · goal (Lewandowski, assist Yamal).
	fa := makeStaging(8001, time.Date(2026, 8, 1, 19, 0, 0, 0, time.UTC))
	fa.Home = fixture.Team{ID: 529, Name: "Barcelona"}
	fa.Away = fixture.Team{ID: 541, Name: "Real Madrid"}
	fa.League = fixture.League{ID: 140, Name: "La Liga", Season: 2026}
	if err := fa.Activate(time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("activate A: %v", err)
	}
	if err := repo.Upsert(ctx, fa); err != nil {
		t.Fatalf("upsert A: %v", err)
	}
	insertSearchEvent(t, ctx, pool, 8001, "529_1_goal_1", "R. Lewandowski", "L. Yamal")

	// Fixture B: Serie A · Inter vs AC Milan · goal (Martinez, no assist).
	fb := makeStaging(8002, time.Date(2026, 8, 2, 19, 0, 0, 0, time.UTC))
	fb.Home = fixture.Team{ID: 505, Name: "Inter"}
	fb.Away = fixture.Team{ID: 489, Name: "AC Milan"}
	fb.League = fixture.League{ID: 135, Name: "Serie A", Season: 2026}
	if err := fb.Activate(time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("activate B: %v", err)
	}
	if err := repo.Upsert(ctx, fb); err != nil {
		t.Fatalf("upsert B: %v", err)
	}
	insertSearchEvent(t, ctx, pool, 8002, "505_2_goal_1", "L. Martinez", "")

	check := func(q string, wantID int64) {
		t.Helper()
		got, err := repo.SearchFixtures(ctx, q, 100)
		if err != nil {
			t.Fatalf("SearchFixtures(%q): %v", q, err)
		}
		if wantID == 0 {
			if len(got) != 0 {
				t.Errorf("Search(%q) = %v, want none", q, idsOf(got))
			}
			return
		}
		if len(got) != 1 || got[0].ID != wantID {
			t.Errorf("Search(%q) = %v, want [%d]", q, idsOf(got), wantID)
		}
	}

	check("la liga", 8001)     // competition
	check("serie a", 8002)     // competition
	check("barcelona", 8001)   // home team
	check("milan", 8002)       // away team
	check("lewandowski", 8001) // scorer
	check("yamal", 8001)       // assist
	check("MARTINEZ", 8002)    // scorer, case-insensitive
	check("zzznomatch", 0)     // none
}

// insertSearchEvent seeds one non-removed goal event (raw SQL — the search test
// needs only the searchable name columns) with player_name + assist_name;
// assistName "" → NULL.
func insertSearchEvent(t *testing.T, ctx context.Context, pool *pg.Pool, fixtureID int64, naturalKey, playerName, assistName string) {
	t.Helper()
	var assist *string
	if assistName != "" {
		assist = &assistName
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, player_name, assist_name, minute)
		VALUES ($1, $2, $3, 'goal', 'normal goal', 40, 'Team', $4, $5, 30)
	`, uuid.New(), fixtureID, naturalKey, playerName, assist); err != nil {
		t.Fatalf("insert search event: %v", err)
	}
}

// idsOf projects fixture IDs for assertion messages.
func idsOf(fx []*fixture.Fixture) []int64 {
	out := make([]int64, len(fx))
	for i, f := range fx {
		out[i] = f.ID
	}
	return out
}

// #181: a candidate carries a per-candidate terminal outcome — defaults to
// 'pending', the consumer's UPDATE (the exact SQL RecordCandidateOutcome runs)
// stamps class/reason/detail/at, and the CHECK rejects an unknown class.
func TestEventSearchCandidate_Outcome(t *testing.T) {
	ctx, pool, repo := setupRepo(t)

	f := makeStaging(8101, time.Date(2026, 8, 1, 19, 0, 0, 0, time.UTC))
	if err := f.Activate(time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	evID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail, team_id, team_name, minute)
		VALUES ($1, $2, '40_1_goal_1', 'goal', 'normal goal', 40, 'T', 30)`, evID, f.ID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_search_candidates (event_id, fixture_id, search_attempt, query, tweet_url, video_page_url)
		VALUES ($1, $2, 1, 'q', 'https://x.com/a/1', 'https://x.com/i/1')`, evID, f.ID); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}

	// defaults to pending
	var class string
	if err := pool.QueryRow(ctx, `SELECT outcome_class FROM event_search_candidates WHERE event_id=$1`, evID).Scan(&class); err != nil {
		t.Fatalf("select default: %v", err)
	}
	if class != "pending" {
		t.Errorf("default outcome_class = %q, want pending", class)
	}

	// the consumer's terminal UPDATE stamps class + reason + detail + at
	if _, err := pool.Exec(ctx, `
		UPDATE event_search_candidates
		   SET outcome_class = 'rejected', reject_reason = 'geo_restricted',
		       outcome_detail = '{"soccer_votes":1}'::jsonb, outcome_at = NOW()
		 WHERE event_id = $1 AND tweet_url = 'https://x.com/a/1'`, evID); err != nil {
		t.Fatalf("outcome update: %v", err)
	}
	var (
		reason *string
		at     *time.Time
		detail []byte
	)
	if err := pool.QueryRow(ctx, `
		SELECT outcome_class, reject_reason, outcome_detail, outcome_at
		  FROM event_search_candidates WHERE event_id=$1`, evID).Scan(&class, &reason, &detail, &at); err != nil {
		t.Fatalf("select after update: %v", err)
	}
	if class != "rejected" || reason == nil || *reason != "geo_restricted" || at == nil || len(detail) == 0 {
		t.Errorf("outcome roundtrip: class=%q reason=%v at=%v detail=%s", class, reason, at, detail)
	}

	// the CHECK constraint rejects an unregistered class
	if _, err := pool.Exec(ctx, `UPDATE event_search_candidates SET outcome_class='bogus' WHERE event_id=$1`, evID); err == nil {
		t.Error("CHECK should reject an unknown outcome_class")
	}
}

// ListReclaimableEventIDs -----------------------------------------

// seedGoalWithShare inserts one event + asset + share for fixtureID and
// returns the event ID. shareState is "active" (live → reclaimable) or
// "removed" (410 tombstone, already reclaimed → excluded). Mirrors the
// raw-SQL seeding in TestFixtureRepo_PruneCompleted_WithShares_Retains;
// md5 is derived from shareID to satisfy UNIQUE(event_id, md5).
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
			INSERT INTO video_shares (id, asset_id, event_id, timestamp_verified, rank, state, removed_reason)
			VALUES ($1, $2, $3, true, 1, 'removed', 'policy')
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
