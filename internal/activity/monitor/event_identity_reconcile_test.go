// Event-identity stability and replacement tests for active reconciliation.
package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// TestReconcileFixture_ReplacementGoalAllowsOldIdentityToDecay proves that a
// same-team replacement accounts for the unchanged score while the old player
// identity follows the absence path.
func TestReconcileFixture_ReplacementGoalAllowsOldIdentityToDecay(t *testing.T) {
	kickoff := time.Date(2026, 8, 16, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564802, kickoff))
	eRepo := newFakeEventRepo()

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564802, Status: apifootball.APIFixtureStatus{Short: "2h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(1), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{
			mkAPIGoal(40, 111, 30),
		},
	}
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(31*time.Minute))
	_, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("original cycle: %v", err)
	}

	replacement := apiFix
	replacement.Events = []apifootball.APIFixtureEvent{mkAPIGoal(40, 222, 30)}
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: replacement, WorkflowID: "w2"})
	if err != nil {
		t.Fatalf("replacement cycle: %v", err)
	}
	if len(out.GoalAbsencesHeld) != 0 {
		t.Fatalf("GoalAbsencesHeld = %v, want none", out.GoalAbsencesHeld)
	}
	if len(out.EventsRemoved) != 1 || out.NewEventsDetected != 1 {
		t.Fatalf("removed=%v new=%d, want one old removal and one replacement", out.EventsRemoved, out.NewEventsDetected)
	}
}

// TestReconcileFixture_BraceKeepsLaterSequenceAfterFirstGoalVAR reproduces
// audit P1-2. Removing the earlier goal must not renumber the surviving later
// goal onto the tombstoned key, and a subsequent goal must allocate above the
// complete active + removed sequence history.
func TestReconcileFixture_BraceKeepsLaterSequenceAfterFirstGoalVAR(t *testing.T) {
	kickoff := time.Date(2026, 8, 17, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564901, kickoff))
	eRepo := newFakeEventRepo()
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(61*time.Minute))

	brace := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564901, Status: apifootball.APIFixtureStatus{Short: "2h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(2), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{
			mkAPIGoal(40, 111, 30),
			mkAPIGoal(40, 111, 60),
		},
	}
	if _, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: brace, WorkflowID: "brace-1",
	}); err != nil {
		t.Fatalf("brace insert: %v", err)
	}

	firstRemoved := brace
	firstRemoved.Goals.Home = pi(1)
	firstRemoved.Events = []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 60)}
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: firstRemoved, WorkflowID: "brace-2",
	})
	if err != nil {
		t.Fatalf("first-goal VAR: %v", err)
	}
	if len(out.EventsRemoved) != 1 || out.EventsRemoved[0] != "40_111_goal_1" {
		t.Fatalf("removed keys = %v, want first goal sequence", out.EventsRemoved)
	}
	survivor, err := eRepo.GetByNaturalKey(context.Background(), 1564901, "40_111_goal_2")
	if err != nil {
		t.Fatalf("get surviving second goal: %v", err)
	}
	if survivor.Removed || survivor.Minute != 60 || survivor.DebounceCount != 2 {
		t.Fatalf("survivor = removed %v minute %d count %d, want false/60/2",
			survivor.Removed, survivor.Minute, survivor.DebounceCount)
	}

	thirdGoal := firstRemoved
	thirdGoal.Goals.Home = pi(2)
	thirdGoal.Events = []apifootball.APIFixtureEvent{
		mkAPIGoal(40, 111, 60),
		mkAPIGoal(40, 111, 80),
	}
	out, err = acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: thirdGoal, WorkflowID: "brace-3",
	})
	if err != nil {
		t.Fatalf("third goal: %v", err)
	}
	if out.NewEventsDetected != 1 {
		t.Fatalf("new events = %d, want one third goal", out.NewEventsDetected)
	}
	if _, err := eRepo.GetByNaturalKey(context.Background(), 1564901, "40_111_goal_3"); err != nil {
		t.Fatalf("third goal did not allocate sequence 3: %v", err)
	}
}

// TestReconcileFixture_BraceArrayReorderDoesNotSwapRows ensures provider array
// order is not identity. The stored first and second goals retain their clocks
// when the same response arrives in reverse order.
func TestReconcileFixture_BraceArrayReorderDoesNotSwapRows(t *testing.T) {
	kickoff := time.Date(2026, 8, 17, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564902, kickoff))
	eRepo := newFakeEventRepo()
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(61*time.Minute))

	brace := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564902, Status: apifootball.APIFixtureStatus{Short: "2h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(2), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{
			mkAPIGoal(40, 111, 30),
			mkAPIGoal(40, 111, 60),
		},
	}
	if _, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: brace, WorkflowID: "reorder-1",
	}); err != nil {
		t.Fatalf("brace insert: %v", err)
	}

	brace.Events[0], brace.Events[1] = brace.Events[1], brace.Events[0]
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: brace, WorkflowID: "reorder-2",
	})
	if err != nil {
		t.Fatalf("reordered brace: %v", err)
	}
	if out.NewEventsDetected != 0 || len(out.EventsRemoved) != 0 {
		t.Fatalf("reordered result = new %d removed %v", out.NewEventsDetected, out.EventsRemoved)
	}
	first, err := eRepo.GetByNaturalKey(context.Background(), 1564902, "40_111_goal_1")
	if err != nil {
		t.Fatalf("get first stored goal: %v", err)
	}
	second, err := eRepo.GetByNaturalKey(context.Background(), 1564902, "40_111_goal_2")
	if err != nil {
		t.Fatalf("get second stored goal: %v", err)
	}
	if first.Minute != 30 || second.Minute != 60 {
		t.Fatalf("stored clocks swapped: seq1=%d seq2=%d", first.Minute, second.Minute)
	}
}

// TestReconcileFixture_IncompleteGoalInventoryDoesNotConsumeNearbyIdentity
// protects FF-014 and brace matching together. When score proves one goal is
// omitted, a nearby same-scorer goal must be inserted rather than treated as a
// mutable-clock correction of the stored missing goal.
func TestReconcileFixture_IncompleteGoalInventoryDoesNotConsumeNearbyIdentity(t *testing.T) {
	kickoff := time.Date(2026, 8, 17, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564903, kickoff))
	eRepo := newFakeEventRepo()
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(35*time.Minute))

	first := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564903, Status: apifootball.APIFixtureStatus{Short: "1h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals:  apifootball.APIFixtureGoals{Home: pi(1), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}
	if _, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: first, WorkflowID: "incomplete-1",
	}); err != nil {
		t.Fatalf("first goal: %v", err)
	}

	omittedWithNewGoal := first
	omittedWithNewGoal.Goals.Home = pi(2)
	omittedWithNewGoal.Events = []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 34)}
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: omittedWithNewGoal, WorkflowID: "incomplete-2",
	})
	if err != nil {
		t.Fatalf("incomplete inventory: %v", err)
	}
	if out.NewEventsDetected != 1 || len(out.GoalAbsencesHeld) != 1 {
		t.Fatalf("result = new %d held %v, want one new and one held",
			out.NewEventsDetected, out.GoalAbsencesHeld)
	}
	storedFirst, err := eRepo.GetByNaturalKey(context.Background(), 1564903, "40_111_goal_1")
	if err != nil {
		t.Fatalf("get omitted stored goal: %v", err)
	}
	storedSecond, err := eRepo.GetByNaturalKey(context.Background(), 1564903, "40_111_goal_2")
	if err != nil {
		t.Fatalf("nearby goal was not assigned a new identity: %v", err)
	}
	if storedFirst.Minute != 30 || storedSecond.Minute != 34 {
		t.Fatalf("stored clocks = seq1:%d seq2:%d, want 30/34", storedFirst.Minute, storedSecond.Minute)
	}
}

// TestReconcileFixture_ClockCorrectionKeepsNaturalKey proves FF-027 retains
// the reason sequence identity existed: a small provider clock correction
// updates mutable fields on the original row instead of inserting a duplicate.
func TestReconcileFixture_ClockCorrectionKeepsNaturalKey(t *testing.T) {
	kickoff := time.Date(2026, 8, 17, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564904, kickoff))
	eRepo := newFakeEventRepo()
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(32*time.Minute))

	fixturePoll := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564904, Status: apifootball.APIFixtureStatus{Short: "1h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals:  apifootball.APIFixtureGoals{Home: pi(1), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}
	if _, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: fixturePoll, WorkflowID: "correction-1",
	}); err != nil {
		t.Fatalf("first poll: %v", err)
	}

	fixturePoll.Events = []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 31)}
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: fixturePoll, WorkflowID: "correction-2",
	})
	if err != nil {
		t.Fatalf("corrected poll: %v", err)
	}
	if out.NewEventsDetected != 0 || len(out.EventsRemoved) != 0 || !out.Structural {
		t.Fatalf("correction result = new %d removed %v structural %v",
			out.NewEventsDetected, out.EventsRemoved, out.Structural)
	}
	stored, err := eRepo.GetByNaturalKey(context.Background(), 1564904, "40_111_goal_1")
	if err != nil {
		t.Fatalf("corrected row: %v", err)
	}
	if stored.Minute != 31 {
		t.Fatalf("corrected minute = %d, want 31", stored.Minute)
	}
}
