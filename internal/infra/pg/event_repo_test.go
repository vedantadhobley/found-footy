// Tests for the EventRepo — testcontainer Postgres with the app
// schema loaded (same runTestPostgres helper as pool_test.go +
// fixture_repo_test.go).
//
// Fix 3a scope: basic CRUD only (Get, GetByNaturalKey, Insert,
// Upsert, ListPending). Debounce methods land in 3b tests.
package pg_test

import (
	"context"
	"testing"
	"time"

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
	if err := repo.Insert(ctx, f); err != nil {
		t.Fatalf("seed Insert: %v", err)
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
