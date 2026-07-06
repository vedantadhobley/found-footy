// Command api is the HTTP API binary — Chi + Huma serving typed
// endpoints, NATS-backed SSE stream, JetStream-backed webhook delivery.
// See §8 for the surface this binary hosts.
package main

import "fmt"

var (
	gitSHA  = "dev"
	builtAt = "unknown"
	binary  = "api"
)

func main() {
	fmt.Printf("hello from %s (sha=%s built=%s)\n", binary, gitSHA, builtAt)
}
