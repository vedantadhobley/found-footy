// Package pg is the Postgres substrate adapter. Domain code depends on
// this package's Pool type (which embeds *pgxpool.Pool), never on
// pgxpool directly — that keeps the driver swap-able and lets us layer
// observability + retry policy in one place.
//
// Phase S2.1: pool construction, ping, close.
// Phase S2.2: initial schema (schema.sql, applied via docker-entrypoint-initdb.d
// in dev and tcpostgres.WithInitScripts in tests).
// Phase S2.3: query-level metrics + structured logs via pgx.QueryTracer;
// pool-stats collector for pgxpool.Stat gauges at scrape time.
package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Pool is the Postgres connection pool the domain layer depends on.
// Embeds *pgxpool.Pool so callers get every pgx method for free (Query,
// QueryRow, Exec, Acquire, BeginTx, ...) without a pass-through layer.
// The wrapper exists to (a) emit lifecycle log lines, (b) hold the
// Observability instance for pool-stats deregistration on Close, and
// (c) override Ping with an explicit ActionPing / ActionPingFailed
// emission for /healthz callers that want a clean signal.
type Pool struct {
	*pgxpool.Pool
	obs            *Observability
	statsCollector *poolStatsCollector
}

// New builds a pool from cfg + attaches the query tracer + registers a
// pool-stats collector on obs.reg. Pings the pool once, bounded by
// cfg.ConnectTimeout, so a dead Postgres can't hang startup.
//
// The caller is responsible for calling Close when done.
func New(ctx context.Context, cfg config.PGConfig, obs *Observability) (*Pool, error) {
	if obs == nil {
		return nil, fmt.Errorf("pg.New: Observability is required (call RegisterMetrics first)")
	}
	if cfg.DSN == "" {
		return nil, fmt.Errorf("pg.New: PG_DSN not set")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		obs.log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed,
			"parse PG_DSN failed",
			logging.Err(err),
		)
		return nil, fmt.Errorf("pg.New: parse DSN: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	// Attach the pgx-native query tracer — every Query/QueryRow/Exec on
	// this pool fires TraceQueryStart + TraceQueryEnd, which increments
	// found_footy_pg_queries_total and emits a structured log line.
	poolCfg.ConnConfig.Tracer = &queryTracer{obs: obs}

	pgxPool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		obs.log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed,
			"construct pool failed",
			logging.Err(err),
		)
		return nil, fmt.Errorf("pg.New: create pool: %w", err)
	}

	// Bound the initial Ping by the configured connect timeout so a
	// dead Postgres doesn't hang the binary's startup indefinitely.
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pgxPool.Ping(pingCtx); err != nil {
		pgxPool.Close()
		obs.log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed,
			"initial ping failed",
			logging.Err(err),
		)
		return nil, fmt.Errorf("pg.New: ping: %w", err)
	}

	// Register a pool-stats collector so scrape-time gauges reflect the
	// live pgxpool.Stat. One collector per pool; unregistered in Close.
	statsCollector := newPoolStatsCollector(pgxPool)
	obs.reg.PrometheusRegistry().MustRegister(statsCollector)

	obs.log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnected,
		"pg pool ready",
		logging.Int("max_conns", int(cfg.MaxConns)),
		logging.Int("min_conns", int(cfg.MinConns)),
		logging.String("connect_timeout", cfg.ConnectTimeout.String()),
	)

	return &Pool{
		Pool:           pgxPool,
		obs:            obs,
		statsCollector: statsCollector,
	}, nil
}

// Close shuts down the pool, deregisters the pool-stats collector, and
// emits a lifecycle log line. Safe to call once.
func (p *Pool) Close() {
	if p.statsCollector != nil {
		p.obs.reg.PrometheusRegistry().Unregister(p.statsCollector)
		p.statsCollector = nil
	}
	p.obs.log.Emit(context.Background(), logging.LevelInfo,
		vocabulary.ModuleInfraPG, vocabulary.ActionPoolClosed,
		"pg pool closed",
	)
	p.Pool.Close()
}

// Ping runs a health-probe round trip and emits ActionPing /
// ActionPingFailed. Callers use this in /healthz handlers and pre-query
// gates for a clean single-outcome signal, distinct from the
// per-query traffic ActionQuery reports.
//
// Note: pgxpool's internal Ping issues a "SELECT 1" that also flows
// through the query tracer — expect one ActionPing/ActionPingFailed
// plus one ActionQuery per Ping call.
func (p *Pool) Ping(ctx context.Context) error {
	start := time.Now()
	if err := p.Pool.Ping(ctx); err != nil {
		p.obs.log.Emit(ctx, logging.LevelWarn,
			vocabulary.ModuleInfraPG, vocabulary.ActionPingFailed,
			"pg ping failed",
			logging.Int64("elapsed_ms", time.Since(start).Milliseconds()),
			logging.Err(err),
		)
		return err
	}
	p.obs.log.Emit(ctx, logging.LevelDebug,
		vocabulary.ModuleInfraPG, vocabulary.ActionPing,
		"pg ping ok",
		logging.Int64("elapsed_ms", time.Since(start).Milliseconds()),
	)
	return nil
}
