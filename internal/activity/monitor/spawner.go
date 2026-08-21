// DownstreamSpawner interface + a Temporal-client-backed
// implementation. Monitor's ReconcileFixture activity depends on the
// interface (not the concrete Temporal client) so unit tests can inject a
// recording fake without spinning a Temporal server. FF-025 also makes this
// boundary the owner of conservative stale-running execution recovery.
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
	"sync"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
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
	// Nil error means the workflow was newly started, confirmed active or
	// successful, or conservatively recovered from a proven stale run.
	SpawnEvent(ctx context.Context, workflowID string, in discoverycontract.EventWorkflowInput) error
}

// TemporalSpawner is the production DownstreamSpawner backed by the Temporal
// client wrapper. StartTimeout bounds one spawn/recovery inspection call;
// EventWorkflow execution time remains governed by its finite search loop and
// activity timeouts.
type TemporalSpawner struct {
	Client       workflowStarter
	StartTimeout time.Duration
	StaleAfter   time.Duration

	mu           sync.Mutex
	observations map[string]workflowObservation
	now          func() time.Time
}

type workflowObservation struct {
	runID                string
	historyLength        int64
	stateTransitionCount int64
	progressAt           time.Time
}

// workflowStarter is the Temporal client subset event spawning needs. The
// production adapter satisfies it; the narrow port lets tests inspect the
// exact reuse and timeout contract without a Temporal server.
type workflowStarter interface {
	TaskQueue() string
	StartWorkflow(context.Context, client.StartWorkflowOptions, string, ...interface{}) (client.WorkflowRun, error)
	DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
	TerminateWorkflow(context.Context, string, string, string, ...interface{}) error
}

const defaultEventWorkflowStaleAfter = 30 * time.Minute

// NewTemporalSpawner constructs a TemporalSpawner. StartTimeout defaults to
// 10s if zero. staleAfter defaults to 30m and means "no observable Temporal
// progress for this whole interval", not total workflow runtime.
func NewTemporalSpawner(c workflowStarter, startTimeout, staleAfter time.Duration) *TemporalSpawner {
	if startTimeout <= 0 {
		startTimeout = 10 * time.Second
	}
	if staleAfter <= 0 {
		staleAfter = defaultEventWorkflowStaleAfter
	}
	return &TemporalSpawner{
		Client: c, StartTimeout: startTimeout, StaleAfter: staleAfter,
		observations: make(map[string]workflowObservation), now: time.Now,
	}
}

// ConservativeEventStaleAfter derives the no-progress bound from the two
// operator-tunable waits that can legitimately make an EventWorkflow history
// quiet. The 30m floor also exceeds every fixed candidate activity retry chain
// and the legacy 10m VideoWorkflow child timeout.
func ConservativeEventStaleAfter(attemptSpacing, queryTimeout time.Duration) time.Duration {
	staleAfter := defaultEventWorkflowStaleAfter
	if timerBound := 2 * attemptSpacing; timerBound > staleAfter {
		staleAfter = timerBound
	}
	// Pre-FF-061 histories can still have four SearchTweets activity attempts.
	// Five extra minutes cover that bounded retry chain plus scheduling variance;
	// current one-attempt histories remain inside the same conservative bound.
	if searchBound := 4*queryTimeout + 5*time.Minute; searchBound > staleAfter {
		staleAfter = searchBound
	}
	return staleAfter
}

// SpawnEvent calls the Temporal client's StartWorkflow with failed-only reuse.
// A running or successfully completed execution is still duplicate-rejected,
// while a failed, timed-out, canceled, or terminated execution can restart and
// finish the existing downstream checklist row. On duplicate-running starts,
// FF-025 describes the exact run and requires two snapshots proving no history
// or state-transition progress for StaleAfter. A newer activity heartbeat also
// resets the clock. Only then may that exact run be terminated and re-driven;
// the checklist is never force-completed.
func (s *TemporalSpawner) SpawnEvent(ctx context.Context, workflowID string, in discoverycontract.EventWorkflowInput) error {
	callCtx, cancel := context.WithTimeout(ctx, s.StartTimeout)
	defer cancel()

	opts := s.startOptions(workflowID)
	_, err := s.Client.StartWorkflow(callCtx, opts, "EventWorkflow", in)
	if err == nil {
		s.forget(workflowID)
		return nil
	}

	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	if !errors.As(err, &alreadyStarted) {
		return fmt.Errorf("monitor.SpawnEvent: %w", err)
	}

	desc, err := s.Client.DescribeWorkflowExecution(callCtx, workflowID, "")
	if err != nil {
		return fmt.Errorf("monitor.SpawnEvent: describe duplicate %s: %w", workflowID, err)
	}
	info := desc.GetWorkflowExecutionInfo()
	if info == nil || info.GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		s.forget(workflowID)
		return nil
	}
	if !s.provenStale(workflowID, desc) {
		return nil
	}

	runID := info.GetExecution().GetRunId()
	reason := fmt.Sprintf("FF-025: no Temporal progress for %s", s.StaleAfter)
	if err := s.Client.TerminateWorkflow(callCtx, workflowID, runID, reason); err != nil {
		// Exact-run termination fails closed if the run completed or changed
		// between Describe and Terminate. A later monitor cycle re-evaluates.
		return fmt.Errorf("monitor.SpawnEvent: terminate stale run %s/%s: %w", workflowID, runID, err)
	}
	s.forget(workflowID)

	_, err = s.Client.StartWorkflow(callCtx, opts, "EventWorkflow", in)
	if err != nil {
		// Another worker may have won the terminate→restart race. The same
		// failed-only identity makes that a successful recovery outcome.
		var raced *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &raced) {
			return nil
		}
		return fmt.Errorf("monitor.SpawnEvent: restart stale %s: %w", workflowID, err)
	}
	return nil
}

func (s *TemporalSpawner) startOptions(workflowID string) client.StartWorkflowOptions {
	return client.StartWorkflowOptions{
		ID:                    workflowID,
		TaskQueue:             s.Client.TaskQueue(),
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}
}

// provenStale records one exact-run progress snapshot and returns true only
// after the same history/state counters have remained unchanged for the full
// bound. Execution age is checked independently, so a first observation can
// never terminate a young run. Heartbeats are progress even though Temporal
// does not append each one to workflow history.
func (s *TemporalSpawner) provenStale(
	workflowID string,
	desc *workflowservice.DescribeWorkflowExecutionResponse,
) bool {
	info := desc.GetWorkflowExecutionInfo()
	if info == nil || info.GetExecution() == nil || info.GetStartTime() == nil {
		return false
	}
	runID := info.GetExecution().GetRunId()
	if runID == "" {
		return false
	}
	now := s.now()
	if now.Before(info.GetStartTime().AsTime()) {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now, workflowID)

	current := workflowObservation{
		runID: runID, historyLength: info.GetHistoryLength(),
		stateTransitionCount: info.GetStateTransitionCount(), progressAt: now,
	}
	previous, exists := s.observations[workflowID]
	if !exists || previous.runID != current.runID ||
		previous.historyLength != current.historyLength ||
		previous.stateTransitionCount != current.stateTransitionCount {
		s.observations[workflowID] = current
		return false
	}

	progressAt := previous.progressAt
	for _, pending := range desc.GetPendingActivities() {
		if heartbeat := pending.GetLastHeartbeatTime(); heartbeat != nil {
			heartbeatAt := heartbeat.AsTime()
			if heartbeatAt.After(progressAt) && !heartbeatAt.After(now) {
				progressAt = heartbeatAt
			}
		}
	}
	if progressAt.After(previous.progressAt) {
		previous.progressAt = progressAt
		s.observations[workflowID] = previous
	}

	runAge := now.Sub(info.GetStartTime().AsTime())
	quietFor := now.Sub(progressAt)
	return runAge >= s.StaleAfter && quietFor >= s.StaleAfter
}

func (s *TemporalSpawner) forget(workflowID string) {
	s.mu.Lock()
	delete(s.observations, workflowID)
	s.mu.Unlock()
}

func (s *TemporalSpawner) pruneLocked(now time.Time, currentWorkflowID string) {
	retention := 4 * s.StaleAfter
	for workflowID, observation := range s.observations {
		if workflowID == currentWorkflowID {
			continue
		}
		if now.Sub(observation.progressAt) > retention {
			delete(s.observations, workflowID)
		}
	}
}
