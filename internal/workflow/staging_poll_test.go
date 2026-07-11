// staging_poll_test.go — WorkflowTestSuite tests for StagingPollWorkflow.
// Tests the coordinator only — activity-level unit tests live in
// internal/activity/monitor/activities_test.go.
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"github.com/vedantadhobley/found-footy/internal/activity/monitor"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

func newStagingPollEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflow.StagingPollWorkflow)
	env.RegisterActivity(&monitor.Activities{})
	env.OnActivity("GetMonitorConfig", mock.Anything, mock.Anything).
		Return(monitor.GetMonitorConfigOutput{
			ActivationWindow: 5 * time.Minute,
		}, nil).Maybe()
	return env
}

// TestStagingPollWorkflow_HappyPath — activity returns non-zero
// counts; workflow propagates them into the output.
func TestStagingPollWorkflow_HappyPath(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newStagingPollEnv(&s)

	env.OnActivity("PollStagingFixtures", mock.Anything,
		mock.MatchedBy(func(in monitor.PollStagingFixturesInput) bool {
			return in.ActivationWindow == 5*time.Minute
		})).Return(monitor.PollStagingFixturesOutput{
		Considered:         5,
		Polled:             5,
		EmergencyActivated: 1,
		KickoffActivated:   1,
	}, nil).Once()

	env.ExecuteWorkflow(workflow.StagingPollWorkflow, workflow.StagingPollWorkflowInput{})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out workflow.StagingPollWorkflowOutput
	env.GetWorkflowResult(&out)
	if out.Considered != 5 {
		t.Errorf("Considered = %d, want 5", out.Considered)
	}
	if out.Polled != 5 {
		t.Errorf("Polled = %d, want 5", out.Polled)
	}
	if out.EmergencyActivated != 1 {
		t.Errorf("EmergencyActivated = %d, want 1", out.EmergencyActivated)
	}
	if out.KickoffActivated != 1 {
		t.Errorf("KickoffActivated = %d, want 1", out.KickoffActivated)
	}
	env.AssertExpectations(t)
}

// TestStagingPollWorkflow_ActivityFailure_Completes — activity fails
// (all retries exhausted). Workflow catches, logs, and completes with
// the error surfaced in Errors. Doesn't cascade to a workflow-level
// failure — next tick will retry.
func TestStagingPollWorkflow_ActivityFailure_Completes(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newStagingPollEnv(&s)

	env.OnActivity("PollStagingFixtures", mock.Anything, mock.Anything).
		Return(monitor.PollStagingFixturesOutput{}, errors.New("vendor 500")).Times(2)

	env.ExecuteWorkflow(workflow.StagingPollWorkflow, workflow.StagingPollWorkflowInput{})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow should complete despite activity failure: %v", err)
	}
	var out workflow.StagingPollWorkflowOutput
	env.GetWorkflowResult(&out)
	if len(out.Errors) == 0 {
		t.Error("expected an error surfaced from PollStagingFixtures")
	}
	env.AssertExpectations(t)
}

// TestStagingPollWorkflow_ActivationWindow_UsesDefault — empty input
// resolves to 5-min default via GetMonitorConfig.
func TestStagingPollWorkflow_ActivationWindow_UsesDefault(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newStagingPollEnv(&s)

	env.OnActivity("PollStagingFixtures", mock.Anything,
		mock.MatchedBy(func(in monitor.PollStagingFixturesInput) bool {
			return in.ActivationWindow == 5*time.Minute
		})).Return(monitor.PollStagingFixturesOutput{}, nil).Once()

	env.ExecuteWorkflow(workflow.StagingPollWorkflow, workflow.StagingPollWorkflowInput{})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}
