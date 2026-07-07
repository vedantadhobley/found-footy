// Command api is the HTTP + SSE service serving the vedanta-systems
// frontend and any other external callers. Runs Chi + Huma per §8, with
// SSE fan-out subscribing to workspace NATS (§11 decision 2026-07-01).
//
// Phase S2.4: opens the pg pool at startup, blocks until SIGINT/SIGTERM,
// closes the pool cleanly on shutdown. Chi + Huma router lands in
// Phase A when the read endpoints are ready to serve real data.
package main

import (
	"context"

	"github.com/vedantadhobley/found-footy/internal/bootstrap"
	"github.com/vedantadhobley/found-footy/internal/infra/nats"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/infra/s3"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11
// deploy tracking.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("api", gitSHA, builtAt, func(ctx context.Context, deps *bootstrap.Deps) error {
		pgIns := pg.RegisterMetrics(deps.Metrics, deps.Log)
		pool, err := pg.New(ctx, deps.Cfg.Postgres, pgIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("pg", func(_ context.Context) error {
			pool.Close()
			return nil
		})

		natsIns := nats.RegisterMetrics(deps.Metrics, deps.Log)
		nc, err := nats.New(ctx, deps.Cfg.NATS, natsIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("nats", func(_ context.Context) error {
			nc.Close()
			return nil
		})

		s3Ins := s3.RegisterMetrics(deps.Metrics, deps.Log)
		s3c, err := s3.New(ctx, deps.Cfg.S3, s3Ins)
		if err != nil {
			return err
		}
		_ = s3c // consumed by the /videos/{share_id} presign path in Phase A
		// s3 client has no explicit Close (no persistent connection).

		// Public API surface lands here in Phase A. For now: hold the
		// adapters open until the signal-handled context cancels.
		<-ctx.Done()
		return nil
	})
}
