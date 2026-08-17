// active_poll.go — the 30-second ActivePollWorkflow. Fires per cycle
// via Temporal Schedule (registered in cmd/worker/main.go). Handles
// the active-fixture hot path — everything that has to happen fast
// enough to catch live match events. Staging-fixture polling lives in
// a separate workflow (StagingPollWorkflow) on a slower cadence, so
// this one stays lean.
//
// Steps (all in internal/activity/monitor):
//  1. ActivateUpcoming — DB-only. Staging fixtures with STORED
//     kickoff inside the activation window (default 5 min) get
//     promoted to active. Runs here at 30s cadence for failure
//     isolation: activation is P0 and inherits the resilient hot-path
//     cadence rather than being tied to the slower StagingPoll.
//  2. ListActiveFixtureIDs — cheap ID pull. Includes anything step
//     1 just activated.
//  3. FetchLiveFixtures — one activity call regardless of ID count;
//     the apifootball client chunks internally at IDsBatchLimit and
//     fires per-chunk HTTP calls in parallel via goroutines. Partial
//     failures surface as FailedIDs — we log them and let the next
//     30s cycle naturally re-request (the poll IS the retry).
//  4. ReconcileFixture — per fixture, refresh row + diff events +
//     vote presence/absence. Concurrent across fixtures via
//     workflow.ExecuteActivity in a loop (dispatched in parallel;
//     workflow waits for all).
//
// As-built (O3+):
//   - EventWorkflow spawn for stable events — SHIPPED (DownstreamSpawner
//     on the downstream_triggered flip).
//   - Fixture completion transition — SHIPPED (FixtureReadyToComplete; the
//     completion contract).
//   - VAR destroy pipeline (Temporal cancel + revoke shares + reclaim Garage
//     objects) for overturned events — SHIPPED (#172; Step 4.5 runs it for each
//     event that just debounced to 0).
//
// See decisions.md 2026-07-10 workflow-split entry for the rationale
// behind splitting Monitor into ActivePoll + StagingPoll.
package workflow

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	fleetactivity "github.com/vedantadhobley/found-footy/internal/activity/fleet"
	livefeedactivity "github.com/vedantadhobley/found-footy/internal/activity/livefeed"
	"github.com/vedantadhobley/found-footy/internal/activity/monitor"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
)

// ActivePollWorkflowInput carries per-cycle overrides. All fields
// optional; zero defaults match the scheduled invocation.
type ActivePollWorkflowInput struct {
	// ActivationWindow — staging fixtures with kickoff within this
	// window get activated by step 1. Zero → read from config
	// (default 5 min per WORKFLOWS_ACTIVATION_WINDOW).
	ActivationWindow time.Duration
}

// ActivePollWorkflowOutput carries counts + surfaced errors for the
// cycle. The schedule doesn't consume this; the Temporal UI + log
// aggregation do.
type ActivePollWorkflowOutput struct {
	// Activated — number of fixtures promoted from staging to active
	// by step 1's DB check this cycle.
	Activated int
	// ActiveFixtureCount — total active fixtures polled this cycle
	// (includes anything Activated flipped just now).
	ActiveFixtureCount int
	FetchedCount       int
	// MissedIDs — IDs the client-side chunk fetch didn't get back this
	// cycle. Not retried in-workflow; the next 30s poll picks them up
	// naturally. Surfaced for observability.
	MissedIDs          int
	NewEvents          int
	EventsBecameStable []string
	EventsRemoved      []string
	// GoalAbsencesHeld are provider-array omissions protected from VAR
	// classification because the score still requires those goals.
	GoalAbsencesHeld []string
	UnknownDropped   int // unknown-scorer placeholders hard-deleted this cycle
	Errors           []string
}

// ActivePollWorkflow — the coordinator for the 30s hot path. Called
// by the Temporal Schedule `active-poll-scheduled`.
func ActivePollWorkflow(ctx workflow.Context, in ActivePollWorkflowInput) (ActivePollWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	out := ActivePollWorkflowOutput{}

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
	// config via GetMonitorConfig (workflows can't touch env directly
	// per Temporal determinism).
	// Read monitor config once at cycle start (trivial in-process activity).
	// Caller override for ActivationWindow still wins; FleetEnabled (#160)
	// always comes from config and is needed later at the provision/release
	// steps, so cfgOut is function-scoped rather than block-local.
	var cfgOut monitor.GetMonitorConfigOutput
	if err := workflow.ExecuteActivity(ctx,
		"GetMonitorConfig",
		monitor.GetMonitorConfigInput{},
	).Get(ctx, &cfgOut); err != nil {
		return out, fmt.Errorf("read monitor config: %w", err)
	}
	activationWindow := in.ActivationWindow
	if activationWindow == 0 {
		activationWindow = cfgOut.ActivationWindow
	}

	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	logger.Info("ActivePollWorkflow cycle started", "workflow_id", workflowID)

	// ── Step 1: ActivateUpcoming ──
	// DB-only kickoff-window check. Cheap. Owns the standard activation
	// path (Path 2) — see decisions.md 2026-07-10 workflow-split entry
	// for why activation lives in the fast-cadence workflow.
	var activateOut monitor.ActivateUpcomingOutput
	if err := workflow.ExecuteActivity(ctx, "ActivateUpcoming",
		monitor.ActivateUpcomingInput{Lookahead: activationWindow},
	).Get(ctx, &activateOut); err != nil {
		// Not fatal — log and continue to the active-fixtures path.
		logger.Warn("ActivateUpcoming failed; continuing", "error", err)
		out.Errors = append(out.Errors, "ActivateUpcoming: "+err.Error())
	}
	out.Activated = activateOut.Activated
	out.Errors = append(out.Errors, activateOut.Errors...)

	// ── Step 2: ListActiveFixtureIDs ──
	var listOut monitor.ListActiveFixtureIDsOutput
	if err := workflow.ExecuteActivity(ctx, "ListActiveFixtureIDs").Get(ctx, &listOut); err != nil {
		return out, err
	}
	out.ActiveFixtureCount = len(listOut.IDs)

	if len(listOut.IDs) == 0 {
		logger.Info("ActivePollWorkflow: no active fixtures", "activated", out.Activated)
		return out, nil
	}

	// ── Step 3: FetchLiveFixtures ──
	// One activity call regardless of ID count. The apifootball client
	// chunks internally at IDsBatchLimit and fires parallel HTTP calls.
	// Partial failures come back as FailedIDs — we log the count and
	// let the next 30s poll pick them up.
	var fetchOut monitor.FetchLiveFixturesOutput
	if err := workflow.ExecuteActivity(ctx, "FetchLiveFixtures",
		monitor.FetchLiveFixturesInput(listOut),
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
		logger.Warn("ActivePollWorkflow: fetched zero fixtures despite active IDs",
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
	var removedEventIDs []uuid.UUID
	var newNamedEventIDs []uuid.UUID
	// N5: partition this cycle's reconciles into the two disjoint fixture
	// subjects — structural wins (a structural fixture rides its fresh clock in
	// its full refetch), so a fixture is never in both.
	var clockEntries []livefeedactivity.FixtureClockEntry
	var updateIDs []int64
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
		out.GoalAbsencesHeld = append(out.GoalAbsencesHeld, reconcileOut.GoalAbsencesHeld...)
		removedEventIDs = append(removedEventIDs, reconcileOut.EventsRemovedIDs...)
		newNamedEventIDs = append(newNamedEventIDs, reconcileOut.NewNamedEventIDs...)
		out.UnknownDropped += reconcileOut.UnknownDropped
		out.Errors = append(out.Errors, reconcileOut.Errors...)
		if len(reconcileOut.GoalAbsencesHeld) > 0 {
			logger.Warn("goal absence held: aggregate score requires omitted event",
				"fixture_id", reconcileOut.FixtureID,
				"natural_keys", reconcileOut.GoalAbsencesHeld)
		}

		switch {
		case reconcileOut.Structural:
			updateIDs = append(updateIDs, reconcileOut.FixtureID)
		case reconcileOut.ClockChanged:
			clockEntries = append(clockEntries, livefeedactivity.FixtureClockEntry{
				FixtureID: reconcileOut.FixtureID,
				Minute:    reconcileOut.Minute,
				Extra:     reconcileOut.Extra,
			})
		}
	}

	// ── Step 4.4: warm a Firefox instance per newly-detected named event
	// (#160). Provision is create+start only (fast); the container warms in
	// the background during the ~60-90s debounce window so it is ready when
	// the event triggers at count=3 and the EventWorkflow begins searching.
	// No-op when the fleet is disabled (nil Fleet). Best-effort — a failed
	// provision is survivable: when the EventWorkflow later searches the
	// (absent) per-event instance, the twitter client's transport-error
	// fallback retries the shared service once (twitter/client.go, audit
	// P0-5), so the goal still gets its search.
	if cfgOut.FleetEnabled && len(newNamedEventIDs) > 0 {
		provCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 30 * time.Second,
			RetryPolicy:         baseAO.RetryPolicy,
		})
		for _, evID := range newNamedEventIDs {
			if err := workflow.ExecuteActivity(provCtx, "ProvisionFirefox",
				fleetactivity.ProvisionFirefoxInput{EventID: evID}).Get(provCtx, nil); err != nil {
				logger.Warn("ProvisionFirefox failed", "event_id", evID.String(), "error", err)
				out.Errors = append(out.Errors, "ProvisionFirefox: "+err.Error())
			}
		}
	}

	// ── Step 4.5: VAR destroy (#172) ──
	// A confirmed event that just debounced to 0 was overturned. Cancel its
	// in-flight discovery FIRST — so nothing mints a new clip after the teardown
	// (closes the revoke↔promote race) — then DestroyEvent revokes its shares
	// (→ the redirect 410s, clips stop serving) + reclaims its Garage objects.
	// Idempotent + best-effort: a completed event workflow cancels to not-found
	// (fine), and a failed DestroyEvent is retried by the activity retry policy.
	if len(removedEventIDs) > 0 {
		destroyCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 2 * time.Minute,
			RetryPolicy:         baseAO.RetryPolicy,
		})
		for _, evID := range removedEventIDs {
			_ = workflow.RequestCancelExternalWorkflow(ctx, "event-"+evID.String(), "").Get(ctx, nil)
			if err := workflow.ExecuteActivity(destroyCtx, "DestroyEvent",
				videoactivity.DestroyEventInput{EventID: evID}).Get(destroyCtx, nil); err != nil {
				logger.Warn("DestroyEvent failed", "event_id", evID.String(), "error", err)
				out.Errors = append(out.Errors, "DestroyEvent: "+err.Error())
			}
			// #160: release the event's Firefox instance. Covers both the
			// pre-trigger decay (provisioned at count=1, never spawned an
			// EventWorkflow) and the post-trigger VAR (its EventWorkflow was
			// cancelled above, so finalizeEvent won't run to self-release).
			// Idempotent. Guarded so nothing fires when the fleet is off.
			if cfgOut.FleetEnabled {
				if err := workflow.ExecuteActivity(destroyCtx, "ReleaseFirefox",
					fleetactivity.ReleaseFirefoxInput{EventID: evID}).Get(destroyCtx, nil); err != nil {
					logger.Warn("ReleaseFirefox failed (reaper will sweep)", "event_id", evID.String(), "error", err)
					out.Errors = append(out.Errors, "ReleaseFirefox: "+err.Error())
				}
			}
		}
	}

	// ── Step 5: live-feed batch emit (N5) ──
	// One PublishFixtureBatch per cycle: fixture.clock (inline ticks) +
	// fixture.update (ids to bulk-refetch). Best-effort — a lost batch heals on
	// the consumer's next window refetch. Activation (staging→active) is not
	// emitted here: the kickoff status-flip is captured as Structural on the
	// fixture's first live reconcile, and it rides the window fetch meanwhile.
	if len(clockEntries) > 0 || len(updateIDs) > 0 {
		emitCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 15 * time.Second,
			RetryPolicy:         baseAO.RetryPolicy,
		})
		if err := workflow.ExecuteActivity(emitCtx, "PublishFixtureBatch",
			livefeedactivity.FixtureBatchInput{Clock: clockEntries, UpdateIDs: updateIDs}).Get(emitCtx, nil); err != nil {
			logger.Warn("PublishFixtureBatch failed (heals on refetch)", "error", err)
			out.Errors = append(out.Errors, "PublishFixtureBatch: "+err.Error())
		}
	}

	logger.Info("ActivePollWorkflow cycle complete",
		"activated", out.Activated,
		"active", out.ActiveFixtureCount,
		"fetched", out.FetchedCount,
		"missed", out.MissedIDs,
		"new_events", out.NewEvents,
		"stable", len(out.EventsBecameStable),
		"removed", len(out.EventsRemoved),
		"unknown_dropped", out.UnknownDropped,
		"errors", len(out.Errors),
	)
	return out, nil
}
