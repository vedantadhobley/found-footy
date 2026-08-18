// Active-fixture event-reconciliation activity tests.
package monitor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

func TestReconcileFixture_NewGoalInserted_CountIs1(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(30 * time.Minute)
	fRepo := newFakeFixtureRepo()
	fRepo.Upsert(context.Background(), mkActiveFixture(999, kickoff))
	eRepo := newFakeEventRepo()

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h", Long: "First Half"}},
		Events:  []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: apiFix, WorkflowID: "monitor-w1",
	})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if out.NewEventsDetected != 1 {
		t.Errorf("NewEventsDetected = %d, want 1", out.NewEventsDetected)
	}
	if len(out.EventsBecameStable) != 0 {
		t.Errorf("EventsBecameStable = %v, want empty (count is 1, not 3)", out.EventsBecameStable)
	}
	// A goal is a structural change (new event + a score move).
	if !out.Structural {
		t.Error("Structural = false, want true (a goal was inserted)")
	}
}

// ── N4 classification signals ──────────────────────────────────
//
// mkActiveN4Fixture seeds an active fixture with a known prior clock/score so
// the snapshot-diff has a concrete baseline (mkActiveFixture leaves elapsed +
// scores nil, which any poll would count as "changed").
func mkActiveN4Fixture(id int64, kickoff time.Time, elapsed, home, away int) *fixture.Fixture {
	f := mkActiveFixture(id, kickoff)
	e, h, a := elapsed, home, away
	f.APIElapsed = &e
	f.HomeScore, f.AwayScore = &h, &a
	return f
}

func pi(n int) *int { return &n }

// TestReconcileFixture_ClockAdvance_ClockOnly — the minute advances and nothing
// else: ClockChanged, NOT Structural. This is the fixture.clock tick case.
func TestReconcileFixture_ClockAdvance_ClockOnly(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(46 * time.Minute)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveN4Fixture(999, kickoff, 45, 0, 0))

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h", Long: "First Half", Elapsed: pi(46)}},
		Goals:   apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if !out.ClockChanged {
		t.Error("ClockChanged = false, want true (45→46)")
	}
	if out.Structural {
		t.Error("Structural = true, want false (only the clock moved)")
	}
	if out.Minute != 46 {
		t.Errorf("Minute = %d, want 46", out.Minute)
	}
}

// TestReconcileFixture_FrozenPoll_NeitherSignal — an identical re-poll (stalled
// minute, no changes): neither signal fires → no message that cycle.
func TestReconcileFixture_FrozenPoll_NeitherSignal(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(45 * time.Minute)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveN4Fixture(999, kickoff, 45, 0, 0))

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h", Long: "First Half", Elapsed: pi(45)}},
		Goals:   apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if out.ClockChanged || out.Structural {
		t.Errorf("ClockChanged=%v Structural=%v, want both false (nothing changed)", out.ClockChanged, out.Structural)
	}
}

// TestReconcileFixture_Halftime_StructuralNotClock — the status flips 1H→HT with
// the clock frozen: Structural (a full-refetch change), NOT ClockChanged. Proves
// a status change rides fixture.update even when the minute doesn't move.
func TestReconcileFixture_Halftime_StructuralNotClock(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(45 * time.Minute)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveN4Fixture(999, kickoff, 45, 0, 0))

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "ht", Long: "Halftime", Elapsed: pi(45)}},
		Goals:   apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if !out.Structural {
		t.Error("Structural = false, want true (status 1H→HT)")
	}
	if out.ClockChanged {
		t.Error("ClockChanged = true, want false (clock frozen at HT)")
	}
}

// TestReconcileFixture_ScoreChange_Structural — a score move with no event in the
// same poll (vendor eventual consistency) still classifies as Structural.
func TestReconcileFixture_ScoreChange_Structural(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(46 * time.Minute)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveN4Fixture(999, kickoff, 45, 0, 0))

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h", Long: "First Half", Elapsed: pi(46)}},
		Goals:   apifootball.APIFixtureGoals{Home: pi(1), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if !out.Structural {
		t.Error("Structural = false, want true (score 0→1)")
	}
}

func TestReconcileFixture_ThreeCyclesTriggersDownstream(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(30 * time.Minute)
	fRepo := newFakeFixtureRepo()
	fRepo.Upsert(context.Background(), mkActiveFixture(999, kickoff))
	eRepo := newFakeEventRepo()

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h"}},
		Events:  []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, now)

	// Cycle 1 — insert (count = 1)
	_, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	// Cycle 2 — presence vote (count = 2)
	out2, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w2"})
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if len(out2.EventsBecameStable) != 0 {
		t.Errorf("cycle 2 should not have triggered; got %v", out2.EventsBecameStable)
	}
	// Cycle 3 — presence vote (count = 3, TRIGGERS)
	out3, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w3"})
	if err != nil {
		t.Fatalf("cycle 3: %v", err)
	}
	if len(out3.EventsBecameStable) != 1 {
		t.Errorf("cycle 3 EventsBecameStable = %v, want 1 event", out3.EventsBecameStable)
	}
}

// TestReconcileFixture_TerminalWithWinnerRequiresCoherentDebounce proves that
// vendor winner data remains display/result data and cannot bypass three
// coherent terminal responses.
func TestReconcileFixture_TerminalWithWinnerRequiresCoherentDebounce(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(105 * time.Minute) // post-FT
	fRepo := newFakeFixtureRepo()

	// Fixture in active state with winner data already present.
	f := mkActiveFixture(999, kickoff)
	trueBool := true
	f.HomeWinner = &trueBool
	_ = fRepo.Upsert(context.Background(), f)

	// API response: coherent 0-0 FT snapshot with no events.
	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID:     999,
			Status: apifootball.APIFixtureStatus{Short: "ft", Long: "Match Finished"},
		},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	for cycle := 1; cycle <= 3; cycle++ {
		out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
			APIFixture: apiFix, WorkflowID: fmt.Sprintf("monitor-w%d", cycle),
		})
		if err != nil {
			t.Fatalf("ReconcileFixture cycle %d: %v", cycle, err)
		}
		if cycle < 3 && out.Completed {
			t.Fatalf("cycle %d completed despite counter below 3", cycle)
		}
		if cycle == 3 && !out.Completed {
			t.Fatal("cycle 3 did not complete after three coherent terminal snapshots")
		}
	}
	got, _ := fRepo.Get(context.Background(), 999)
	if got.State != fixture.StateCompleted {
		t.Errorf("state = %q, want completed", got.State)
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt should be set after completion")
	}
}

// TestReconcileFixture_TerminalCounterBelowThreshold_DoesNotComplete —
// FT status but only 1 Terminal poll observed. Counter is 1, no winner.
// Fixture should stay in active waiting for more Terminal polls.
func TestReconcileFixture_TerminalCounterBelowThreshold_DoesNotComplete(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(95 * time.Minute)
	fRepo := newFakeFixtureRepo()
	f := mkActiveFixture(888, kickoff)
	// No winner data set — must debounce via counter.
	_ = fRepo.Upsert(context.Background(), f)

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID:     888,
			Status: apifootball.APIFixtureStatus{Short: "ft"},
		},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: apiFix, WorkflowID: "monitor-w1",
	})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if out.Completed {
		t.Errorf("out.Completed = true, want false (counter = 1, no winner)")
	}
	got, _ := fRepo.Get(context.Background(), 888)
	if got.State != fixture.StateActive {
		t.Errorf("state = %q, want active (still debouncing)", got.State)
	}
	if got.CompletionCounter != 1 {
		t.Errorf("CompletionCounter = %d, want 1", got.CompletionCounter)
	}
}

func TestReconcileFixture_AbsenceHitZeroSoftDeletes(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(30 * time.Minute)
	fRepo := newFakeFixtureRepo()
	fRepo.Upsert(context.Background(), mkActiveFixture(999, kickoff))
	eRepo := newFakeEventRepo()
	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals:  apifootball.APIFixtureGoals{Home: pi(1), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}

	acts := newActs(&fakeFetcher{}, fRepo, eRepo, now)
	// Insert event
	_, _ = acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})

	// Now the event vanishes from the API — one absence brings count 1→0
	empty := apiFix
	empty.Events = nil
	empty.Goals.Home = pi(0) // aggregate score correction is the VAR evidence
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: empty, WorkflowID: "w2"})
	if err != nil {
		t.Fatalf("absence cycle: %v", err)
	}
	if len(out.EventsRemoved) != 1 {
		t.Errorf("EventsRemoved = %v, want 1", out.EventsRemoved)
	}
}

// TestReconcileFixture_GoalAbsenceHeldWhenScoreRequiresIt reproduces the
// Lazio-Mantova failure shape: the provider drops the event-array element but
// retains the aggregate score. The stored goal must receive no absence vote.
func TestReconcileFixture_GoalAbsenceHeldWhenScoreRequiresIt(t *testing.T) {
	kickoff := time.Date(2026, 8, 16, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564801, kickoff))
	eRepo := newFakeEventRepo()

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564801, Status: apifootball.APIFixtureStatus{Short: "2h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(0), Away: pi(1)},
		Events: []apifootball.APIFixtureEvent{
			mkAPIGoal(42, 222, 90),
		},
	}
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(96*time.Minute))
	_, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("insert cycle: %v", err)
	}

	omitted := apiFix
	omitted.Events = nil
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: omitted, WorkflowID: "w2"})
	if err != nil {
		t.Fatalf("omission cycle: %v", err)
	}
	if len(out.GoalAbsencesHeld) != 1 {
		t.Fatalf("GoalAbsencesHeld = %v, want one protected goal", out.GoalAbsencesHeld)
	}
	if len(out.EventsRemoved) != 0 {
		t.Fatalf("EventsRemoved = %v, want none", out.EventsRemoved)
	}

	stored, err := eRepo.GetByNaturalKey(context.Background(), 1564801, "42_222_goal_1")
	if err != nil {
		t.Fatalf("get protected event: %v", err)
	}
	if stored.DebounceCount != 1 || stored.Removed {
		t.Fatalf("protected event = count %d removed %v, want count 1 removed false", stored.DebounceCount, stored.Removed)
	}
}
