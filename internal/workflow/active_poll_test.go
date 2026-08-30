// active_poll_test.go — WorkflowTestSuite tests for ActivePollWorkflow.
// Same pattern as ingest_test.go: register workflow + zero-value
// activities struct, use testify OnActivity mocks, execute, assert.
package workflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	livefeedactivity "github.com/vedantadhobley/found-footy/internal/activity/livefeed"
	"github.com/vedantadhobley/found-footy/internal/activity/monitor"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

func newActivePollEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflow.ActivePollWorkflow)
	env.RegisterActivity(&monitor.Activities{})
	// DestroyEvent (#172) is a PersistActivities method; register so the
	// VAR-destroy test can mock it by name. Empty deps — the mock overrides it.
	env.RegisterActivity(&videoactivity.PersistActivities{})
	// N5 PublishFixtureBatch — registered so tests that produce a non-empty
	// partition can mock it. Existing tests leave Structural/ClockChanged false
	// (empty partition → not called), so no default mock is needed here.
	env.RegisterActivity(&livefeedactivity.Activities{})
	// Default GetMonitorConfig — tests that don't override this get the
	// same 5-min activation window as production. Tests that pass an
	// explicit ActivePollWorkflowInput.ActivationWindow bypass this call
	// entirely.
	env.OnActivity("GetMonitorConfig", mock.Anything, mock.Anything).
		Return(monitor.GetMonitorConfigOutput{
			ActivationWindow: 5 * time.Minute,
		}, nil).Maybe()
	return env
}

// TestActivePollWorkflow_HappyPath — activation promoted 1 fixture,
// one active fixture, ReconcileFixture finds a new goal event.
func TestActivePollWorkflow_HappyPath(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newActivePollEnv(&s)

	env.OnActivity("ActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.ActivateUpcomingOutput{Considered: 2, Activated: 1}, nil).Once()

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

	env.ExecuteWorkflow(workflow.ActivePollWorkflow, workflow.ActivePollWorkflowInput{})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out workflow.ActivePollWorkflowOutput
	env.GetWorkflowResult(&out)
	if out.Activated != 1 {
		t.Errorf("Activated = %d, want 1", out.Activated)
	}
	if out.ActiveFixtureCount != 1 {
		t.Errorf("ActiveFixtureCount = %d, want 1", out.ActiveFixtureCount)
	}
	if out.NewEvents != 1 {
		t.Errorf("NewEvents = %d, want 1", out.NewEvents)
	}
	env.AssertExpectations(t)
}

func TestActivePollWorkflow_AggregatesProviderIntegrityShadowVerdicts(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newActivePollEnv(&s)

	env.OnActivity("ActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.ActivateUpcomingOutput{}, nil).Once()
	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: []int64{101, 102}}, nil).Once()
	env.OnActivity("FetchLiveFixtures", mock.Anything, mock.Anything).
		Return(monitor.FetchLiveFixturesOutput{Fixtures: []apifootball.APIFixture{
			{Fixture: apifootball.APIFixtureFixture{ID: 101}},
			{Fixture: apifootball.APIFixtureFixture{ID: 102}},
		}}, nil).Once()

	for _, fixtureID := range []int64{101, 102} {
		id := fixtureID
		env.OnActivity("ReconcileFixture", mock.Anything,
			mock.MatchedBy(func(in monitor.ReconcileFixtureInput) bool {
				return in.APIFixture.Fixture.ID == id
			})).Return(monitor.ReconcileFixtureOutput{
			FixtureID: id,
			ProviderIntegrity: providerintegrity.FixtureVerdict{
				FixtureID: id,
				Policy:    providerintegrity.PolicyPositiveOnly,
				Reasons:   []providerintegrity.Reason{providerintegrity.ReasonScoreDecreased},
			},
		}, nil).Once()
	}

	env.ExecuteWorkflow(workflow.ActivePollWorkflow, workflow.ActivePollWorkflowInput{})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out workflow.ActivePollWorkflowOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if out.ProviderIntegrity.Policy != providerintegrity.PolicyPositiveOnly ||
		out.ProviderIntegrity.RegressedFixtures != 2 {
		t.Fatalf("ProviderIntegrity = %+v, want systemic positive-only verdict", out.ProviderIntegrity)
	}
	env.AssertExpectations(t)
}

// TestActivePollWorkflow_LiveFeedPartition — N5: a structural fixture goes to
// fixture.update, a clock-only fixture to fixture.clock, disjoint, both riding
// one PublishFixtureBatch. A fixture that is BOTH structural and clock-changed
// goes to update only (structural wins).
func TestActivePollWorkflow_LiveFeedPartition(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newActivePollEnv(&s)

	env.OnActivity("ActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.ActivateUpcomingOutput{}, nil).Once()
	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: []int64{101, 102}}, nil).Once()
	env.OnActivity("FetchLiveFixtures", mock.Anything, mock.Anything).
		Return(monitor.FetchLiveFixturesOutput{
			Fixtures: []apifootball.APIFixture{
				{Fixture: apifootball.APIFixtureFixture{ID: 101}},
				{Fixture: apifootball.APIFixtureFixture{ID: 102}},
			},
		}, nil).Once()

	// 101 → structural AND clock moved (structural wins → update);
	// 102 → clock-only advance (→ clock).
	env.OnActivity("ReconcileFixture", mock.Anything,
		mock.MatchedBy(func(in monitor.ReconcileFixtureInput) bool { return in.APIFixture.Fixture.ID == 101 })).
		Return(monitor.ReconcileFixtureOutput{FixtureID: 101, Structural: true, ClockChanged: true, Minute: 47}, nil).Once()
	env.OnActivity("ReconcileFixture", mock.Anything,
		mock.MatchedBy(func(in monitor.ReconcileFixtureInput) bool { return in.APIFixture.Fixture.ID == 102 })).
		Return(monitor.ReconcileFixtureOutput{FixtureID: 102, ClockChanged: true, Minute: 62}, nil).Once()

	var captured livefeedactivity.FixtureBatchInput
	env.OnActivity("PublishFixtureBatch", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in livefeedactivity.FixtureBatchInput) error {
			captured = in
			return nil
		}).Once()

	env.ExecuteWorkflow(workflow.ActivePollWorkflow, workflow.ActivePollWorkflowInput{})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	if len(captured.UpdateIDs) != 1 || captured.UpdateIDs[0] != 101 {
		t.Errorf("UpdateIDs = %v, want [101] (structural wins)", captured.UpdateIDs)
	}
	if len(captured.Clock) != 1 || captured.Clock[0].FixtureID != 102 || captured.Clock[0].Minute != 62 {
		t.Errorf("Clock = %+v, want [{102 62}]", captured.Clock)
	}
	env.AssertExpectations(t)
}

// TestActivePollWorkflow_VARDestroy — a confirmed event that just debounced to
// 0 (VAR overturn) must be torn down: the workflow runs DestroyEvent for it
// (#172). The external-workflow cancel is best-effort (the target isn't running
// in the test env; our code ignores its error), so we assert on DestroyEvent.
func TestActivePollWorkflow_VARDestroy(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newActivePollEnv(&s)

	env.OnActivity("ActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.ActivateUpcomingOutput{}, nil).Once()
	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: []int64{101}}, nil).Once()
	env.OnActivity("FetchLiveFixtures", mock.Anything, mock.Anything).
		Return(monitor.FetchLiveFixturesOutput{
			Fixtures: []apifootball.APIFixture{{Fixture: apifootball.APIFixtureFixture{ID: 101}}},
		}, nil).Once()

	removed := uuid.New()
	env.OnActivity("ReconcileFixture", mock.Anything, mock.Anything).
		Return(monitor.ReconcileFixtureOutput{
			FixtureID:        101,
			EventsRemoved:    []string{"517_101_Goal_1"},
			EventsRemovedIDs: []uuid.UUID{removed},
		}, nil).Once()

	// The best-effort external cancel targets a workflow not running in the env;
	// mock it to a no-op so the env doesn't panic (our code ignores its result).
	env.OnRequestCancelExternalWorkflow(mock.Anything, mock.Anything, mock.Anything).Return(nil)

	destroyed := 0
	env.OnActivity("DestroyEvent", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in videoactivity.DestroyEventInput) error {
			if in.EventID == removed {
				destroyed++
			}
			return nil
		}).Once()

	env.ExecuteWorkflow(workflow.ActivePollWorkflow, workflow.ActivePollWorkflowInput{})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	if destroyed != 1 {
		t.Errorf("DestroyEvent for the overturned event called %d times, want 1", destroyed)
	}
	env.AssertExpectations(t)
}

// TestActivePollWorkflow_EmptyActive_SkipsFetchAndReconcile — no
// active fixtures returned, workflow completes without hitting the
// API or reconciling anything.
func TestActivePollWorkflow_EmptyActive_SkipsFetchAndReconcile(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newActivePollEnv(&s)

	env.OnActivity("ActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.ActivateUpcomingOutput{}, nil).Once()
	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: nil}, nil).Once()
	// FetchLiveFixtures + ReconcileFixture NOT registered — if they're
	// invoked, the test panics on unknown mock.

	env.ExecuteWorkflow(workflow.ActivePollWorkflow, workflow.ActivePollWorkflowInput{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}

// TestActivePollWorkflow_LargeBatch_SingleActivityCall — 25 active
// fixtures. Chunking moved into apifootball.ListFixturesByIDs
// (parallel goroutine fan-out inside the client), so the workflow
// dispatches ONE FetchLiveFixtures activity call regardless of input
// size. Locks in the invariant that Temporal history stays lean and
// per-chunk retry is delegated to the client.
func TestActivePollWorkflow_LargeBatch_SingleActivityCall(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newActivePollEnv(&s)

	ids := make([]int64, 25)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	env.OnActivity("ActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.ActivateUpcomingOutput{}, nil).Once()
	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: ids}, nil).Once()

	env.OnActivity("FetchLiveFixtures", mock.Anything, mock.Anything).
		Return(monitor.FetchLiveFixturesOutput{Fixtures: nil}, nil).Once()

	env.ExecuteWorkflow(workflow.ActivePollWorkflow, workflow.ActivePollWorkflowInput{})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}

// TestActivePollWorkflow_ReconcileFailure_ContinuesOthers — one
// fixture's reconcile fails; other fixtures still process; workflow
// completes without failing.
func TestActivePollWorkflow_ReconcileFailure_ContinuesOthers(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newActivePollEnv(&s)

	env.OnActivity("ActivateUpcoming", mock.Anything, mock.Anything).
		Return(monitor.ActivateUpcomingOutput{}, nil).Once()
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

	env.ExecuteWorkflow(workflow.ActivePollWorkflow, workflow.ActivePollWorkflowInput{})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow should complete despite one reconcile failure: %v", err)
	}
	var out workflow.ActivePollWorkflowOutput
	env.GetWorkflowResult(&out)
	if out.NewEvents != 1 {
		t.Errorf("NewEvents = %d, want 1 (from the succeeding fixture)", out.NewEvents)
	}
	if len(out.Errors) == 0 {
		t.Error("expected at least one error for the failing fixture")
	}
	env.AssertExpectations(t)
}

// TestActivePollWorkflow_ActivationWindow_UsesDefault — empty input
// resolves to 5-min default activation window via GetMonitorConfig.
func TestActivePollWorkflow_ActivationWindow_UsesDefault(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newActivePollEnv(&s)

	env.OnActivity("ActivateUpcoming", mock.Anything,
		mock.MatchedBy(func(in monitor.ActivateUpcomingInput) bool {
			return in.Lookahead == 5*time.Minute
		})).Return(monitor.ActivateUpcomingOutput{}, nil).Once()
	env.OnActivity("ListActiveFixtureIDs", mock.Anything).
		Return(monitor.ListActiveFixtureIDsOutput{IDs: nil}, nil).Once()

	env.ExecuteWorkflow(workflow.ActivePollWorkflow, workflow.ActivePollWorkflowInput{})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}
