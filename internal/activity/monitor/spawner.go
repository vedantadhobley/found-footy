// DownstreamSpawner interface + a Temporal-client-backed
// implementation. Monitor's ReconcileFixture activity depends on the
// interface (not the concrete Temporal client) so unit tests can
// inject a recording fake without spinning a Temporal server.
//
// Per decisions.md 2026-07-16 "Downstream workflow spawn via
// Temporal-direct + register-on-flip", every downstream spawn is
// bundled with the same activity that inserts the
// event_downstream_workflows row. This spawner is the "spawn" half
// of that bundle; the row insert lives on event.Repo.
// RegisterDownstreamWorkflow.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	temporaladapter "github.com/vedantadhobley/found-footy/internal/infra/temporal"
)

// DownstreamSpawner spawns downstream workflows for events that have
// crossed to downstream_triggered=true. Every method is idempotent
// under Temporal's RejectDuplicate reuse policy — an activity retry
// after a partial-success crash sees WorkflowExecutionAlreadyStarted,
// which the impl swallows so the activity finishes cleanly on retry.
type DownstreamSpawner interface {
	// SpawnEvent starts a EventWorkflow with the given
	// deterministic workflow_id ("event-{event_id}") and input.
	// Nil error means either the workflow was newly started OR was
	// already running (RejectDuplicate + already-started swallowed);
	// both count as success.
	SpawnEvent(ctx context.Context, workflowID string, in discoveryactivity.EventWorkflowInput) error
}

// TemporalSpawner is the production DownstreamSpawner backed by the
// Temporal client wrapper. Bundles the per-spawn timeout so callers
// don't need to reason about it at the call site.
type TemporalSpawner struct {
	Client       *temporaladapter.Client
	StartTimeout time.Duration
}

// NewTemporalSpawner constructs a TemporalSpawner. StartTimeout
// defaults to 10s if zero — enough for a healthy Temporal round trip,
// short enough that a wedged server surfaces quickly.
func NewTemporalSpawner(c *temporaladapter.Client, startTimeout time.Duration) *TemporalSpawner {
	if startTimeout == 0 {
		startTimeout = 10 * time.Second
	}
	return &TemporalSpawner{Client: c, StartTimeout: startTimeout}
}

// SpawnEvent calls the Temporal client's StartWorkflow with
// RejectDuplicate reuse policy so a retry-after-partial-success
// crash surfaces WorkflowExecutionAlreadyStarted (swallowed) rather
// than starting a second run. Uses the client's default task queue.
func (s *TemporalSpawner) SpawnEvent(ctx context.Context, workflowID string, in discoveryactivity.EventWorkflowInput) error {
	callCtx, cancel := context.WithTimeout(ctx, s.StartTimeout)
	defer cancel()

	opts := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.Client.TaskQueue(),
		WorkflowIDReusePolicy:    3, // enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE
		WorkflowExecutionTimeout: 30 * time.Minute,
	}

	_, err := s.Client.StartWorkflow(callCtx, opts, "EventWorkflow", in)
	if err != nil {
		// Retry-after-partial-success: previous attempt got past
		// ExecuteWorkflow but not past activity-return. Swallow so the
		// activity completes cleanly on retry.
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) {
			return nil
		}
		return fmt.Errorf("monitor.SpawnEvent: %w", err)
	}
	return nil
}
