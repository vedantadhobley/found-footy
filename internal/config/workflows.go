// WorkflowsConfig — env-driven settings shared across IngestWorkflow +
// MonitorWorkflow orchestration. Values that used to live as
// `const default*` in the workflow files (fragile — the same value
// declared twice across ingest.go + monitor.go was one accidental edit
// away from silent divergence) now live here and are threaded through
// each workflow's Activities struct at cmd/worker startup.
package config

import "time"

// WorkflowsConfig owns cross-workflow orchestration settings.
//
// StagingPollInterval is the source of truth for the activation window.
// ActivationWindow is DERIVED as ActivationMultiplier × StagingPollInterval
// via the method below — no separate config field, so the invariant
// (activation window must cover the poll interval with headroom) can't
// silently drift. See the ActivationWindow method comment.
type WorkflowsConfig struct {
	// StagingPollInterval — how often MonitorWorkflow polls STAGING
	// fixtures via the API. Distinct from ActiveFixturePollInterval
	// (that's for LIVE fixtures on every cycle). Staging polling is
	// amortized across the interval: each staging fixture gets one
	// API poll per interval regardless of how many active-poll
	// cycles fire in between (per-fixture skip via LastPolledAt).
	// See decisions.md 2026-07-07 staging-poll design.
	//
	// This is the SOURCE OF TRUTH for the activation window — the
	// derived value gets computed via ActivationWindow() below.
	//
	// NOT YET WIRED — 15-min staging poll is designed but not shipped
	// as of 2026-07-09. Value lives here so the wire-up commit doesn't
	// need another config refactor.
	StagingPollInterval time.Duration `env:"WORKFLOWS_STAGING_POLL_INTERVAL" envDefault:"15m"`

	// ActivationMultiplier — the multiplier used to derive ActivationWindow
	// from StagingPollInterval. Default 2 gives one-full-interval of
	// headroom on either side of a kickoff that lands between polls.
	//
	// Values < 2 risk missing activation for kickoffs that fall in an
	// interval boundary. Values > 2 are safe but eat activate-earlier
	// than necessary, which can affect frontend "upcoming fixtures"
	// display timing.
	ActivationMultiplier int `env:"WORKFLOWS_ACTIVATION_MULTIPLIER" envDefault:"2"`

	// ActiveFixturePollInterval — how often MonitorWorkflow's Temporal
	// schedule fires. Each cycle polls ACTIVE fixtures via /fixtures?ids=
	// batch call and runs PreActivateUpcoming (DB-only). Also the base
	// interval on top of which the 15-min staging poll layers.
	//
	// Wired at cmd/worker/main.go into the Monitor schedule's Intervals.
	ActiveFixturePollInterval time.Duration `env:"WORKFLOWS_ACTIVE_FIXTURE_POLL_INTERVAL" envDefault:"30s"`

	// RetentionDays — completed fixtures older than this get pruned
	// by IngestWorkflow's PruneOldFixtures activity. Zero = skip
	// prune (manual triggers set this to zero explicitly; scheduled
	// invocation uses the default). Mirrors Python's
	// FIXTURE_RETENTION_DAYS (archive/src/workflows/ingest_workflow.py:40).
	RetentionDays int `env:"WORKFLOWS_RETENTION_DAYS" envDefault:"14"`
}

// ActivationWindow returns the derived kickoff-lookahead used to
// promote imminent staging fixtures to active. Applied at BOTH:
//
//  1. IngestWorkflow's CategorizeAndUpsertFixtures — if a fetched
//     fixture's kickoff is within ActivationWindow of the anchor
//     time (or already-live per APIStatus), it lands in `active`
//     directly, never staging.
//  2. MonitorWorkflow's PreActivateUpcoming (DB-only, every 30s
//     cycle) — any staging fixture whose kickoff is within
//     ActivationWindow of now gets promoted to active.
//
// Method-style so callers can't accidentally set it to a value that
// violates the ActivationMultiplier × StagingPollInterval relationship.
// Tune StagingPollInterval or ActivationMultiplier if you need to
// change it.
func (c WorkflowsConfig) ActivationWindow() time.Duration {
	m := c.ActivationMultiplier
	if m < 1 {
		m = 1
	}
	return time.Duration(m) * c.StagingPollInterval
}
