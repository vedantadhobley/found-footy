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
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11
// deploy tracking.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("api", gitSHA, builtAt, func(ctx context.Context, deps *bootstrap.Deps) error {
		pgObs := pg.RegisterMetrics(deps.Metrics, deps.Log)
		pool, err := pg.New(ctx, deps.Cfg.Postgres, pgObs)
		if err != nil {
			return err
		}
		defer pool.Close()

		// Public API surface lands here in Phase A. For now: hold the
		// pool open until the signal-handled context cancels.
		<-ctx.Done()
		return nil
	})
}
