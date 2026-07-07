// Command worker is the Temporal worker binary — registers workflows and
// activities and processes tasks from the found-footy task queue. See §5
// orchestration + §16.5 Phase O for the workflows this binary hosts.
//
// Phase S2.4: opens the pg pool at startup, blocks until SIGINT/SIGTERM,
// closes the pool cleanly on shutdown. Workflow/activity registration
// lands in Phase O when the domain layer is ready to be plugged in.
package main

import (
	"context"

	"github.com/vedantadhobley/found-footy/internal/bootstrap"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11
// deploy tracking. Empty defaults for direct `go run` invocations.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("worker", gitSHA, builtAt, func(ctx context.Context, deps *bootstrap.Deps) error {
		pgObs := pg.RegisterMetrics(deps.Metrics, deps.Log)
		pool, err := pg.New(ctx, deps.Cfg.Postgres, pgObs)
		if err != nil {
			return err
		}
		defer pool.Close()

		// Domain workflows land here in Phase O. For now: hold the pool
		// open until the signal-handled context cancels.
		<-ctx.Done()
		return nil
	})
}
