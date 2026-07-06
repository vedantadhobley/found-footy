// Command scaler is the auto-scaling sidecar that watches Temporal
// queue depth + active-goal count and scales the worker + twitter
// pools between 2 and 8 replicas. See §7 for the scaling policy.
//
// Phase S1: startup + shutdown observability only; scaling loop lands
// alongside the Temporal adapter in Phase S5.
package main

import (
	"github.com/vedantadhobley/found-footy/internal/bootstrap"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11
// deploy tracking.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("scaler", gitSHA, builtAt, bootstrap.BlockUntilDone)
}
