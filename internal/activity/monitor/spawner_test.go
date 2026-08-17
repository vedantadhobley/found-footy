// spawner_test.go — unit coverage for EventWorkflow start identity, failed-run
// recovery policy, and duplicate/error classification without a Temporal server.
package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
)

type fakeWorkflowStarter struct {
	taskQueue       string
	options         client.StartWorkflowOptions
	workflowType    string
	args            []interface{}
	err             error
	startErrors     []error
	startCalls      int
	description     *workflowservice.DescribeWorkflowExecutionResponse
	describeErr     error
	terminateErr    error
	terminatedID    string
	terminatedRunID string
	terminateReason string
	terminateCalls  int
}

func (f *fakeWorkflowStarter) TaskQueue() string { return f.taskQueue }

func (f *fakeWorkflowStarter) StartWorkflow(
	_ context.Context,
	options client.StartWorkflowOptions,
	workflowType string,
	args ...interface{},
) (client.WorkflowRun, error) {
	f.startCalls++
	f.options = options
	f.workflowType = workflowType
	f.args = args
	if len(f.startErrors) >= f.startCalls {
		return nil, f.startErrors[f.startCalls-1]
	}
	return nil, f.err
}

func (f *fakeWorkflowStarter) DescribeWorkflowExecution(
	_ context.Context,
	_, _ string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return f.description, f.describeErr
}

func (f *fakeWorkflowStarter) TerminateWorkflow(
	_ context.Context,
	workflowID, runID, reason string,
	_ ...interface{},
) error {
	f.terminateCalls++
	f.terminatedID, f.terminatedRunID, f.terminateReason = workflowID, runID, reason
	return f.terminateErr
}

func TestTemporalSpawner_UsesFailedOnlyReuseWithoutExecutionTimeout(t *testing.T) {
	starter := &fakeWorkflowStarter{taskQueue: "found-footy"}
	spawner := NewTemporalSpawner(starter, time.Second, 0)
	in := discoveryactivity.EventWorkflowInput{FixtureID: 123}

	if err := spawner.SpawnEvent(context.Background(), "event-abc", in); err != nil {
		t.Fatalf("SpawnEvent: %v", err)
	}
	if starter.options.ID != "event-abc" || starter.options.TaskQueue != "found-footy" {
		t.Errorf("start identity = (%q, %q), want (event-abc, found-footy)",
			starter.options.ID, starter.options.TaskQueue)
	}
	if starter.options.WorkflowIDReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY {
		t.Errorf("reuse policy = %s, want ALLOW_DUPLICATE_FAILED_ONLY", starter.options.WorkflowIDReusePolicy)
	}
	if starter.options.WorkflowExecutionTimeout != 0 || starter.options.WorkflowRunTimeout != 0 {
		t.Errorf("workflow timeouts = execution %s/run %s, want unbounded",
			starter.options.WorkflowExecutionTimeout, starter.options.WorkflowRunTimeout)
	}
	if starter.workflowType != "EventWorkflow" || len(starter.args) != 1 {
		t.Errorf("start call = %q args=%d, want EventWorkflow with one input", starter.workflowType, len(starter.args))
	}
}

func TestTemporalSpawner_DuplicateRunningOrSuccessfulIsIdempotent(t *testing.T) {
	starter := &fakeWorkflowStarter{
		taskQueue: "found-footy",
		err:       &serviceerror.WorkflowExecutionAlreadyStarted{},
	}
	starter.description = runningDescription("event-abc", "run-1", time.Now(), 10, 10, time.Time{})
	spawner := NewTemporalSpawner(starter, time.Second, time.Hour)

	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err != nil {
		t.Fatalf("duplicate start should be swallowed: %v", err)
	}
}

func TestTemporalSpawner_PropagatesStartFailure(t *testing.T) {
	starter := &fakeWorkflowStarter{taskQueue: "found-footy", err: errors.New("temporal unavailable")}
	spawner := NewTemporalSpawner(starter, time.Second, 0)

	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err == nil {
		t.Fatal("non-duplicate start failure should propagate")
	}
}

func TestConservativeEventStaleAfterTracksTunableQuietPeriods(t *testing.T) {
	if got := ConservativeEventStaleAfter(time.Minute, 2*time.Minute); got != 30*time.Minute {
		t.Errorf("default-shaped bound = %s, want 30m", got)
	}
	if got := ConservativeEventStaleAfter(time.Hour, 2*time.Minute); got != 2*time.Hour {
		t.Errorf("long timer bound = %s, want 2h", got)
	}
	if got := ConservativeEventStaleAfter(time.Minute, 10*time.Minute); got != 45*time.Minute {
		t.Errorf("long query bound = %s, want 45m", got)
	}
}

func TestTemporalSpawner_StaleRunRequiresTwoUnchangedSnapshots(t *testing.T) {
	start := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	now := start.Add(time.Minute)
	duplicate := &serviceerror.WorkflowExecutionAlreadyStarted{}
	starter := &fakeWorkflowStarter{
		taskQueue:   "found-footy",
		startErrors: []error{duplicate, duplicate, nil},
		description: runningDescription("event-abc", "run-stale", start, 20, 30, time.Time{}),
	}
	spawner := NewTemporalSpawner(starter, time.Second, 30*time.Minute)
	spawner.now = func() time.Time { return now }

	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err != nil {
		t.Fatalf("first observation: %v", err)
	}
	if starter.terminateCalls != 0 {
		t.Fatal("first observation terminated the run")
	}

	now = start.Add(32 * time.Minute)
	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err != nil {
		t.Fatalf("stale recovery: %v", err)
	}
	if starter.terminateCalls != 1 {
		t.Fatalf("terminate calls = %d, want 1", starter.terminateCalls)
	}
	if starter.terminatedID != "event-abc" || starter.terminatedRunID != "run-stale" {
		t.Errorf("terminated %s/%s, want event-abc/run-stale", starter.terminatedID, starter.terminatedRunID)
	}
	if starter.startCalls != 3 {
		t.Errorf("start calls = %d, want duplicate + duplicate + replacement", starter.startCalls)
	}
	if starter.options.WorkflowIDReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY {
		t.Errorf("replacement reuse policy = %s", starter.options.WorkflowIDReusePolicy)
	}
}

func TestTemporalSpawner_HistoryProgressResetsStaleClock(t *testing.T) {
	start := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	now := start.Add(time.Minute)
	duplicate := &serviceerror.WorkflowExecutionAlreadyStarted{}
	starter := &fakeWorkflowStarter{
		taskQueue: "found-footy", err: duplicate,
		description: runningDescription("event-abc", "run-1", start, 20, 30, time.Time{}),
	}
	spawner := NewTemporalSpawner(starter, time.Second, 30*time.Minute)
	spawner.now = func() time.Time { return now }
	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err != nil {
		t.Fatal(err)
	}

	now = start.Add(32 * time.Minute)
	starter.description = runningDescription("event-abc", "run-1", start, 21, 31, time.Time{})
	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err != nil {
		t.Fatal(err)
	}
	if starter.terminateCalls != 0 {
		t.Fatal("history progress did not reset the stale clock")
	}
}

func TestTemporalSpawner_RecentHeartbeatResetsStaleClock(t *testing.T) {
	start := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	now := start.Add(time.Minute)
	duplicate := &serviceerror.WorkflowExecutionAlreadyStarted{}
	starter := &fakeWorkflowStarter{
		taskQueue: "found-footy", err: duplicate,
		description: runningDescription("event-abc", "run-1", start, 20, 30, time.Time{}),
	}
	spawner := NewTemporalSpawner(starter, time.Second, 30*time.Minute)
	spawner.now = func() time.Time { return now }
	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err != nil {
		t.Fatal(err)
	}

	now = start.Add(32 * time.Minute)
	starter.description = runningDescription("event-abc", "run-1", start, 20, 30, start.Add(31*time.Minute))
	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err != nil {
		t.Fatal(err)
	}
	if starter.terminateCalls != 0 {
		t.Fatal("recent activity heartbeat did not reset the stale clock")
	}
}

func TestTemporalSpawner_DescribeFailureFailsClosed(t *testing.T) {
	starter := &fakeWorkflowStarter{
		taskQueue:   "found-footy",
		err:         &serviceerror.WorkflowExecutionAlreadyStarted{},
		describeErr: errors.New("temporal unavailable"),
	}
	spawner := NewTemporalSpawner(starter, time.Second, time.Minute)

	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err == nil {
		t.Fatal("describe failure should propagate")
	}
	if starter.terminateCalls != 0 {
		t.Fatal("describe failure terminated a run")
	}
}

func TestTemporalSpawner_TerminationRaceDoesNotRestart(t *testing.T) {
	start := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	now := start.Add(time.Minute)
	duplicate := &serviceerror.WorkflowExecutionAlreadyStarted{}
	starter := &fakeWorkflowStarter{
		taskQueue: "found-footy", err: duplicate,
		description:  runningDescription("event-abc", "run-1", start, 20, 30, time.Time{}),
		terminateErr: errors.New("run already closed"),
	}
	spawner := NewTemporalSpawner(starter, time.Second, 30*time.Minute)
	spawner.now = func() time.Time { return now }
	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err != nil {
		t.Fatal(err)
	}
	now = start.Add(32 * time.Minute)
	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err == nil {
		t.Fatal("termination race should surface without restarting")
	}
	if starter.startCalls != 2 {
		t.Errorf("start calls = %d, want only the two duplicate probes", starter.startCalls)
	}
}

func runningDescription(
	workflowID, runID string,
	start time.Time,
	historyLength, stateTransitions int64,
	heartbeat time.Time,
) *workflowservice.DescribeWorkflowExecutionResponse {
	response := &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Execution:            &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: runID},
			StartTime:            timestamppb.New(start),
			Status:               enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
			HistoryLength:        historyLength,
			StateTransitionCount: stateTransitions,
		},
	}
	if !heartbeat.IsZero() {
		response.PendingActivities = []*workflowpb.PendingActivityInfo{{
			LastHeartbeatTime: timestamppb.New(heartbeat),
		}}
	}
	return response
}
