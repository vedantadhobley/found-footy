// ingest_test.go — WorkflowTestSuite-based tests for IngestWorkflow.
// Uses testify/mock to swap activity implementations so the workflow
// runs in-process (no worker, no Temporal server, no DB).
//
// What we're testing: the workflow's control flow — activity call
// order, output aggregation, conditional skipping (empty TeamRefs
// skips alias work), durable media-retention dispatch,
// error propagation. NOT testing the activity logic itself — that's
// covered by internal/activity/ingest/activities_test.go.
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"

	"github.com/vedantadhobley/found-footy/internal/activity/ingest"
	retentionactivity "github.com/vedantadhobley/found-footy/internal/activity/retention"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

// mkFixtures returns n APIFixtures with distinct IDs (1..n). Needed
// because the workflow deduplicates fixtures by ID across per-day
// activity calls — a bare `make([]APIFixture, n)` gives all zero IDs
// which collapse to one after dedup.
func mkFixtures(n int) []apifootball.APIFixture {
	out := make([]apifootball.APIFixture, n)
	for i := range out {
		out[i].Fixture.ID = int64(i + 1)
	}
	return out
}

// newEnv sets up a test workflow environment with the workflow and
// ingest activities registered. Individual tests then attach
// OnActivity mocks before ExecuteWorkflow.
func newEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflow.IngestWorkflow)
	// Register with zero-value deps — mocks intercept, real methods
	// never execute.
	env.RegisterActivity(&ingest.Activities{})
	env.RegisterActivity(&retentionactivity.Activities{})
	// Default no-op mocks for the two new "always-fires" activities so
	// individual tests don't have to register them explicitly. Tests
	// that DO care can override via a later OnActivity(...).
	env.OnActivity("RefreshTrackedTeamsIfStale", mock.Anything, mock.Anything).
		Return(ingest.RefreshTrackedTeamsIfStaleOutput{}, nil).Maybe()
	env.OnActivity("GetIngestConfig", mock.Anything, mock.Anything).
		Return(ingest.GetIngestConfigOutput{
			MaxLookaheadDays:      30,
			ActivationWindow:      30 * time.Minute,
			CompletedFixtureDates: 14,
		}, nil).Maybe()
	// FetchFixturesForDay is NOT defaulted — each test registers it
	// explicitly so `.Once()` / count assertions actually enforce.
	return env
}

// expectEmptyRetention supplies the always-run media-retention plan for tests
// whose focus is elsewhere.
func expectEmptyRetention(env *testsuite.TestWorkflowEnvironment) {
	env.OnActivity("PlanMediaRetention", mock.Anything, mock.Anything).
		Return(retentionactivity.PlanMediaRetentionOutput{}, nil).Once()
}

// stdInput — reasonable defaults matching the new plan §5 W1 shape;
// individual tests override fields they care about. Anchor date is
// supplied via ManualDate for determinism (the TestWorkflowEnvironment's
// clock is otherwise nondeterministic across CI runs).
func stdInput(now time.Time) workflow.IngestWorkflowInput {
	return workflow.IngestWorkflowInput{
		ManualDate:       &now,
		ActivationWindow: 30 * time.Minute,
	}
}

func TestIngestWorkflowInput_DecodesLegacyRetentionDays(t *testing.T) {
	payloads, err := converter.GetDefaultDataConverter().ToPayloads(map[string]any{
		"FetchFuture":   true,
		"RetentionDays": 14,
	})
	if err != nil {
		t.Fatalf("encode legacy input: %v", err)
	}
	var got workflow.IngestWorkflowInput
	if err := converter.GetDefaultDataConverter().FromPayloads(payloads, &got); err != nil {
		t.Fatalf("decode legacy input: %v", err)
	}
	if !got.FetchFuture {
		t.Fatal("known legacy field was not decoded")
	}
}

// TestIngestWorkflow_HappyPath — all four activities fire, output
// aggregates counts correctly.
func TestIngestWorkflow_HappyPath(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)

	env.OnActivity("FetchFixturesForDay", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesForDayOutput{
			Fixtures: mkFixtures(5), // 5 fixtures, distinct IDs for dedup
			Count:    5,
		}, nil).Once()

	env.OnActivity("CategorizeAndUpsertFixtures", mock.Anything, mock.Anything).
		Return(ingest.CategorizeOutput{
			Staging: 3, Active: 1, Completed: 1,
			TeamRefs: []ingest.TeamRef{
				{TeamID: 40, TeamName: "Liverpool"},
				{TeamID: 42, TeamName: "Arsenal"},
			},
		}, nil).Once()

	env.OnActivity("EnsureAliasPlaceholders", mock.Anything, mock.Anything).
		Return(ingest.EnsureAliasPlaceholdersOutput{Existing: 1, Inserted: 1}, nil).Once()

	expectEmptyRetention(env)

	env.ExecuteWorkflow(workflow.IngestWorkflow, stdInput(time.Date(2026, 7, 8, 0, 5, 0, 0, time.UTC)))

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out workflow.IngestWorkflowOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	// Every count landed correctly.
	if out.Fetched != 5 {
		t.Errorf("Fetched = %d, want 5", out.Fetched)
	}
	if out.Staging != 3 || out.Active != 1 || out.Completed != 1 {
		t.Errorf("categorize counts = %+v", out)
	}
	if out.ExistingAliases != 1 || out.InsertedAliases != 1 {
		t.Errorf("alias counts = %+v", out)
	}
	if out.ReclaimedEvents != 0 {
		t.Errorf("ReclaimedEvents = %d, want 0", out.ReclaimedEvents)
	}
	env.AssertExpectations(t)
}

// TestIngestWorkflow_Retention_ReclaimsMediaWithoutDeletingHistory verifies
// that the planner supplies asset-bearing events and the workflow reclaims
// their objects with the policy reason. SQL fixture/event history is outside
// this workflow's deletion surface.
func TestIngestWorkflow_Retention_ReclaimsClipBearingEvents(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)
	// DestroyEvent lives on PersistActivities — register so the env knows
	// its typed signature, then stub it and capture the reasons passed.
	env.RegisterActivity(&videoactivity.PersistActivities{})

	env.OnActivity("FetchFixturesForDay", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesForDayOutput{Fixtures: mkFixtures(2), Count: 2}, nil).Once()
	// No TeamRefs → alias steps skip; keeps the test focused on retention.
	env.OnActivity("CategorizeAndUpsertFixtures", mock.Anything, mock.Anything).
		Return(ingest.CategorizeOutput{Completed: 2}, nil).Once()

	e1, e2 := uuid.New(), uuid.New()
	cutoff := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	env.OnActivity("PlanMediaRetention", mock.Anything, mock.Anything).
		Return(retentionactivity.PlanMediaRetentionOutput{
			Cutoff:   &cutoff,
			EventIDs: []uuid.UUID{e1, e2},
		}, nil).Once()

	var reasons []string
	env.OnActivity("DestroyEvent", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(videoactivity.DestroyEventInput)
			reasons = append(reasons, in.Reason)
		}).Return(nil).Times(2)

	env.ExecuteWorkflow(workflow.IngestWorkflow, stdInput(time.Date(2026, 7, 8, 0, 5, 0, 0, time.UTC)))

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out workflow.IngestWorkflowOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if out.ReclaimedEvents != 2 {
		t.Errorf("ReclaimedEvents = %d, want 2", out.ReclaimedEvents)
	}
	if len(reasons) != 2 {
		t.Fatalf("DestroyEvent called %d times, want 2", len(reasons))
	}
	for i, r := range reasons {
		if r != string(dvideo.RemovalPolicy) {
			t.Errorf("DestroyEvent[%d] reason = %q, want %q (retention, not var)", i, r, dvideo.RemovalPolicy)
		}
	}
	env.AssertExpectations(t)
}

// TestIngestWorkflow_NoTeamRefs_SkipsAliasStep — empty categorize
// output.TeamRefs means alias step should not fire.
func TestIngestWorkflow_NoTeamRefs_SkipsAliasStep(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)

	env.OnActivity("FetchFixturesForDay", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesForDayOutput{Count: 0}, nil).Once()

	env.OnActivity("CategorizeAndUpsertFixtures", mock.Anything, mock.Anything).
		Return(ingest.CategorizeOutput{TeamRefs: nil}, nil).Once()

	// EnsureAliasPlaceholders NOT expected to fire.
	// (env.OnActivity with .Times(0) would enforce; the absence of a
	// registration means testsuite will error if it's called.)

	expectEmptyRetention(env)

	env.ExecuteWorkflow(workflow.IngestWorkflow, stdInput(time.Date(2026, 7, 8, 0, 5, 0, 0, time.UTC)))

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}

// TestIngestWorkflow_FetchFails_AbortsEarly — an activity error on
// the fetch step propagates as workflow error; downstream activities
// don't run.
func TestIngestWorkflow_FetchFails_AbortsEarly(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)

	env.OnActivity("FetchFixturesForDay", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesForDayOutput{}, errors.New("api-sports.io down")).Times(3)
	// No Categorize, Alias, or Retention work is expected — fetch failure aborts.

	env.ExecuteWorkflow(workflow.IngestWorkflow, stdInput(time.Date(2026, 7, 8, 0, 5, 0, 0, time.UTC)))

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete (should complete with error)")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error from fetch failure, got nil")
	}
	env.AssertExpectations(t)
}

// TestIngestWorkflow_CategorizeFails_PropagatesError — fetch
// succeeds but categorize fails; workflow errors out.
func TestIngestWorkflow_CategorizeFails_PropagatesError(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)

	env.OnActivity("FetchFixturesForDay", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesForDayOutput{Count: 2, Fixtures: mkFixtures(2)}, nil).Once()

	env.OnActivity("CategorizeAndUpsertFixtures", mock.Anything, mock.Anything).
		Return(ingest.CategorizeOutput{}, errors.New("pg pool exhausted")).Times(3)
	// Alias and retention work are not expected.

	env.ExecuteWorkflow(workflow.IngestWorkflow, stdInput(time.Date(2026, 7, 8, 0, 5, 0, 0, time.UTC)))

	if env.GetWorkflowError() == nil {
		t.Fatal("expected error from categorize failure")
	}
	env.AssertExpectations(t)
}

// TestIngestWorkflow_ManualFixtureIDs_UsesByIDsPath — populating
// ManualFixtureIDs must switch the fetch step from
// FetchFixturesForDay → FetchFixturesByIDs. Verified by only
// registering the byIDs mock; if the workflow dispatched to the
// window activity instead, the test would panic (unregistered).
func TestIngestWorkflow_ManualFixtureIDs_UsesByIDsPath(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)

	env.OnActivity("FetchFixturesByIDs", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesByIDsOutput{
			Fixtures:  mkFixtures(2),
			Count:     2,
			FailedIDs: nil, // clean fetch — no targeted-retry loop
		}, nil).Once()
	// FetchFixturesForDay NOT registered — should not be called.

	env.OnActivity("CategorizeAndUpsertFixtures", mock.Anything, mock.Anything).
		Return(ingest.CategorizeOutput{Staging: 2, TeamRefs: []ingest.TeamRef{{TeamID: 40}}}, nil).Once()
	env.OnActivity("EnsureAliasPlaceholders", mock.Anything, mock.Anything).
		Return(ingest.EnsureAliasPlaceholdersOutput{Inserted: 1}, nil).Once()
	expectEmptyRetention(env)

	in := stdInput(time.Date(2026, 7, 8, 0, 5, 0, 0, time.UTC))
	in.ManualFixtureIDs = []int64{1_515_514, 1_515_515}

	env.ExecuteWorkflow(workflow.IngestWorkflow, in)

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}

// TestIngestWorkflow_EmptyInput_UsesDefaults — scheduled invocation
// passes empty input; workflow must self-configure with anchor =
// workflow.Now and activity-provided policy values. The schedule itself does
// not freeze environment policy in serialized input.
func TestIngestWorkflow_EmptyInput_UsesDefaults(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)

	env.OnActivity("FetchFixturesForDay", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesForDayOutput{Count: 0}, nil).Once()
	env.OnActivity("CategorizeAndUpsertFixtures", mock.Anything, mock.Anything).
		Return(ingest.CategorizeOutput{TeamRefs: nil}, nil).Once()
	// Alias is skipped because TeamRefs is nil; retention still runs.
	expectEmptyRetention(env)

	env.ExecuteWorkflow(workflow.IngestWorkflow, workflow.IngestWorkflowInput{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}
