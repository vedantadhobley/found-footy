// Tests for the EventRepo — testcontainer Postgres with the app
// schema loaded (same runTestPostgres helper as pool_test.go +
// fixture_repo_test.go).
//
// Fix 3a scope: basic CRUD only (Get, GetByNaturalKey, Insert,
// Upsert, ListPending). Debounce methods land in 3b tests.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// setupEventRepo mirrors setupRepo — spins up a fresh Postgres via
// runTestPostgres, returns the pool + EventRepo + FixtureRepo (events
// require a parent fixture row per the FK).
func setupEventRepo(t *testing.T) (context.Context, *pg.Pool, *pg.EventRepo, *pg.FixtureRepo) {
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
	return ctx, pool, pg.NewEventRepo(pool), pg.NewFixtureRepo(pool)
}

// seedFixture inserts a parent fixture row so events have a valid FK.
func seedFixture(t *testing.T, ctx context.Context, repo *pg.FixtureRepo, id int64) {
	t.Helper()
	f := makeStaging(id, time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC))
	// Events are typically detected during active play — activate the
	// fixture so it's in the state where events actually arrive.
	if err := f.Activate(time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed Activate: %v", err)
	}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}
}

// makeGoalEvent — helper for a standard goal event on a given fixture.
func makeGoalEvent(fixtureID int64, seq int) *event.Event {
	playerID := 999
	playerName := "Test Scorer"
	at := time.Date(2026, 7, 8, 15, 30, 0, 0, time.UTC)
	return event.New(
		fixtureID,
		event.Team{ID: 40, Name: "Liverpool"},
		event.Player{ID: &playerID, Name: &playerName},
		event.TypeGoal,
		"Normal Goal",
		30,
		nil, // no extra time
		seq,
		at,
	)
}

// Get ------------------------------------------------------------

func TestEventRepo_Get_NotFound(t *testing.T) {
	ctx, _, repo, _ := setupEventRepo(t)
	_, err := repo.Get(ctx, uuid.New())
	if !errors.Is(err, event.ErrNotFound) {
		t.Errorf("Get(new UUID) = %v, want event.ErrNotFound", err)
	}
}

func TestEventRepo_InsertThenGet(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7001)

	e := makeGoalEvent(7001, 1)
	if err := repo.Insert(ctx, e); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Roundtrip verification of every scanEvent field mapping.
	if got.FixtureID != 7001 {
		t.Errorf("FixtureID = %d, want 7001", got.FixtureID)
	}
	if got.NaturalKey != e.NaturalKey {
		t.Errorf("NaturalKey = %q, want %q", got.NaturalKey, e.NaturalKey)
	}
	if got.Type != event.TypeGoal {
		t.Errorf("Type = %q, want Goal", got.Type)
	}
	if got.Detail != "Normal Goal" {
		t.Errorf("Detail = %q, want Normal Goal", got.Detail)
	}
	if got.Team.ID != 40 || got.Team.Name != "Liverpool" {
		t.Errorf("Team = %+v", got.Team)
	}
	if got.Player.ID == nil || *got.Player.ID != 999 {
		t.Errorf("Player.ID = %v, want 999", got.Player.ID)
	}
	if got.Player.Name == nil || *got.Player.Name != "Test Scorer" {
		t.Errorf("Player.Name = %v", got.Player.Name)
	}
	if got.Minute != 30 {
		t.Errorf("Minute = %d, want 30", got.Minute)
	}
	if got.Extra != nil {
		t.Errorf("Extra = %v, want nil", got.Extra)
	}
	if got.MonitorComplete || got.DownloadComplete || got.Removed {
		t.Errorf("state flags should be false on fresh insert: %+v", got)
	}
}

func TestEventRepo_Insert_DuplicateNaturalKeyFails(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7002)

	first := makeGoalEvent(7002, 1)
	if err := repo.Insert(ctx, first); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	// Second event with same (fixture_id, natural_key) — should fail
	// with unique_violation. The concurrent-detection-race case.
	second := makeGoalEvent(7002, 1)
	err := repo.Insert(ctx, second)
	if err == nil {
		t.Fatal("expected unique_violation on duplicate natural_key, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("expected pgconn.PgError code 23505, got %v", err)
	}
	if !strings.Contains(err.Error(), "events") {
		t.Errorf("error missing 'events' context: %v", err)
	}
}

// GetByNaturalKey -----------------------------------------------

func TestEventRepo_GetByNaturalKey(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7003)

	e := makeGoalEvent(7003, 1)
	if err := repo.Insert(ctx, e); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.GetByNaturalKey(ctx, 7003, e.NaturalKey)
	if err != nil {
		t.Fatalf("GetByNaturalKey: %v", err)
	}
	if got.ID != e.ID {
		t.Errorf("ID = %v, want %v", got.ID, e.ID)
	}
}

func TestEventRepo_GetByNaturalKey_NotFound(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7004)
	_, err := repo.GetByNaturalKey(ctx, 7004, "40_999_Goal_99")
	if !errors.Is(err, event.ErrNotFound) {
		t.Errorf("GetByNaturalKey miss = %v, want ErrNotFound", err)
	}
}

// Upsert (state changes) -----------------------------------------

func TestEventRepo_Upsert_UpdatesStateFields(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7005)

	e := makeGoalEvent(7005, 1)
	if err := repo.Insert(ctx, e); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Simulate: monitor debounce passed → flip monitor_complete.
	e.MonitorComplete = true
	if err := repo.Upsert(ctx, e); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get after Upsert: %v", err)
	}
	if !got.MonitorComplete {
		t.Errorf("MonitorComplete = false, want true after Upsert")
	}
	if got.DownloadComplete || got.Removed {
		t.Errorf("other state flags shouldn't flip: %+v", got)
	}
}

func TestEventRepo_Upsert_SoftDeleteRoundtrip(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7006)

	e := makeGoalEvent(7006, 1)
	if err := repo.Insert(ctx, e); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Mark VAR-removed.
	e.Removed = true
	reason := event.RemovalVAR
	e.RemovedReason = &reason
	removedAt := time.Date(2026, 7, 8, 15, 32, 0, 0, time.UTC)
	e.RemovedAt = &removedAt
	if err := repo.Upsert(ctx, e); err != nil {
		t.Fatalf("Upsert soft-delete: %v", err)
	}

	got, err := repo.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get after soft-delete: %v", err)
	}
	if !got.Removed || got.RemovedReason == nil || *got.RemovedReason != event.RemovalVAR {
		t.Errorf("removed state wrong: %+v", got)
	}
	if got.RemovedAt == nil || !got.RemovedAt.Equal(removedAt) {
		t.Errorf("RemovedAt = %v, want %v", got.RemovedAt, removedAt)
	}
}

// ListPending ----------------------------------------------------

func TestEventRepo_ListPending(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7007)

	// Three events: pending, monitor-complete-but-not-downloaded (still pending),
	// fully-complete, removed. Only the first two should surface.
	pending := makeGoalEvent(7007, 1)
	if err := repo.Insert(ctx, pending); err != nil {
		t.Fatalf("Insert pending: %v", err)
	}
	monitorOnly := makeGoalEvent(7007, 2)
	if err := repo.Insert(ctx, monitorOnly); err != nil {
		t.Fatalf("Insert monitorOnly: %v", err)
	}
	monitorOnly.MonitorComplete = true
	if err := repo.Upsert(ctx, monitorOnly); err != nil {
		t.Fatalf("Upsert monitorOnly: %v", err)
	}
	fullyDone := makeGoalEvent(7007, 3)
	if err := repo.Insert(ctx, fullyDone); err != nil {
		t.Fatalf("Insert fullyDone: %v", err)
	}
	fullyDone.MonitorComplete = true
	fullyDone.DownloadComplete = true
	if err := repo.Upsert(ctx, fullyDone); err != nil {
		t.Fatalf("Upsert fullyDone: %v", err)
	}
	removed := makeGoalEvent(7007, 4)
	if err := repo.Insert(ctx, removed); err != nil {
		t.Fatalf("Insert removed: %v", err)
	}
	removed.Removed = true
	reason := event.RemovalVAR
	removed.RemovedReason = &reason
	now := time.Now().UTC()
	removed.RemovedAt = &now
	if err := repo.Upsert(ctx, removed); err != nil {
		t.Fatalf("Upsert removed: %v", err)
	}

	pendingList, err := repo.ListPending(ctx, 7007)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pendingList) != 2 {
		t.Fatalf("ListPending returned %d, want 2 (pending + monitorOnly): %+v", len(pendingList), pendingList)
	}
	// Ordered by first_seen_at — both used the same time in makeGoalEvent,
	// so verify the returned IDs cover exactly {pending, monitorOnly}.
	gotIDs := map[uuid.UUID]bool{pendingList[0].ID: true, pendingList[1].ID: true}
	if !gotIDs[pending.ID] || !gotIDs[monitorOnly.ID] {
		t.Errorf("ListPending returned wrong events: %+v", pendingList)
	}
}

func TestEventRepo_ListPending_Empty(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7008)
	pending, err := repo.ListPending(ctx, 7008)
	if err != nil {
		t.Fatalf("ListPending empty: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected empty pending, got %d events", len(pending))
	}
}
