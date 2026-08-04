// DiscoveryConfig owns the EventWorkflow tuning knobs — the values
// that were consts in internal/workflow/discovery.go through 2026-07-22.
// Moved here so operators can bump `DISCOVERY_MAX_ATTEMPTS=20` in prod
// without a rebuild, and so the audit surface exposes what's tunable.
//
// Consumed by EventWorkflow via the GetDiscoveryConfig activity
// (Temporal-determinism rule: workflows can't touch env directly).
// See internal/activity/discovery/activities.go for the accessor.
package config

import "time"

// DiscoveryConfig — env-driven settings for EventWorkflow's
// per-event candidate collection cycle. Every field has an envDefault
// matched to what was hardcoded on 2026-07-22, EXCEPT MaxAttempts which
// bumps from 10 → 15 (decision 2026-07-23 user sign-off: multi-attempt
// coverage is load-bearing, some goal videos surface only 5-10 min
// after the initial burst — see decisions.md 2026-07-23 wall-clock
// design entry for evidence).
type DiscoveryConfig struct {
	// MaxAttempts — how many /search calls EventWorkflow issues
	// per event. At AttemptSpacing=60s, this is the workflow's total
	// lifetime in minutes. Default 15.
	MaxAttempts int `env:"DISCOVERY_MAX_ATTEMPTS" envDefault:"15"`

	// AttemptSpacing — sleep between attempts. Default 60s per
	// twitter-search-query.md D5. Do not jitter — Twitter's search
	// response time (10-25s) already provides natural spacing variance.
	AttemptSpacing time.Duration `env:"DISCOVERY_ATTEMPT_SPACING" envDefault:"60s"`

	// MaxAgeMinutes — client-side age filter passed to T/c's /search
	// endpoint. Candidates whose tweet age at scrape time is greater
	// than this get dropped by the twitter service before returning.
	// Default 3 per twitter-search-query.md D4.
	MaxAgeMinutes int `env:"DISCOVERY_MAX_AGE_MINUTES" envDefault:"3"`

	// QueryTimeout — StartToCloseTimeout on a single SearchTweets
	// activity. T/c's typical /search finishes in 10-25s; this is a
	// hang-detection ceiling, not a normal-path budget. Default 2m.
	QueryTimeout time.Duration `env:"DISCOVERY_QUERY_TIMEOUT" envDefault:"2m"`
}
