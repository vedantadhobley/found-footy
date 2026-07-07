// Command scaler is the auto-scaling sidecar that watches Temporal
// queue depth + active-goal count and scales the worker + twitter
// pools between 2 and 8 replicas. See §7 for the scaling policy.
//
// Phase S5.2: opens the Temporal client at startup so it can query
// queue depth. Scaling loop lands in a later phase; for now the client
// is held open + closed cleanly on shutdown.
package main

import (
	"context"

	"github.com/vedantadhobley/found-footy/internal/bootstrap"
	"github.com/vedantadhobley/found-footy/internal/infra/temporal"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11
// deploy tracking.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("scaler", gitSHA, builtAt, func(ctx context.Context, deps *bootstrap.Deps) error {
		tempIns := temporal.RegisterMetrics(deps.Metrics, deps.Log)
		tempClient, err := temporal.NewClient(ctx, deps.Cfg.Temporal, tempIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("temporal-client", func(_ context.Context) error {
			tempClient.Close()
			return nil
		})
		_ = tempClient // consumed by the scaling loop in a later phase

		<-ctx.Done()
		return nil
	})
}
