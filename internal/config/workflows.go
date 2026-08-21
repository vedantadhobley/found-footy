// WorkflowsConfig — env-driven settings shared across IngestWorkflow +
// ActivePollWorkflow + StagingPollWorkflow orchestration. Values that
// used to live as `const default*` in the workflow files (fragile —
// the same value declared twice across ingest.go + monitor.go was one
// accidental edit away from silent divergence) now live here and are
// threaded through each workflow's Activities struct at cmd/worker
// startup.
package config

import "time"

// WorkflowsConfig owns cross-workflow orchestration settings.
//
// Design note (2026-07-10): the earlier StagingPollInterval +
// ActivationMultiplier coupling is gone. It was a workaround for
// having active + staging polling crammed into one workflow with a
// bucket-math hack. Now the two responsibilities are two independent
// workflows on independent Temporal Schedules; the config surface
// collapses to what each one actually needs.
type WorkflowsConfig struct {
	// ActiveFixturePollInterval — how often ActivePollWorkflow's
	// Temporal schedule fires. Each cycle runs PreActivateUpcoming
	// (DB-only) + polls ACTIVE fixtures via /fixtures?ids= batch.
	// Cron doesn't support sub-minute resolution, so this is wired as
	// an IntervalSpec on the schedule.
	ActiveFixturePollInterval time.Duration `env:"WORKFLOWS_ACTIVE_FIXTURE_POLL_INTERVAL" envDefault:"30s"`

	// StagingPollCron — when StagingPollWorkflow fires. Default
	// `*/15 * * * *` = every 15 min at :00 :15 :30 :45. Because
	// StagingPollWorkflow is its own Temporal Schedule, operators can
	// tune this at runtime with `temporal schedule update
	// staging-poll-scheduled --cron "..."` — no redeploy needed.
	// On worker restart the code re-Creates the schedule with this
	// cron, but Create is idempotent (ErrScheduleAlreadyRunning →
	// success) and does NOT overwrite runtime updates.
	StagingPollCron string `env:"WORKFLOWS_STAGING_POLL_CRON" envDefault:"*/15 * * * *"`

	// TwitterMaintenanceCron runs the fixture-independent authentication and
	// DOM canary against the static fallback browser. Four checks per day keep
	// the shared session active and bound selector-regression detection without
	// keeping any per-event Firefox instance warm.
	TwitterMaintenanceCron string `env:"WORKFLOWS_TWITTER_MAINTENANCE_CRON" envDefault:"17 */6 * * *"`

	// ActivationWindow — kickoff-lookahead used to promote imminent
	// staging fixtures to active. Three callers:
	//
	//  1. IngestWorkflow's CategorizeAndUpsertFixtures — if a fetched
	//     fixture's kickoff is within this of the anchor (or already
	//     Live() per APIStatus), land it in `active` directly.
	//  2. ActivePollWorkflow's ActivateUpcoming (DB-only, every 30s) —
	//     any staging fixture with kickoff <= now + ActivationWindow
	//     gets promoted. This is the STANDARD activation path.
	//  3. StagingPollWorkflow's PollStagingFixtures (API-driven, every
	//     15 min) — for the "vendor pushed corrected kickoff into
	//     window" edge case.
	//
	// Default 5 min. Sized as a buffer against vendor kickoff-time
	// drift (real matches can start 1-5 min early/late vs the
	// scheduled kickoff). Matches that start >5 min earlier than
	// scheduled are essentially impossible in professional football
	// (TV schedules constrain), but the staging poll's Live()
	// emergency path catches those anyway.
	//
	// Historical note: this used to default to 30m, sized as a
	// safety margin for a 15-min staging poll cadence. That coupling
	// is gone since ActivateUpcoming now runs at 30s cadence — see
	// decisions.md 2026-07-10 workflow-split entry.
	ActivationWindow time.Duration `env:"WORKFLOWS_ACTIVATION_WINDOW" envDefault:"5m"`

	// RetentionDays — completed fixtures older than this get pruned
	// by IngestWorkflow's PruneOldFixtures activity. Zero = skip
	// prune (manual triggers set this to zero explicitly; scheduled
	// invocation uses the default). Mirrors Python's
	// FIXTURE_RETENTION_DAYS (archive/src/workflows/ingest_workflow.py:40).
	RetentionDays int `env:"WORKFLOWS_RETENTION_DAYS" envDefault:"14"`
}
