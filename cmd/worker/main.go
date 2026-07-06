// Command worker is the Temporal worker binary — registers workflows and
// activities and processes tasks from the found-footy task queue. See §5
// orchestration + §16.5 Phase O for the workflows this binary hosts.
package main

import "fmt"

// gitSHA, builtAt, binary are baked in at build time via -ldflags per §11
// deploy tracking. Empty defaults for direct `go run` invocations.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
	binary  = "worker"
)

func main() {
	fmt.Printf("hello from %s (sha=%s built=%s)\n", binary, gitSHA, builtAt)
}
