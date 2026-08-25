// Active-fixture event-reconciliation activity tests.
package monitor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	eventinfra "github.com/vedantadhobley/found-footy/internal/infra/event"
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

// TestReconcileFixture_TieClearsPriorLeader is the FF-055 regression. The
// provider reports winner flags for the current live leader, then null/null
// when an equalizer restores a tie. Result derivation must clear the stale
// leader from storage and emit a structural update.
func TestReconcileFixture_TieClearsPriorLeader(t *testing.T) {
	kickoff := time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(70 * time.Minute)
	fRepo := newFakeFixtureRepo()
	f := mkActiveN4Fixture(1001, kickoff, 69, 1, 0)
	homeWon, awayWon := true, false
	f.HomeWinner, f.AwayWinner = &homeWon, &awayWon
	_ = fRepo.Upsert(context.Background(), f)

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID:     1001,
			Status: apifootball.APIFixtureStatus{Short: apifootball.StatusSecondHalf, Elapsed: pi(70)},
		},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(1), Away: pi(1)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: apiFix, WorkflowID: "monitor-equalizer",
	})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if !out.Structural {
		t.Fatal("Structural = false, want true for score and result change")
	}
	got, err := fRepo.Get(context.Background(), 1001)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.HomeWinner != nil || got.AwayWinner != nil {
		t.Fatalf("winner = %v/%v, want nil/nil after equalizer", got.HomeWinner, got.AwayWinner)
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

// TestReconcileFixture_TerminalWithWinnerRequiresGrace proves that result data
// cannot bypass the terminal observation grace period.
func TestReconcileFixture_TerminalWithWinnerRequiresGrace(t *testing.T) {
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
	composer := &recordingComposer{}
	times := []time.Time{now, now.Add(time.Hour - 30*time.Second), now.Add(time.Hour)}
	for cycle, cycleAt := range times {
		acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), cycleAt)
		acts.Composer = composer
		out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
			APIFixture: apiFix, WorkflowID: fmt.Sprintf("monitor-w%d", cycle+1),
		})
		if err != nil {
			t.Fatalf("ReconcileFixture cycle %d: %v", cycle+1, err)
		}
		if cycle < 2 && out.Completed {
			t.Fatalf("cycle %d completed before grace elapsed", cycle+1)
		}
		if cycle == 2 && !out.Completed {
			t.Fatal("cycle 3 did not complete at grace boundary")
		}
	}
	got, _ := fRepo.Get(context.Background(), 999)
	if got.State != fixture.StateCompleted {
		t.Errorf("state = %q, want completed", got.State)
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt should be set after completion")
	}
	if composer.kind != eventinfra.KindFixtureCompleted {
		t.Fatalf("completion audit kind = %q, want %q", composer.kind, eventinfra.KindFixtureCompleted)
	}
	payload, ok := composer.payload.(eventinfra.FixtureCompletedPayload)
	if !ok {
		t.Fatalf("completion audit payload type = %T", composer.payload)
	}
	if !payload.TerminalObservedAt.Equal(now) || !payload.CompletedAt.Equal(now.Add(time.Hour)) {
		t.Errorf("completion audit times = observed %v completed %v", payload.TerminalObservedAt, payload.CompletedAt)
	}
	if payload.GraceSeconds != 3600 {
		t.Errorf("completion audit grace = %d, want 3600", payload.GraceSeconds)
	}
	if payload.ProviderScoreEventParity == nil || !*payload.ProviderScoreEventParity ||
		payload.DurableScoreEventParity == nil || !*payload.DurableScoreEventParity {
		t.Errorf("completion parity evidence = provider %v durable %v, want true/true",
			payload.ProviderScoreEventParity, payload.DurableScoreEventParity)
	}
}

// TestReconcileFixture_FirstTerminalPollStartsGrace verifies that the first
// successful terminal response persists the stable recency/grace anchor.
func TestReconcileFixture_FirstTerminalPollStartsGrace(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(95 * time.Minute)
	fRepo := newFakeFixtureRepo()
	f := mkActiveFixture(888, kickoff)
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
		t.Errorf("out.Completed = true, want false before grace")
	}
	got, _ := fRepo.Get(context.Background(), 888)
	if got.State != fixture.StateActive {
		t.Errorf("state = %q, want active during grace", got.State)
	}
	if got.TerminalObservedAt == nil || !got.TerminalObservedAt.Equal(now) {
		t.Errorf("TerminalObservedAt = %v, want %v", got.TerminalObservedAt, now)
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
