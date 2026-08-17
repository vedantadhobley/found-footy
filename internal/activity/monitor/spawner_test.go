// spawner_test.go — unit coverage for EventWorkflow start identity, failed-run
// recovery policy, and duplicate/error classification without a Temporal server.
package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
)

type fakeWorkflowStarter struct {
	taskQueue    string
	options      client.StartWorkflowOptions
	workflowType string
	args         []interface{}
	err          error
}

func (f *fakeWorkflowStarter) TaskQueue() string { return f.taskQueue }

func (f *fakeWorkflowStarter) StartWorkflow(
	_ context.Context,
	options client.StartWorkflowOptions,
	workflowType string,
	args ...interface{},
) (client.WorkflowRun, error) {
	f.options = options
	f.workflowType = workflowType
	f.args = args
	return nil, f.err
}

func TestTemporalSpawner_UsesFailedOnlyReuseWithoutExecutionTimeout(t *testing.T) {
	starter := &fakeWorkflowStarter{taskQueue: "found-footy"}
	spawner := NewTemporalSpawner(starter, time.Second)
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
	spawner := NewTemporalSpawner(starter, time.Second)

	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err != nil {
		t.Fatalf("duplicate start should be swallowed: %v", err)
	}
}

func TestTemporalSpawner_PropagatesStartFailure(t *testing.T) {
	starter := &fakeWorkflowStarter{taskQueue: "found-footy", err: errors.New("temporal unavailable")}
	spawner := NewTemporalSpawner(starter, time.Second)

	if err := spawner.SpawnEvent(context.Background(), "event-abc", discoveryactivity.EventWorkflowInput{}); err == nil {
		t.Fatal("non-duplicate start failure should propagate")
	}
}
