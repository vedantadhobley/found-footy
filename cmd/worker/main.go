// Command worker starts the production Temporal worker composition root.
package main

import (
	"context"

	workerapp "github.com/vedantadhobley/found-footy/internal/app/worker"
	"github.com/vedantadhobley/found-footy/internal/bootstrap"
)

// gitSHA and builtAt are baked in at build time via -ldflags.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("worker", gitSHA, builtAt, func(ctx context.Context, deps *bootstrap.Deps) error {
		return workerapp.Run(ctx, deps)
	})
}
