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

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
)

// DownstreamSpawner spawns downstream workflows for events that have
// crossed to downstream_triggered=true. Every method is idempotent
// under Temporal's failed-only reuse policy — an activity retry while
// an execution is running or after success sees
// WorkflowExecutionAlreadyStarted, which the impl swallows so the
// activity finishes cleanly. A closed unsuccessful execution may restart.
type DownstreamSpawner interface {
	// SpawnEvent starts a EventWorkflow with the given
	// deterministic workflow_id ("event-{event_id}") and input.
	// Nil error means either the workflow was newly started OR was
	// already running or successfully completed (duplicate-start swallowed);
	// both count as success.
	SpawnEvent(ctx context.Context, workflowID string, in discoveryactivity.EventWorkflowInput) error
}

// TemporalSpawner is the production DownstreamSpawner backed by the
// Temporal client wrapper. It bounds only the client RPC that requests a
// start; EventWorkflow execution time is governed by its finite search loop
// and activity timeouts.
type TemporalSpawner struct {
	Client       workflowStarter
	StartTimeout time.Duration
}

// workflowStarter is the Temporal client subset event spawning needs. The
// production adapter satisfies it; the narrow port lets tests inspect the
// exact reuse and timeout contract without a Temporal server.
type workflowStarter interface {
	TaskQueue() string
	StartWorkflow(context.Context, client.StartWorkflowOptions, string, ...interface{}) (client.WorkflowRun, error)
}

// NewTemporalSpawner constructs a TemporalSpawner. StartTimeout
// defaults to 10s if zero — enough for a healthy Temporal round trip,
// short enough that a wedged server surfaces quickly.
func NewTemporalSpawner(c workflowStarter, startTimeout time.Duration) *TemporalSpawner {
	if startTimeout == 0 {
		startTimeout = 10 * time.Second
	}
	return &TemporalSpawner{Client: c, StartTimeout: startTimeout}
}

// SpawnEvent calls the Temporal client's StartWorkflow with failed-only reuse.
// A running or successfully completed execution is still duplicate-rejected,
// while a failed, timed-out, canceled, or terminated execution can restart and
// finish the existing downstream checklist row. EventWorkflow owns its own
// bounded attempt loop and activity timeouts, so no arbitrary execution timeout
// truncates legitimate queued video work.
func (s *TemporalSpawner) SpawnEvent(ctx context.Context, workflowID string, in discoveryactivity.EventWorkflowInput) error {
	callCtx, cancel := context.WithTimeout(ctx, s.StartTimeout)
	defer cancel()

	opts := client.StartWorkflowOptions{
		ID:                    workflowID,
		TaskQueue:             s.Client.TaskQueue(),
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
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
