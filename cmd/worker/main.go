// Command worker is the Temporal worker binary — registers workflows and
// activities and processes tasks from the found-footy task queue. See §5
// orchestration + §16.5 Phase O for the workflows this binary hosts.
//
// Phase S1: startup + shutdown observability only; workflow/activity
// registration lands in Phase O.
package main

import (
	"github.com/vedantadhobley/found-footy/internal/bootstrap"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11
// deploy tracking. Empty defaults for direct `go run` invocations.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("worker", gitSHA, builtAt, bootstrap.BlockUntilDone)
}
