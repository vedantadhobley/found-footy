// Tests for the FixtureRepo — real Postgres via testcontainers with
// the app schema loaded (same runTestPostgres helper as pool_test.go).
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

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
