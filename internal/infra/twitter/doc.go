// Package twitter is the HTTP client that reaches the twitter binary's
// /search endpoint (Playwright-Go browser automation). Single-endpoint
// wrapper — exposes Search + an internal probeHealth check; there is no
// instance registry / healthy-selection / fleet-drain (per-event instances
// are #160, see design/proposals/twitter-scaling.md).
package twitter
