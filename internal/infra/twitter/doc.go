// Package twitter is the HTTP client that reaches the twitter binary's
// /search endpoint (Playwright-Go browser automation). Construction validates
// static configuration without probing remote readiness, so later Search calls
// recover when a browser service returns. There is no instance registry /
// healthy-selection / fleet-drain (per-event instances are #160, see
// design/proposals/twitter-scaling.md).
package twitter
