// pg.go — testcontainer Postgres for the harness. Single container
// per test binary invocation; scenarios share it via TRUNCATE
// between runs.
package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

// SetupPG spins up a Postgres testcontainer with the app schema
// loaded and returns a pg.Pool wired against it. Registers cleanup
// via t.Cleanup — no manual teardown needed.
func SetupPG(ctx context.Context, t *testing.T) (*pg.Pool, testcontainers.Container) {
	t.Helper()

	schemaPath := findRepoFile(t, "internal/infra/pg/schema.sql")

	pgContainer, err := postgres.Run(ctx,
		"pgvector/pgvector:pg16",
		postgres.WithDatabase("found_footy"),
		postgres.WithUsername("ffuser"),
		postgres.WithPassword("ffpass"),
		postgres.WithInitScripts(schemaPath),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("harness.SetupPG: container start: %v", err)
	}
	t.Cleanup(func() {
		_ = pgContainer.Terminate(context.Background())
	})

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("harness.SetupPG: connection string: %v", err)
	}

	reg := metrics.New()
	log := &logging.TestEmitter{}
	ins := pg.RegisterMetrics(reg, log)
	pool, err := pg.New(ctx, config.PGConfig{
		DSN:            connStr,
		MaxConns:       5,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("harness.SetupPG: pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, pgContainer
}

// TruncateAll wipes every scenario-touched table. TRUNCATE ... CASCADE
// so FK-dependent rows go with their parents. Runs in <10ms.
func TruncateAll(ctx context.Context, t *testing.T, pool *pg.Pool) {
	t.Helper()
	tables := []string{
		"fixtures",    // cascades to events + event_*_workflows (FK ON DELETE CASCADE)
		"team_aliases",
		// video_assets + video_shares don't cascade from fixtures;
		// add here when scenarios write them.
	}
	for _, tbl := range tables {
		if _, err := pool.Exec(ctx, "TRUNCATE "+tbl+" CASCADE"); err != nil {
			t.Fatalf("harness.TruncateAll: TRUNCATE %s: %v", tbl, err)
		}
	}
}

// findRepoFile walks up from cwd looking for the given path relative
// to the repo root. Works whether tests run from repo root or a
// subdirectory.
func findRepoFile(t *testing.T, relative string) string {
	t.Helper()
	cwd, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(cwd, relative)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		cwd = filepath.Dir(cwd)
	}
	t.Fatalf("harness.findRepoFile: %s not found within 8 parents of cwd", relative)
	return ""
}
