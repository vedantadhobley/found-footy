// monitor.go — the 30-second MonitorWorkflow. Fires per cycle via
// Temporal Schedule (registered in cmd/worker/main.go). Orchestrates
// the four activities in internal/activity/monitor:
//   1. PreActivateUpcoming — staging fixtures with imminent kickoff
//      get promoted to active before we poll.
//   2. ListActiveFixtureIDs — cheap ID pull for the batched API call.
//   3. FetchLiveFixtures — batched /fixtures?ids= call (chunked at
//      20 per API-Football's cap).
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
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/vedantadhobley/found-footy/internal/activity/monitor"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
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
	NewEvents          int
	EventsBecameStable []string
	EventsRemoved      []string
	Errors             []string
}

const (
	defaultMonitorActivationWindow = 30 * time.Minute
	// apifootball caps by-IDs at 20 per call. If we have more active
	// fixtures than this, chunk the batch.
	maxByIDsChunk = 20
)

// MonitorWorkflow — the coordinator. Called every 30s by the
// Temporal Schedule.
func MonitorWorkflow(ctx workflow.Context, in MonitorWorkflowInput) (MonitorWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	out := MonitorWorkflowOutput{}

	activationWindow := in.ActivationWindow
	if activationWindow == 0 {
		activationWindow = defaultMonitorActivationWindow
	}

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

	// ── Step 3: FetchLiveFixtures (chunked) ──
	// Chunk IDs at maxByIDsChunk (20) — API's per-call cap. Multiple
	// chunks dispatched in parallel via ExecuteActivity + Get later.
	chunks := chunkIDs(listOut.IDs, maxByIDsChunk)
	fetchFutures := make([]workflow.Future, 0, len(chunks))
	for _, chunk := range chunks {
		fetchFutures = append(fetchFutures,
			workflow.ExecuteActivity(ctx, "FetchLiveFixtures",
				monitor.FetchLiveFixturesInput{IDs: chunk}))
	}

	var allFixtures []apifootball.APIFixture
	for _, f := range fetchFutures {
		var fetchOut monitor.FetchLiveFixturesOutput
		if err := f.Get(ctx, &fetchOut); err != nil {
			logger.Warn("FetchLiveFixtures chunk failed", "error", err)
			out.Errors = append(out.Errors, "FetchLiveFixtures: "+err.Error())
			continue
		}
		allFixtures = append(allFixtures, fetchOut.Fixtures...)
	}
	out.FetchedCount = len(allFixtures)

	if len(allFixtures) == 0 {
		logger.Warn("MonitorWorkflow: fetched zero fixtures despite active IDs",
			"active_ids_count", out.ActiveFixtureCount)
		return out, nil
	}

	// ── Step 4: ReconcileFixture per fixture, concurrent ──
	reconcileFutures := make([]workflow.Future, 0, len(allFixtures))
	// Longer per-fixture timeout — reconcile does multiple pg ops per
	// event.
	reconcileCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 45 * time.Second,
		RetryPolicy:         baseAO.RetryPolicy,
	})
	for _, apiFix := range allFixtures {
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
		"new_events", out.NewEvents,
		"stable", len(out.EventsBecameStable),
		"removed", len(out.EventsRemoved),
		"errors", len(out.Errors),
	)
	return out, nil
}

// chunkIDs splits the ID slice into batches of at most n. If the
// slice is short enough, returns a single-chunk slice.
func chunkIDs(ids []int64, n int) [][]int64 {
	if len(ids) <= n {
		return [][]int64{ids}
	}
	chunks := make([][]int64, 0, (len(ids)+n-1)/n)
	for i := 0; i < len(ids); i += n {
		end := i + n
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[i:end])
	}
	return chunks
}

