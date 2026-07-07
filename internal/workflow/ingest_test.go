// ingest_test.go — WorkflowTestSuite-based tests for IngestWorkflow.
// Uses testify/mock to swap activity implementations so the workflow
// runs in-process (no worker, no Temporal server, no DB).
//
// What we're testing: the workflow's control flow — activity call
// order, output aggregation, conditional skipping (empty TeamRefs
// skips alias step; zero RetentionThreshold skips prune step),
// error propagation. NOT testing the activity logic itself — that's
// covered by internal/activity/ingest/activities_test.go.
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"github.com/vedantadhobley/found-footy/internal/activity/ingest"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

// newEnv sets up a test workflow environment with the workflow and
// ingest activities registered. Individual tests then attach
// OnActivity mocks before ExecuteWorkflow.
func newEnv(s *testsuite.WorkflowTestSuite) *testsuite.TestWorkflowEnvironment {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflow.IngestWorkflow)
	// Register with zero-value deps — mocks intercept, real methods
	// never execute.
	env.RegisterActivity(&ingest.Activities{})
	return env
}

// stdInput — reasonable defaults; individual tests override fields
// they care about.
func stdInput(now time.Time) workflow.IngestWorkflowInput {
	return workflow.IngestWorkflowInput{
		FetchWindowFrom:    now.Add(-24 * time.Hour),
		FetchWindowTo:      now.Add(72 * time.Hour),
		ActivationWindow:   30 * time.Minute,
		RetentionThreshold: now.Add(-14 * 24 * time.Hour),
	}
}

// TestIngestWorkflow_HappyPath — all four activities fire, output
// aggregates counts correctly.
func TestIngestWorkflow_HappyPath(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)

	env.OnActivity("FetchFixturesForWindow", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesOutput{
			Fixtures: make([]apifootball.APIFixture, 5), // 5 fixtures, contents don't matter
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

	env.OnActivity("PruneOldFixtures", mock.Anything, mock.Anything).
		Return(ingest.PruneOldFixturesOutput{Deleted: 3}, nil).Once()

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
	if out.PrunedFixtures != 3 {
		t.Errorf("PrunedFixtures = %d, want 3", out.PrunedFixtures)
	}
	env.AssertExpectations(t)
}

// TestIngestWorkflow_NoTeamRefs_SkipsAliasStep — empty categorize
// output.TeamRefs means alias step should not fire.
func TestIngestWorkflow_NoTeamRefs_SkipsAliasStep(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)

	env.OnActivity("FetchFixturesForWindow", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesOutput{Count: 0}, nil).Once()

	env.OnActivity("CategorizeAndUpsertFixtures", mock.Anything, mock.Anything).
		Return(ingest.CategorizeOutput{TeamRefs: nil}, nil).Once()

	// EnsureAliasPlaceholders NOT expected to fire.
	// (env.OnActivity with .Times(0) would enforce; the absence of a
	// registration means testsuite will error if it's called.)

	env.OnActivity("PruneOldFixtures", mock.Anything, mock.Anything).
		Return(ingest.PruneOldFixturesOutput{Deleted: 0}, nil).Once()

	env.ExecuteWorkflow(workflow.IngestWorkflow, stdInput(time.Date(2026, 7, 8, 0, 5, 0, 0, time.UTC)))

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	env.AssertExpectations(t)
}

// TestIngestWorkflow_ZeroRetention_SkipsPrune — zero RetentionThreshold
// short-circuits the prune step.
func TestIngestWorkflow_ZeroRetention_SkipsPrune(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)

	env.OnActivity("FetchFixturesForWindow", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesOutput{Count: 1, Fixtures: make([]apifootball.APIFixture, 1)}, nil).Once()
	env.OnActivity("CategorizeAndUpsertFixtures", mock.Anything, mock.Anything).
		Return(ingest.CategorizeOutput{Staging: 1, TeamRefs: []ingest.TeamRef{{TeamID: 40}}}, nil).Once()
	env.OnActivity("EnsureAliasPlaceholders", mock.Anything, mock.Anything).
		Return(ingest.EnsureAliasPlaceholdersOutput{Inserted: 1}, nil).Once()
	// PruneOldFixtures NOT registered — should not be called.

	in := stdInput(time.Date(2026, 7, 8, 0, 5, 0, 0, time.UTC))
	in.RetentionThreshold = time.Time{} // zero → skip prune

	env.ExecuteWorkflow(workflow.IngestWorkflow, in)

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out workflow.IngestWorkflowOutput
	env.GetWorkflowResult(&out)
	if out.PrunedFixtures != 0 {
		t.Errorf("PrunedFixtures = %d, want 0 (prune skipped)", out.PrunedFixtures)
	}
	env.AssertExpectations(t)
}

// TestIngestWorkflow_FetchFails_AbortsEarly — an activity error on
// the fetch step propagates as workflow error; downstream activities
// don't run.
func TestIngestWorkflow_FetchFails_AbortsEarly(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := newEnv(&s)

	env.OnActivity("FetchFixturesForWindow", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesOutput{}, errors.New("api-sports.io down")).Times(3)
	// No Categorize / Alias / Prune expected — fetch failure aborts.

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

	env.OnActivity("FetchFixturesForWindow", mock.Anything, mock.Anything).
		Return(ingest.FetchFixturesOutput{Count: 2, Fixtures: make([]apifootball.APIFixture, 2)}, nil).Once()

	env.OnActivity("CategorizeAndUpsertFixtures", mock.Anything, mock.Anything).
		Return(ingest.CategorizeOutput{}, errors.New("pg pool exhausted")).Times(3)
	// Alias / Prune NOT expected.

	env.ExecuteWorkflow(workflow.IngestWorkflow, stdInput(time.Date(2026, 7, 8, 0, 5, 0, 0, time.UTC)))

	if env.GetWorkflowError() == nil {
		t.Fatal("expected error from categorize failure")
	}
	env.AssertExpectations(t)
}
