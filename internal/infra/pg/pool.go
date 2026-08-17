// Package pg is the Postgres substrate adapter. Domain code depends on
// this package's Pool type (which embeds *pgxpool.Pool), never on
// pgxpool directly — that keeps the driver swap-able and lets us layer
// observability + retry policy in one place.
//
// It owns pool construction, ping/close, schema verification, query-level
// metrics and logs, and scrape-time pgxpool statistics.
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

// Pool wraps *pgxpool.Pool (embedded, so every pgx method is directly
// callable) with lifecycle log emissions and pool-stats deregistration
// on Close. Ping is overridden for /healthz callers that want a single
// ActionPing/ActionPingFailed signal separate from per-query traffic.
type Pool struct {
	*pgxpool.Pool
	ins            *Instruments
	statsCollector *poolStatsCollector
}

// New builds a pool from cfg + attaches the query tracer + registers a
// pool-stats collector on ins.reg. Pings the pool once, bounded by
// cfg.ConnectTimeout, so a dead Postgres can't hang startup.
//
// The caller is responsible for calling Close when done.
func New(ctx context.Context, cfg config.PGConfig, ins *Instruments) (*Pool, error) {
	if ins == nil {
		return nil, fmt.Errorf("pg.New: Instruments is required (call RegisterMetrics first)")
	}
	if cfg.DSN == "" {
		return nil, fmt.Errorf("pg.New: PG_DSN not set")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		ins.log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed,
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
	poolCfg.ConnConfig.Tracer = &queryTracer{ins: ins}

	pgxPool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		ins.log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed,
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
		ins.log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed,
			"initial ping failed",
			logging.Err(err),
		)
		return nil, fmt.Errorf("pg.New: ping: %w", err)
	}

	// Register a pool-stats collector so scrape-time gauges reflect the
	// live pgxpool.Stat. One collector per pool; unregistered in Close.
	statsCollector := newPoolStatsCollector(pgxPool)
	ins.reg.PrometheusRegistry().MustRegister(statsCollector)

	ins.log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnected,
		"pg pool ready",
		logging.Int("max_conns", int(cfg.MaxConns)),
		logging.Int("min_conns", int(cfg.MinConns)),
		logging.String("connect_timeout", cfg.ConnectTimeout.String()),
	)

	return &Pool{
		Pool:           pgxPool,
		ins:            ins,
		statsCollector: statsCollector,
	}, nil
}

// Close shuts down the pool, deregisters the pool-stats collector, and
// emits a lifecycle log line. Idempotent — the statsCollector guard
// makes second calls a no-op on the deregistration side, and
// pgxpool.Close is itself idempotent under a single caller.
func (p *Pool) Close() {
	if p.statsCollector != nil {
		p.ins.reg.PrometheusRegistry().Unregister(p.statsCollector)
		p.statsCollector = nil
	}
	p.ins.log.Emit(context.Background(), logging.LevelInfo,
		vocabulary.ModuleInfraPG, vocabulary.ActionPoolClosed,
		"pg pool closed",
	)
	p.Pool.Close()
}

// Ping runs a health-probe round trip and emits ActionPing /
// ActionPingFailed for /healthz callers. Note: pgxpool.Ping issues a
// SELECT 1 that also flows through the query tracer, so each Ping
// produces one ActionPing plus one ActionQuery.
func (p *Pool) Ping(ctx context.Context) error {
	start := time.Now()
	if err := p.Pool.Ping(ctx); err != nil {
		p.ins.log.Emit(ctx, logging.LevelWarn,
			vocabulary.ModuleInfraPG, vocabulary.ActionPingFailed,
			"pg ping failed",
			logging.Int64("elapsed_ms", time.Since(start).Milliseconds()),
			logging.Err(err),
		)
		return err
	}
	p.ins.log.Emit(ctx, logging.LevelDebug,
		vocabulary.ModuleInfraPG, vocabulary.ActionPing,
		"pg ping ok",
		logging.Int64("elapsed_ms", time.Since(start).Milliseconds()),
	)
	return nil
}
