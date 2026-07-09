// monitor.go — the 30-second MonitorWorkflow. Fires per cycle via
// Temporal Schedule (registered in cmd/worker/main.go). Orchestrates
// four activities in internal/activity/monitor:
//   1. PreActivateUpcoming — staging fixtures with imminent kickoff
//      get promoted to active before we poll.
//   2. ListActiveFixtureIDs — cheap ID pull for the batched API call.
//   3. FetchLiveFixtures — one activity call regardless of ID count;
//      the apifootball client chunks internally at IDsBatchLimit and
//      fires per-chunk HTTP calls in parallel via goroutines. Partial
//      failures surface as FailedIDs — Monitor logs them and lets the
//      next 30s cycle naturally re-request (the poll IS the retry).
//   4. ReconcileFixture — per fixture, refresh row + diff events +
//      vote presence/absence. Concurrent across fixtures via
//      workflow.ExecuteActivity in a loop (dispatched in parallel;
//      workflow waits for all).
//
// Deferred to O3:
//   • DiscoveryWorkflow spawn for stable events (currently just
//     logged via EventsBecameStable in the output).
//   • Destroy pipeline (Temporal cancel + video_shares soft-delete)
//     for removed events (currently just logged via EventsRemoved).
//   • Fixture completion transition (needs Discovery to define
//     "fully done").
package workflow

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/vedantadhobley/found-footy/internal/activity/monitor"
)

// MonitorWorkflowInput carries per-cycle overrides. All fields
// optional; zero defaults match the scheduled invocation.
type MonitorWorkflowInput struct {
	// ActivationWindow — staging fixtures with kickoff within this
	// window get pre-activated. Zero → 30 minutes.
	ActivationWindow time.Duration
}

// MonitorWorkflowOutput carries counts + surfaced errors for the
// cycle. The schedule doesn't consume this; the Temporal UI + log
// aggregation do.
type MonitorWorkflowOutput struct {
	StagingActivated   int
	ActiveFixtureCount int
	FetchedCount       int
	// MissedIDs — IDs the client-side chunk fetch didn't get back
	// this cycle. Not retried in-workflow; the next 30s poll picks
	// them up naturally. Surfaced for observability.
	MissedIDs          int
	NewEvents          int
	EventsBecameStable []string
	EventsRemoved      []string
	Errors             []string
}

// MonitorWorkflow — the coordinator. Called every 30s by the
// Temporal Schedule.
func MonitorWorkflow(ctx workflow.Context, in MonitorWorkflowInput) (MonitorWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	out := MonitorWorkflowOutput{}

	// Default activity options — individual steps may override.
	baseAO := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    10 * time.Second,
			MaximumAttempts:    2,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, baseAO)

	// Resolve activation window: caller override wins, else read from
	// config via GetMonitorConfig activity (workflows can't touch env
	// directly per Temporal determinism). Same 30-min value that
	// IngestWorkflow uses; both sourced from config.Workflows.
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
	logger.Info("MonitorWorkflow cycle started", "workflow_id", workflowID)

	// ── Step 1: PreActivateUpcoming ──
	var preActivateOut monitor.PreActivateUpcomingOutput
	if err := workflow.ExecuteActivity(ctx, "PreActivateUpcoming",
		monitor.PreActivateUpcomingInput{Lookahead: activationWindow},
	).Get(ctx, &preActivateOut); err != nil {
		// Not fatal — log and continue to the active-fixtures path.
		logger.Warn("PreActivateUpcoming failed; continuing", "error", err)
		out.Errors = append(out.Errors, "PreActivateUpcoming: "+err.Error())
	}
	out.StagingActivated = preActivateOut.Activated
	out.Errors = append(out.Errors, preActivateOut.Errors...)

	// ── Step 2: ListActiveFixtureIDs ──
	var listOut monitor.ListActiveFixtureIDsOutput
	if err := workflow.ExecuteActivity(ctx, "ListActiveFixtureIDs").Get(ctx, &listOut); err != nil {
		return out, err
	}
	out.ActiveFixtureCount = len(listOut.IDs)

	if len(listOut.IDs) == 0 {
		logger.Info("MonitorWorkflow: no active fixtures", "staging_activated", out.StagingActivated)
		return out, nil
	}

	// ── Step 3: FetchLiveFixtures ──
	// One activity call regardless of ID count. The apifootball client
	// chunks internally at IDsBatchLimit and fires parallel HTTP calls.
	// Partial failures come back as FailedIDs — we log the count and
	// let the next 30s poll pick them up.
	var fetchOut monitor.FetchLiveFixturesOutput
	if err := workflow.ExecuteActivity(ctx, "FetchLiveFixtures",
		monitor.FetchLiveFixturesInput{IDs: listOut.IDs},
	).Get(ctx, &fetchOut); err != nil {
		// Catastrophic (all chunks failed / ctx cancelled) — log and
		// exit. Next cycle retries the whole set.
		logger.Warn("FetchLiveFixtures failed catastrophically", "error", err)
		out.Errors = append(out.Errors, "FetchLiveFixtures: "+err.Error())
		return out, nil
	}
	out.FetchedCount = len(fetchOut.Fixtures)
	out.MissedIDs = len(fetchOut.FailedIDs)
	if out.MissedIDs > 0 {
		logger.Warn("FetchLiveFixtures partial: missed IDs will retry next cycle",
			"missed", out.MissedIDs, "fetched", out.FetchedCount)
	}

	if len(fetchOut.Fixtures) == 0 {
		logger.Warn("MonitorWorkflow: fetched zero fixtures despite active IDs",
			"active_ids_count", out.ActiveFixtureCount,
			"missed_ids", out.MissedIDs)
		return out, nil
	}

	// ── Step 4: ReconcileFixture per fixture, concurrent ──
	reconcileFutures := make([]workflow.Future, 0, len(fetchOut.Fixtures))
	// Longer per-fixture timeout — reconcile does multiple pg ops per
	// event.
	reconcileCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 45 * time.Second,
		RetryPolicy:         baseAO.RetryPolicy,
	})
	for _, apiFix := range fetchOut.Fixtures {
		reconcileFutures = append(reconcileFutures,
			workflow.ExecuteActivity(reconcileCtx, "ReconcileFixture",
				monitor.ReconcileFixtureInput{
					APIFixture: apiFix,
					WorkflowID: workflowID,
				}))
	}
	for _, f := range reconcileFutures {
		var reconcileOut monitor.ReconcileFixtureOutput
		if err := f.Get(ctx, &reconcileOut); err != nil {
			logger.Warn("ReconcileFixture failed", "error", err)
			out.Errors = append(out.Errors, "ReconcileFixture: "+err.Error())
			continue
		}
		out.NewEvents += reconcileOut.NewEventsDetected
		out.EventsBecameStable = append(out.EventsBecameStable, reconcileOut.EventsBecameStable...)
		out.EventsRemoved = append(out.EventsRemoved, reconcileOut.EventsRemoved...)
		out.Errors = append(out.Errors, reconcileOut.Errors...)
	}

	logger.Info("MonitorWorkflow cycle complete",
		"active", out.ActiveFixtureCount,
		"fetched", out.FetchedCount,
		"missed", out.MissedIDs,
		"new_events", out.NewEvents,
		"stable", len(out.EventsBecameStable),
		"removed", len(out.EventsRemoved),
		"errors", len(out.Errors),
	)
	return out, nil
}

