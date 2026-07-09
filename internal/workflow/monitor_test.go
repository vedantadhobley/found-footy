// monitor_test.go — WorkflowTestSuite tests for MonitorWorkflow.
// Same pattern as ingest_test.go: register workflow + zero-value
// activities struct, use testify OnActivity mocks, execute, assert.
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"github.com/vedantadhobley/found-footy/internal/activity/monitor"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

func newMonitorEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflow.MonitorWorkflow)
	env.RegisterActivity(&monitor.Activities{})
	// Default GetMonitorConfig — tests that don't override this get the
	// same 30-min activation window as production. Tests that pass an
	// explicit MonitorWorkflowInput.ActivationWindow bypass this call
	// entirely.
	env.OnActivity("GetMonitorConfig", mock.Anything, mock.Anything).
		Return(monitor.GetMonitorConfigOutput{
			ActivationWindow:    30 * time.Minute,
			StagingPollInterval: 15 * time.Minute,
		}, nil).Maybe()
	return env
}

// TestMonitorWorkflow_HappyPath — one staging activated, one active
// fixture, ReconcileFixture finds a new goal event.
func TestMonitorWorkflow_HappyPath(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newMonitorEnv(&s)

	env.OnActivity("PreActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.PreActivateUpcomingOutput{Considered: 2, Activated: 1}, nil).Once()

	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: []int64{101}}, nil).Once()

	env.OnActivity("FetchLiveFixtures", mock.Anything, mock.Anything).
		Return(monitor.FetchLiveFixturesOutput{
			Fixtures: []apifootball.APIFixture{
				{Fixture: apifootball.APIFixtureFixture{ID: 101}},
			},
		}, nil).Once()

	env.OnActivity("ReconcileFixture", mock.Anything, mock.Anything).
		Return(monitor.ReconcileFixtureOutput{
			FixtureID:         101,
			NewEventsDetected: 1,
		}, nil).Once()

	env.ExecuteWorkflow(workflow.MonitorWorkflow, workflow.MonitorWorkflowInput{})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out workflow.MonitorWorkflowOutput
	env.GetWorkflowResult(&out)
	if out.StagingActivated != 1 {
		t.Errorf("StagingActivated = %d, want 1", out.StagingActivated)
	}
	if out.ActiveFixtureCount != 1 {
		t.Errorf("ActiveFixtureCount = %d, want 1", out.ActiveFixtureCount)
	}
	if out.NewEvents != 1 {
		t.Errorf("NewEvents = %d, want 1", out.NewEvents)
	}
	env.AssertExpectations(t)
}

// TestMonitorWorkflow_EmptyActive_SkipsFetchAndReconcile — no active
// fixtures returned, workflow completes without hitting the API or
// reconciling anything.
func TestMonitorWorkflow_EmptyActive_SkipsFetchAndReconcile(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newMonitorEnv(&s)

	env.OnActivity("PreActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.PreActivateUpcomingOutput{}, nil).Once()
	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: nil}, nil).Once()
	// FetchLiveFixtures + ReconcileFixture NOT registered — if they're
	// invoked, the test panics on unknown mock.

	env.ExecuteWorkflow(workflow.MonitorWorkflow, workflow.MonitorWorkflowInput{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}

// TestMonitorWorkflow_LargeBatch_SingleActivityCall — 25 active
// fixtures. Since chunking moved into apifootball.ListFixturesByIDs
// (parallel goroutine fan-out inside the client), the workflow now
// dispatches ONE FetchLiveFixtures activity call regardless of the
// input size. Locks in the invariant that Temporal history stays
// lean and per-chunk retry is delegated to the client.
func TestMonitorWorkflow_LargeBatch_SingleActivityCall(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newMonitorEnv(&s)

	ids := make([]int64, 25)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	env.OnActivity("PreActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.PreActivateUpcomingOutput{}, nil).Once()
	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: ids}, nil).Once()

	// Exactly one activity call — Once() enforces "no more than 1"
	// via the mock; if the workflow ever regressed to chunk-side
	// dispatching, this test would fail with unexpected extra calls.
	env.OnActivity("FetchLiveFixtures", mock.Anything, mock.Anything).
		Return(monitor.FetchLiveFixturesOutput{Fixtures: nil}, nil).Once()

	env.ExecuteWorkflow(workflow.MonitorWorkflow, workflow.MonitorWorkflowInput{})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}

// TestMonitorWorkflow_ReconcileFailure_ContinuesOthers — one
// fixture's reconcile fails; other fixtures still process; workflow
// completes without failing.
func TestMonitorWorkflow_ReconcileFailure_ContinuesOthers(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newMonitorEnv(&s)

	env.OnActivity("PreActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.PreActivateUpcomingOutput{}, nil).Once()
	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: []int64{101, 102}}, nil).Once()
	env.OnActivity("FetchLiveFixtures", mock.Anything, mock.Anything).
		Return(monitor.FetchLiveFixturesOutput{
			Fixtures: []apifootball.APIFixture{
				{Fixture: apifootball.APIFixtureFixture{ID: 101}},
				{Fixture: apifootball.APIFixtureFixture{ID: 102}},
			},
		}, nil).Once()

	// First reconcile fails.
	env.OnActivity("ReconcileFixture", mock.Anything,
		mock.MatchedBy(func(in monitor.ReconcileFixtureInput) bool {
			return in.APIFixture.Fixture.ID == 101
		})).Return(monitor.ReconcileFixtureOutput{}, errors.New("simulated failure")).Times(2)
	// Second succeeds.
	env.OnActivity("ReconcileFixture", mock.Anything,
		mock.MatchedBy(func(in monitor.ReconcileFixtureInput) bool {
			return in.APIFixture.Fixture.ID == 102
		})).Return(monitor.ReconcileFixtureOutput{
		FixtureID:         102,
		NewEventsDetected: 1,
	}, nil).Once()

	env.ExecuteWorkflow(workflow.MonitorWorkflow, workflow.MonitorWorkflowInput{})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow should complete despite one reconcile failure: %v", err)
	}
	var out workflow.MonitorWorkflowOutput
	env.GetWorkflowResult(&out)
	if out.NewEvents != 1 {
		t.Errorf("NewEvents = %d, want 1 (from the succeeding fixture)", out.NewEvents)
	}
	if len(out.Errors) == 0 {
		t.Error("expected at least one error for the failing fixture")
	}
	env.AssertExpectations(t)
}

// TestMonitorWorkflow_ActivationWindow_UsesDefault — empty input
// resolves to 30-min default activation window.
func TestMonitorWorkflow_ActivationWindow_UsesDefault(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newMonitorEnv(&s)

	env.OnActivity("PreActivateUpcoming", mock.Anything,
		mock.MatchedBy(func(in monitor.PreActivateUpcomingInput) bool {
			return in.Lookahead == 30*time.Minute
		})).Return(monitor.PreActivateUpcomingOutput{}, nil).Once()
	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: nil}, nil).Once()

	env.ExecuteWorkflow(workflow.MonitorWorkflow, workflow.MonitorWorkflowInput{})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}
