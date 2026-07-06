// Command api is the HTTP + SSE service serving the vedanta-systems
// frontend and any other external callers. Runs Chi + Huma per §8, with
// SSE fan-out subscribing to workspace NATS (§11 decision 2026-07-01).
//
// Phase S1: startup + shutdown observability only; router lands in
// Phase A.
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
	bootstrap.Run("api", gitSHA, builtAt, bootstrap.BlockUntilDone)
}
