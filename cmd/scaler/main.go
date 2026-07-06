// Command scaler is the auto-scaling sidecar binary — watches Temporal
// queue depth + active-goal count and scales worker/twitter replicas
// between 2 and 8. See docs/rebuild-plan.md architecture § scaler.
package main

import "fmt"

var (
	gitSHA  = "dev"
	builtAt = "unknown"
	binary  = "scaler"
)

func main() {
	fmt.Printf("hello from %s (sha=%s built=%s)\n", binary, gitSHA, builtAt)
}
