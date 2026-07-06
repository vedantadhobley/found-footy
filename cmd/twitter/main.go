// Command twitter is the Firefox+Selenium browser-automation service
// that scrapes Twitter for goal-video links. See docs/twitter-auth.md
// for cookie/auth lifecycle; §12 for the port surface.
//
// Phase S1: startup + shutdown observability only; browser wiring lands
// in Phase S7 (external adapters) alongside the API-Football +
// syndication + Wikidata adapters.
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
	bootstrap.Run("twitter", gitSHA, builtAt, bootstrap.BlockUntilDone)
}
