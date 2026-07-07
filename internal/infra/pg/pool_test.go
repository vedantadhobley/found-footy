package pg_test

import (
	"context"
	"testing"
	"time"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// TestPool_LifecycleAgainstRealPostgres spins up an ephemeral
// postgres:16-alpine container, verifies pg.New connects + emits
// ActionPoolConnected, runs a trivial SELECT, and confirms Close emits
// ActionPoolClosed.
//
// This is an integration test: the make test target mounts the host
// Docker socket + uses --network=host so testcontainers-go can spawn +
// reach the container. Skipped in -short mode for callers that want
// unit-only feedback.
func TestPool_LifecycleAgainstRealPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("footy_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(pgc); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	log := &logging.TestEmitter{}
	pool, err := pg.New(ctx, config.PGConfig{
		URL:            connStr,
		MaxConns:       5,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	}, log)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}

	if !log.HasAction(vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnected) {
		t.Errorf("expected ActionPoolConnected emission; got: %+v", log.Snapshot())
	}

	if err := pool.Ping(ctx); err != nil {
		t.Errorf("ping failed: %v", err)
	}

	var got int
	if err := pool.QueryRow(ctx, "SELECT 42").Scan(&got); err != nil {
		t.Fatalf("SELECT 42: %v", err)
	}
	if got != 42 {
		t.Errorf("SELECT 42 = %d, want 42", got)
	}

	pool.Close()

	if !log.HasAction(vocabulary.ModuleInfraPG, vocabulary.ActionPoolClosed) {
		t.Errorf("expected ActionPoolClosed emission after Close(); got: %+v", log.Snapshot())
	}
}

// TestNew_EmptyURL_Errors covers the fast-fail path: a binary starts
// without POSTGRES_URL and pg.New refuses to build a broken pool. No
// container needed.
func TestNew_EmptyURL_Errors(t *testing.T) {
	log := &logging.TestEmitter{}
	_, err := pg.New(context.Background(), config.PGConfig{
		URL:      "",
		MaxConns: 5,
		MinConns: 1,
	}, log)
	if err == nil {
		t.Fatal("expected error for empty POSTGRES_URL, got nil")
	}
}

// TestNew_UnreachableHost_ErrorsQuickly ensures a bad URL doesn't hang
// the binary's startup — the ConnectTimeout bounds the initial ping.
func TestNew_UnreachableHost_ErrorsQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration-ish test skipped in -short mode")
	}

	log := &logging.TestEmitter{}
	start := time.Now()
	_, err := pg.New(context.Background(), config.PGConfig{
		// Reserved discard address per RFC 6890 — should refuse-connect fast.
		URL:            "postgres://x:x@192.0.2.1:5432/none?sslmode=disable",
		MaxConns:       1,
		MinConns:       1,
		ConnectTimeout: 2 * time.Second,
	}, log)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for unreachable host, got nil")
	}
	// Give it 3× the timeout as slack — CI can be slow.
	if elapsed > 6*time.Second {
		t.Errorf("New took %v, want ≤ 6s (timeout was 2s)", elapsed)
	}
	if !log.HasAction(vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed) {
		t.Errorf("expected ActionPoolConnectFailed emission; got: %+v", log.Snapshot())
	}
}
