// staging_poll.go — the StagingPollWorkflow. Fires per cron schedule
// (default `*/15 * * * *` = every 15 min at :00 :15 :30 :45). Owns
// the vendor-side edge-case handling for staging fixtures:
//
//   • Kickoff-corrected activation (Path 3a): vendor pushed a new
//     kickoff time that puts the fixture inside our activation window.
//   • Live() emergency activation (Path 3b): vendor status is already
//     Live() (1H, 2H, HT, ET, BT, P, SUSP, INT, PST) while we still
//     have the fixture in staging. Something went wrong upstream —
//     either the vendor's original kickoff was off enough that a match
//     started before we noticed, or the match started earlier than
//     scheduled, or a suspended match resumed.
//
// The STANDARD 30-min-before activation (Path 2) lives in
// ActivePollWorkflow at 30s cadence — see the failure-isolation
// argument in decisions.md 2026-07-10 workflow-split entry.
//
// Cadence is tunable at runtime via
// `temporal schedule update staging-poll-scheduled --cron ...`
// without a code redeploy — the whole point of splitting the two
// workflows onto independent Temporal Schedules.
package workflow

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	fleetactivity "github.com/vedantadhobley/found-footy/internal/activity/fleet"
	"github.com/vedantadhobley/found-footy/internal/activity/monitor"
)

// StagingPollWorkflowInput carries per-cycle overrides. All fields
// optional; zero defaults match the scheduled invocation.
type StagingPollWorkflowInput struct {
	// ActivationWindow — passed through to PollStagingFixtures'
	// kickoff-corrected activation check. Zero → read from config
	// (default 5 min per WORKFLOWS_ACTIVATION_WINDOW).
	ActivationWindow time.Duration
}

// StagingPollWorkflowOutput carries counts + surfaced errors.
type StagingPollWorkflowOutput struct {
	// Considered — raw count of staging fixtures polled this tick.
	Considered int
	// Polled — how many the vendor returned data for.
	Polled int
	// MissedIDs — per-chunk vendor failures. Retried next tick.
	MissedIDs int
	// EmergencyActivated — vendor status flipped to Live() while still
	// staging (Path 3b).
	EmergencyActivated int
	// KickoffActivated — vendor-corrected kickoff pulled fixture into
	// activation window (Path 3a).
	KickoffActivated int
	Errors           []string
}

// StagingPollWorkflow — the coordinator for the vendor-edge-case
// path. Called by the Temporal Schedule `staging-poll-scheduled`
// (default cron `*/15 * * * *`).
func StagingPollWorkflow(ctx workflow.Context, in StagingPollWorkflowInput) (StagingPollWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	out := StagingPollWorkflowOutput{}

	// Vendor polls dominate wall-clock, so give the activity a longer
	// timeout than the 30s ActivePoll steps. Retry once — if the vendor
	// is down we'd rather next tick handle it than pile up retries.
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 90 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    15 * time.Second,
			MaximumAttempts:    2,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	// Resolve activation window: caller override wins, else read from
	// config via GetMonitorConfig.
	activationWindow := in.ActivationWindow
	if activationWindow == 0 {
		var cfgOut monitor.GetMonitorConfigOutput
		if err := workflow.ExecuteActivity(ctx,
			"GetMonitorConfig",
			monitor.GetMonitorConfigInput{},
		).Get(ctx, &cfgOut); err != nil {
			return out, fmt.Errorf("read monitor config: %w", err)
		}
		activationWindow = cfgOut.ActivationWindow
	}

	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	logger.Info("StagingPollWorkflow cycle started", "workflow_id", workflowID)

	var pollOut monitor.PollStagingFixturesOutput
	if err := workflow.ExecuteActivity(ctx, "PollStagingFixtures",
		monitor.PollStagingFixturesInput{ActivationWindow: activationWindow},
	).Get(ctx, &pollOut); err != nil {
		logger.Warn("PollStagingFixtures failed", "error", err)
		out.Errors = append(out.Errors, "PollStagingFixtures: "+err.Error())
		return out, nil
	}
	out.Considered = pollOut.Considered
	out.Polled = pollOut.Polled
	out.MissedIDs = pollOut.MissedIDs
	out.EmergencyActivated = pollOut.EmergencyActivated
	out.KickoffActivated = pollOut.KickoffActivated
	out.Errors = append(out.Errors, pollOut.Errors...)

	// Periodic fleet orphan reap (#183 / audit P0-5). StagingPoll is the
	// always-on */15 cron, so it is the natural home for fleet reconciliation:
	// it sweeps crash-orphans (worker died between Provision and Release) and
	// failed-release strays that no EventWorkflow will ever clean up. The
	// activity no-ops when the fleet is disabled, so this runs unconditionally.
	// Best-effort — a sweep failure is recorded, never fatal (next tick
	// retries). Own options: modest timeout, no in-cycle retry.
	reapCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	var reapOut fleetactivity.ReapOrphanedFirefoxOutput
	if err := workflow.ExecuteActivity(reapCtx, "ReapOrphanedFirefox",
		fleetactivity.ReapOrphanedFirefoxInput{MinAgeSecs: 120},
	).Get(reapCtx, &reapOut); err != nil {
		logger.Warn("ReapOrphanedFirefox failed", "error", err)
		out.Errors = append(out.Errors, "ReapOrphanedFirefox: "+err.Error())
	} else if len(reapOut.Reaped) > 0 {
		logger.Info("reaped orphaned firefox instances",
			"count", len(reapOut.Reaped), "names", reapOut.Reaped)
	}

	logger.Info("StagingPollWorkflow cycle complete",
		"considered", out.Considered,
		"polled", out.Polled,
		"missed", out.MissedIDs,
		"emergency_activated", out.EmergencyActivated,
		"kickoff_activated", out.KickoffActivated,
		"errors", len(out.Errors),
	)
	return out, nil
}
