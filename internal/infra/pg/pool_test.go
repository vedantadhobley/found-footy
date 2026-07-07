package pg_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// pgvector/pgvector:pg16 = official Postgres 16 with the pgvector
// extension preinstalled. Matches the image dev postgres runs. Using
// the same image in tests catches any extension-availability drift.
const pgImage = "pgvector/pgvector:pg16"

// testFixture bundles a fresh metrics.Registry, TestEmitter, and
// pg.Observability per test. Isolated registries mean multi-pool tests
// don't collide on Prometheus's "duplicate metrics" rule.
type testFixture struct {
	reg *metrics.Registry
	log *logging.TestEmitter
	obs *pg.Observability
}

func newTestFixture(_ *testing.T) *testFixture {
	reg := metrics.New()
	log := &logging.TestEmitter{}
	obs := pg.RegisterMetrics(reg, log)
	return &testFixture{reg: reg, log: log, obs: obs}
}

// runTestPostgres starts an ephemeral Postgres container with the app
// schema loaded from internal/infra/pg/schema.sql. Returns the
// connection string. Registers a cleanup that terminates the container.
func runTestPostgres(t *testing.T, ctx context.Context) string {
	t.Helper()

	pgc, err := tcpostgres.Run(ctx,
		pgImage,
		tcpostgres.WithDatabase("found_footy"),
		tcpostgres.WithUsername("ffuser"),
		tcpostgres.WithPassword("ffpass"),
		tcpostgres.WithInitScripts("schema.sql"),
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
	return connStr
}

// scrapeMetrics runs the metrics handler and returns the exposition body.
// Used by tests that assert pg-scoped metrics show up on /metrics.
func scrapeMetrics(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	reg.Handler().ServeHTTP(w, req)
	body, err := io.ReadAll(w.Result().Body)
	if err != nil {
		t.Fatalf("read scrape body: %v", err)
	}
	return string(body)
}

// TestPool_LifecycleAgainstRealPostgres spins up an ephemeral Postgres,
// verifies pg.New connects + emits ActionPoolConnected, runs a trivial
// SELECT, and confirms Close emits ActionPoolClosed.
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

	connStr := runTestPostgres(t, ctx)
	fx := newTestFixture(t)

	pool, err := pg.New(ctx, config.PGConfig{
		URL:            connStr,
		MaxConns:       5,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	}, fx.obs)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}

	if !fx.log.HasAction(vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnected) {
		t.Errorf("expected ActionPoolConnected emission; got: %+v", fx.log.Snapshot())
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

	if !fx.log.HasAction(vocabulary.ModuleInfraPG, vocabulary.ActionPoolClosed) {
		t.Errorf("expected ActionPoolClosed emission after Close(); got: %+v", fx.log.Snapshot())
	}
}

// TestSchemaLoaded_CoreTables verifies the WithInitScripts hook actually
// applied schema.sql — every expected §3 table + all three extensions
// present. Guards against a silently-broken schema.sql that would let
// pool tests pass but Phase D queries fail.
func TestSchemaLoaded_CoreTables(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	connStr := runTestPostgres(t, ctx)
	fx := newTestFixture(t)

	pool, err := pg.New(ctx, config.PGConfig{
		URL:            connStr,
		MaxConns:       2,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	}, fx.obs)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}
	defer pool.Close()

	expectedTables := []string{
		"fixtures",
		"events",
		"event_monitor_workflows",
		"event_download_workflows",
		"event_drop_workflows",
		"video_assets",
		"video_shares",
		"tweet_intent",
		"team_aliases",
		"twitter_sessions",
		"event_log",
		"webhook_subscriptions",
		"webhook_deliveries",
		"outbox_cursor",
	}
	for _, tbl := range expectedTables {
		var exists bool
		err := pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)",
			tbl).Scan(&exists)
		if err != nil {
			t.Errorf("check table %q: %v", tbl, err)
			continue
		}
		if !exists {
			t.Errorf("expected table %q not found in schema", tbl)
		}
	}

	expectedExtensions := []string{"pgcrypto", "pg_trgm", "vector"}
	for _, ext := range expectedExtensions {
		var exists bool
		err := pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)", ext).Scan(&exists)
		if err != nil {
			t.Errorf("check extension %q: %v", ext, err)
			continue
		}
		if !exists {
			t.Errorf("expected extension %q not installed", ext)
		}
	}

	// Sanity: outbox_cursor is bootstrapped with a singleton row.
	var cursorID int
	if err := pool.QueryRow(ctx, "SELECT id FROM outbox_cursor").Scan(&cursorID); err != nil {
		t.Errorf("outbox_cursor bootstrap row missing: %v", err)
	}
	if cursorID != 1 {
		t.Errorf("outbox_cursor.id = %d, want 1", cursorID)
	}
}

// TestQueryTracer_EmitsMetricsAndLogs runs a mix of successful and
// failing queries against a real Postgres, then asserts both the
// counter series and the corresponding structured log lines fired
// through the pgx.QueryTracer hook.
func TestQueryTracer_EmitsMetricsAndLogs(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	connStr := runTestPostgres(t, ctx)
	fx := newTestFixture(t)

	pool, err := pg.New(ctx, config.PGConfig{
		URL:            connStr,
		MaxConns:       2,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	}, fx.obs)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}
	defer pool.Close()

	// Clear captured entries populated by pool startup + Ping so we can
	// assert on tracer output specifically.
	fx.log.Reset()

	// Successful SELECT.
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM fixtures").Scan(&n); err != nil {
		t.Fatalf("SELECT count: %v", err)
	}

	// Deliberately-failing query — table doesn't exist.
	_, err = pool.Exec(ctx, "SELECT * FROM this_table_does_not_exist")
	if err == nil {
		t.Fatal("expected error from bogus SELECT, got nil")
	}

	// Log assertions: tracer should have emitted both actions.
	if !fx.log.HasAction(vocabulary.ModuleInfraPG, vocabulary.ActionQuery) {
		t.Errorf("expected ActionQuery emission from successful select; captured=%+v", fx.log.Snapshot())
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraPG, vocabulary.ActionQueryFailed) {
		t.Errorf("expected ActionQueryFailed emission from bogus select; captured=%+v", fx.log.Snapshot())
	}

	// Metric assertions: both counter series should appear in the scrape.
	scrape := scrapeMetrics(t, fx.reg)
	if !strings.Contains(scrape, `found_footy_pg_queries_total{op="select",outcome="success"}`) {
		t.Errorf("scrape missing success series:\n%s", scrape)
	}
	if !strings.Contains(scrape, `found_footy_pg_queries_total{op="select",outcome="failure"}`) {
		t.Errorf("scrape missing failure series:\n%s", scrape)
	}
	// Histogram should register at least one sample per op.
	if !strings.Contains(scrape, `found_footy_pg_query_duration_seconds_count{op="select"}`) {
		t.Errorf("scrape missing histogram count series:\n%s", scrape)
	}
}

// TestPoolStats_ReportedOnScrape confirms the pool-stats collector is
// registered on pg.New and its gauges appear in the metrics scrape.
// Unregistered on Close so multi-pool tests wouldn't collide.
func TestPoolStats_ReportedOnScrape(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	connStr := runTestPostgres(t, ctx)
	fx := newTestFixture(t)

	pool, err := pg.New(ctx, config.PGConfig{
		URL:            connStr,
		MaxConns:       7, // distinctive value so we can assert on the max gauge
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	}, fx.obs)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}

	scrape := scrapeMetrics(t, fx.reg)
	for _, want := range []string{
		"found_footy_pg_pool_connections_acquired",
		"found_footy_pg_pool_connections_idle",
		"found_footy_pg_pool_connections_total",
		"found_footy_pg_pool_connections_max",
	} {
		if !strings.Contains(scrape, want) {
			t.Errorf("scrape missing pool stat %q:\n%s", want, scrape)
		}
	}
	if !strings.Contains(scrape, "found_footy_pg_pool_connections_max 7") {
		t.Errorf("scrape doesn't report configured max=7:\n%s", scrape)
	}

	pool.Close()

	// After Close, the collector should be unregistered — the gauges
	// no longer appear in the scrape output.
	scrapeAfter := scrapeMetrics(t, fx.reg)
	if strings.Contains(scrapeAfter, "found_footy_pg_pool_connections_max") {
		t.Errorf("pool-stats collector still registered after Close():\n%s", scrapeAfter)
	}
}

// TestNew_NilObs_Errors ensures the constructor rejects a nil
// Observability up front — no silent fallback, no partial startup.
func TestNew_NilObs_Errors(t *testing.T) {
	_, err := pg.New(context.Background(), config.PGConfig{URL: "postgres://x@x/x"}, nil)
	if err == nil {
		t.Fatal("expected error for nil Observability, got nil")
	}
}

// TestNew_EmptyURL_Errors covers the fast-fail path: a binary starts
// without POSTGRES_URL and pg.New refuses to build a broken pool. No
// container needed.
func TestNew_EmptyURL_Errors(t *testing.T) {
	fx := newTestFixture(t)
	_, err := pg.New(context.Background(), config.PGConfig{
		URL:      "",
		MaxConns: 5,
		MinConns: 1,
	}, fx.obs)
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

	fx := newTestFixture(t)
	start := time.Now()
	_, err := pg.New(context.Background(), config.PGConfig{
		// Reserved discard address per RFC 6890 — should refuse-connect fast.
		URL:            "postgres://x:x@192.0.2.1:5432/none?sslmode=disable",
		MaxConns:       1,
		MinConns:       1,
		ConnectTimeout: 2 * time.Second,
	}, fx.obs)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for unreachable host, got nil")
	}
	if elapsed > 6*time.Second {
		t.Errorf("New took %v, want ≤ 6s (timeout was 2s)", elapsed)
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed) {
		t.Errorf("expected ActionPoolConnectFailed emission; got: %+v", fx.log.Snapshot())
	}
}
