// Command twitter is the Playwright-Go browser-automation binary —
// serves the /search endpoint that the DiscoveryWorkflow queries. See
// §16.3 S7 external adapters + docs/rebuild-plan.md twitter fleet.
package main

import "fmt"

var (
	gitSHA  = "dev"
	builtAt = "unknown"
	binary  = "twitter"
)

func main() {
	fmt.Printf("hello from %s (sha=%s built=%s)\n", binary, gitSHA, builtAt)
}
