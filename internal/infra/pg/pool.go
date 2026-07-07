// Package pg is the Postgres substrate adapter. Domain code depends on
// this package's Pool type (which embeds *pgxpool.Pool), never on
// pgxpool directly — that keeps the driver swap-able and lets us layer
// observability + retry policy in one place.
//
// Phase S2.1: pool construction, ping, close. Query-level metric hooks
// and structured DEBUG logging land in S2.3.
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
// The wrapper exists to (a) close cleanly with a lifecycle log line and
// (b) give us a place to hang observability hooks in S2.3.
type Pool struct {
	*pgxpool.Pool
	log logging.Emitter
}

// New builds a pool from cfg + Pings it. Returns a descriptive error
// on any failure — never panics — so binaries can log-and-exit cleanly.
//
// The caller is responsible for calling Close when done.
func New(ctx context.Context, cfg config.PGConfig, log logging.Emitter) (*Pool, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("pg.New: POSTGRES_URL not set")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed,
			"parse POSTGRES_URL failed",
			logging.Err(err),
		)
		return nil, fmt.Errorf("pg.New: parse URL: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	pgxPool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed,
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
		log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnectFailed,
			"initial ping failed",
			logging.Err(err),
		)
		return nil, fmt.Errorf("pg.New: ping: %w", err)
	}

	log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleInfraPG, vocabulary.ActionPoolConnected,
		"pg pool ready",
		logging.Int("max_conns", int(cfg.MaxConns)),
		logging.Int("min_conns", int(cfg.MinConns)),
		logging.String("connect_timeout", cfg.ConnectTimeout.String()),
	)

	return &Pool{Pool: pgxPool, log: log}, nil
}

// Close shuts down the pool + emits a lifecycle log line. Safe to call
// once; pgxpool.Close is idempotent under a single caller.
func (p *Pool) Close() {
	p.log.Emit(context.Background(), logging.LevelInfo,
		vocabulary.ModuleInfraPG, vocabulary.ActionPoolClosed,
		"pg pool closed",
	)
	p.Pool.Close()
}

// Ping runs a health-probe round trip and emits a matching action.
// Callers use this in /healthz handlers and pre-query gates.
func (p *Pool) Ping(ctx context.Context) error {
	start := time.Now()
	if err := p.Pool.Ping(ctx); err != nil {
		p.log.Emit(ctx, logging.LevelWarn,
			vocabulary.ModuleInfraPG, vocabulary.ActionPingFailed,
			"pg ping failed",
			logging.Int64("elapsed_ms", time.Since(start).Milliseconds()),
			logging.Err(err),
		)
		return err
	}
	p.log.Emit(ctx, logging.LevelDebug,
		vocabulary.ModuleInfraPG, vocabulary.ActionPing,
		"pg ping ok",
		logging.Int64("elapsed_ms", time.Since(start).Milliseconds()),
	)
	return nil
}
