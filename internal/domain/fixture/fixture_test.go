// Tests for fixture types + state machine transitions.
package fixture_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// helpers ---------------------------------------------------------

func makeStaging() *fixture.Fixture {
	return fixture.New(
		5000,
		fixture.APIStatus{Short: "NS", Long: "Not Started"},
		time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC),
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Name: "Premier League", Season: 2026},
	)
}

func mustActivate(t *testing.T, f *fixture.Fixture, at time.Time) {
	t.Helper()
	if err := f.Activate(at); err != nil {
		t.Fatalf("Activate: %v", err)
	}
}

// State enum -------------------------------------------------------

func TestState_Valid(t *testing.T) {
	cases := map[fixture.State]bool{
		fixture.StateStaging:   true,
		fixture.StateActive:    true,
		fixture.StateCompleted: true,
		fixture.State("bogus"): false,
		fixture.State(""):      false,
	}
	for s, want := range cases {
		if got := s.Valid(); got != want {
			t.Errorf("State(%q).Valid() = %v, want %v", s, got, want)
		}
	}
}

// APIStatus helpers -----------------------------------------------

func TestAPIStatus_TerminalAndLive(t *testing.T) {
	// Values are pre-canonicalized (lowercase) because production
	// callers construct APIStatus via Parse or JSON unmarshal, which
	// normalize at the boundary. Bypassing Parse in tests uses the
	// canonical form directly.
	terminals := []string{"ft", "aet", "pen", "canc", "abd", "awd", "wo"}
	for _, code := range terminals {
		if !(fixture.APIStatus{Short: apifootball.APIStatusCode(code)}).Terminal() {
			t.Errorf("APIStatus(%q).Terminal() = false, want true", code)
		}
	}
	nonTerminals := []string{"ns", "tbd", "1h", "pst", "susp"}
	for _, code := range nonTerminals {
		if (fixture.APIStatus{Short: apifootball.APIStatusCode(code)}).Terminal() {
			t.Errorf("APIStatus(%q).Terminal() = true, want false", code)
		}
	}

	lives := []string{"1h", "ht", "2h", "et", "bt", "p", "live", "susp", "int", "pst"}
	for _, code := range lives {
		if !(fixture.APIStatus{Short: apifootball.APIStatusCode(code)}).Live() {
			t.Errorf("APIStatus(%q).Live() = false, want true", code)
		}
	}
	nonLives := []string{"ns", "tbd", "ft", "canc", "aet", "pen", "abd"}
	for _, code := range nonLives {
		if (fixture.APIStatus{Short: apifootball.APIStatusCode(code)}).Live() {
			t.Errorf("APIStatus(%q).Live() = true, want false", code)
		}
	}
}

// Construction -----------------------------------------------------

func TestNew_StartsInStaging(t *testing.T) {
	f := makeStaging()
	if f.State != fixture.StateStaging {
		t.Errorf("New fixture state = %q, want staging", f.State)
	}
	if f.ActivatedAt != nil {
		t.Error("New fixture must have ActivatedAt = nil")
	}
	if f.CompletedAt != nil {
		t.Error("New fixture must have CompletedAt = nil")
	}
	if err := f.ValidateInvariants(); err != nil {
		t.Errorf("New fixture violates invariants: %v", err)
	}
}

// State transitions -----------------------------------------------

func TestActivate_FromStaging_Ok(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC)
	if err := f.Activate(at); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if f.State != fixture.StateActive {
		t.Errorf("State = %q, want active", f.State)
	}
	if f.ActivatedAt == nil || !f.ActivatedAt.Equal(at) {
		t.Errorf("ActivatedAt = %v, want %v", f.ActivatedAt, at)
	}
	if err := f.ValidateInvariants(); err != nil {
		t.Errorf("post-Activate invariants: %v", err)
	}
}

func TestActivate_Idempotent(t *testing.T) {
	f := makeStaging()
	firstAt := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC)
	mustActivate(t, f, firstAt)

	// Second Activate should no-op.
	secondAt := firstAt.Add(1 * time.Minute)
	if err := f.Activate(secondAt); err != nil {
		t.Fatalf("second Activate: %v", err)
	}
	if !f.ActivatedAt.Equal(firstAt) {
		t.Errorf("second Activate mutated ActivatedAt to %v; want unchanged %v", f.ActivatedAt, firstAt)
	}
}

func TestActivate_FromCompleted_Errors(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC)
	mustActivate(t, f, at)
	if err := f.Complete(at.Add(2 * time.Hour)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	err := f.Activate(at.Add(3 * time.Hour))
	if !errors.Is(err, fixture.ErrInvalidStateTransition) {
		t.Errorf("Activate from completed err = %v, want ErrInvalidStateTransition", err)
	}
}

func TestComplete_FromActive_Ok(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC)
	mustActivate(t, f, at)
	finish := at.Add(2 * time.Hour)
	if err := f.Complete(finish); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if f.State != fixture.StateCompleted {
		t.Errorf("State = %q, want completed", f.State)
	}
	if f.CompletedAt == nil || !f.CompletedAt.Equal(finish) {
		t.Errorf("CompletedAt = %v, want %v", f.CompletedAt, finish)
	}
	if f.ActivatedAt == nil {
		t.Error("Complete cleared ActivatedAt; must preserve it (completed invariant)")
	}
	if err := f.ValidateInvariants(); err != nil {
		t.Errorf("post-Complete invariants: %v", err)
	}
}

func TestComplete_FromStaging_Errors(t *testing.T) {
	f := makeStaging()
	err := f.Complete(time.Now().UTC())
	if !errors.Is(err, fixture.ErrInvalidStateTransition) {
		t.Errorf("Complete from staging err = %v, want ErrInvalidStateTransition", err)
	}
}

func TestReschedule_FromActive_MovesBackToStaging(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC)
	mustActivate(t, f, at)
	newKickoff := time.Date(2026, 7, 15, 15, 0, 0, 0, time.UTC)
	if err := f.Reschedule(newKickoff, at.Add(10*time.Minute)); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	if f.State != fixture.StateStaging {
		t.Errorf("State = %q, want staging", f.State)
	}
	if f.ActivatedAt != nil {
		t.Error("Reschedule left ActivatedAt set; staging must have nil")
	}
	if !f.Kickoff.Equal(newKickoff) {
		t.Errorf("Kickoff = %v, want %v", f.Kickoff, newKickoff)
	}
	if err := f.ValidateInvariants(); err != nil {
		t.Errorf("post-Reschedule invariants: %v", err)
	}
}

func TestReschedule_FromCompleted_Errors(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC)
	mustActivate(t, f, at)
	if err := f.Complete(at.Add(2 * time.Hour)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	err := f.Reschedule(at.Add(24*time.Hour), at.Add(3*time.Hour))
	if !errors.Is(err, fixture.ErrInvalidStateTransition) {
		t.Errorf("Reschedule from completed err = %v, want ErrInvalidStateTransition", err)
	}
}

// UpdateFromPoll --------------------------------------------------

func TestUpdateFromPoll_PopulatesLiveFieldsWithoutStateChange(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC)
	mustActivate(t, f, at)

	elapsed := 23
	extra := 0
	hs := 1
	as := 0
	pollAt := at.Add(23 * time.Minute)
	f.UpdateFromPoll(
		fixture.APIStatus{Short: "1H", Long: "First Half"},
		&elapsed, &extra, &hs, &as, pollAt,
	)

	if f.State != fixture.StateActive {
		t.Errorf("UpdateFromPoll changed state: got %q", f.State)
	}
	if f.APIStatus.Short != "1H" {
		t.Errorf("APIStatus.Short = %q, want 1H", f.APIStatus.Short)
	}
	if f.APIElapsed == nil || *f.APIElapsed != 23 {
		t.Errorf("APIElapsed = %v, want 23", f.APIElapsed)
	}
	if f.HomeScore == nil || *f.HomeScore != 1 {
		t.Errorf("HomeScore = %v, want 1", f.HomeScore)
	}
	if f.LastPolledAt == nil || !f.LastPolledAt.Equal(pollAt) {
		t.Errorf("LastPolledAt = %v, want %v", f.LastPolledAt, pollAt)
	}
}

func TestUpdateFromPoll_NilScore_LeavesExistingScore(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC)
	mustActivate(t, f, at)

	first := 1
	f.UpdateFromPoll(
		fixture.APIStatus{Short: "1H"},
		nil, nil, &first, &first, at.Add(20*time.Minute),
	)
	// Poll where API drops score field; must not clear the existing score.
	f.UpdateFromPoll(
		fixture.APIStatus{Short: "HT"},
		nil, nil, nil, nil, at.Add(45*time.Minute),
	)
	if f.HomeScore == nil || *f.HomeScore != 1 {
		t.Errorf("HomeScore cleared by nil poll; got %v want 1", f.HomeScore)
	}
}

// ShouldActivateNow ------------------------------------------------

func TestShouldActivateNow_KickoffFarAway_False(t *testing.T) {
	f := makeStaging()                                  // kickoff is 2026-07-08 15:00 UTC
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC) // 5h before kickoff
	if f.ShouldActivateNow(now, 30*time.Minute) {
		t.Error("kickoff 5h out with 30min window should not activate")
	}
}

func TestShouldActivateNow_KickoffWithinWindow_True(t *testing.T) {
	f := makeStaging()                                   // kickoff at 15:00
	now := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC) // 15 min before
	if !f.ShouldActivateNow(now, 30*time.Minute) {
		t.Error("kickoff 15min out with 30min window should activate")
	}
}

func TestShouldActivateNow_KickoffAtBoundary_True(t *testing.T) {
	f := makeStaging()                                   // kickoff at 15:00
	now := time.Date(2026, 7, 8, 14, 30, 0, 0, time.UTC) // exactly 30min before
	if !f.ShouldActivateNow(now, 30*time.Minute) {
		t.Error("kickoff at exact window boundary should activate (inclusive)")
	}
}

func TestShouldActivateNow_KickoffJustOutsideBoundary_False(t *testing.T) {
	f := makeStaging()                                              // kickoff at 15:00
	now := time.Date(2026, 7, 8, 14, 29, 59, 999_999_999, time.UTC) // 30min + 1ns out
	if f.ShouldActivateNow(now, 30*time.Minute) {
		t.Error("kickoff 1ns outside boundary should not activate")
	}
}

func TestShouldActivateNow_KickoffInPast_True(t *testing.T) {
	f := makeStaging()                                  // kickoff at 15:00
	now := time.Date(2026, 7, 8, 15, 5, 0, 0, time.UTC) // 5min AFTER kickoff (delayed ingest)
	if !f.ShouldActivateNow(now, 30*time.Minute) {
		t.Error("kickoff already past should activate (delayed manual ingest catches up)")
	}
}

func TestShouldActivateNow_AlreadyActive_False(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC)
	mustActivate(t, f, at)
	// Even with kickoff imminent, an already-active fixture is ineligible.
	if f.ShouldActivateNow(at, 30*time.Minute) {
		t.Error("active fixture should never re-activate via ShouldActivateNow")
	}
}

func TestShouldActivateNow_Completed_False(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 14, 45, 0, 0, time.UTC)
	mustActivate(t, f, at)
	if err := f.Complete(at.Add(2 * time.Hour)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if f.ShouldActivateNow(at, 30*time.Minute) {
		t.Error("completed fixture should never re-activate")
	}
}

// Terminal observation + winner data -------------------------------

func TestUpdateFromPoll_TerminalObservationStartsAndIsPreserved(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	mustActivate(t, f, at)
	first := at.Add(2 * time.Hour)
	f.UpdateFromPoll(
		fixture.APIStatus{Short: "ft", Long: "Match Finished"},
		nil, nil, nil, nil, first,
	)
	if f.TerminalObservedAt == nil || !f.TerminalObservedAt.Equal(first) {
		t.Fatalf("first terminal observation = %v, want %v", f.TerminalObservedAt, first)
	}
	f.UpdateFromPoll(
		fixture.APIStatus{Short: "ft", Long: "Match Finished"},
		nil, nil, nil, nil, first.Add(30*time.Second),
	)
	if f.TerminalObservedAt == nil || !f.TerminalObservedAt.Equal(first) {
		t.Errorf("later terminal poll moved observation to %v, want %v", f.TerminalObservedAt, first)
	}
}

func TestUpdateFromPoll_NonTerminalClearsTerminalObservation(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	mustActivate(t, f, at)
	f.UpdateFromPoll(fixture.APIStatus{Short: "ft"}, nil, nil, nil, nil, at)
	f.UpdateFromPoll(
		fixture.APIStatus{Short: "2h"},
		nil, nil, nil, nil, at.Add(30*time.Second),
	)
	if f.TerminalObservedAt != nil {
		t.Errorf("non-terminal poll left terminal observation %v", f.TerminalObservedAt)
	}
}

func TestRescheduleClearsTerminalObservation(t *testing.T) {
	f := makeStaging()
	at := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	mustActivate(t, f, at)
	f.UpdateFromPoll(fixture.APIStatus{Short: "ft"}, nil, nil, nil, nil, at)
	if err := f.Reschedule(at.Add(24*time.Hour), at.Add(time.Minute)); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	if f.TerminalObservedAt != nil {
		t.Errorf("reschedule left terminal observation %v", f.TerminalObservedAt)
	}
}

func TestUpdateResult_DerivesPlayedWinnerFromScore(t *testing.T) {
	tests := []struct {
		name                 string
		homeScore, awayScore *int
		wantHome, wantAway   *bool
	}{
		{name: "home leads", homeScore: intPointer(2), awayScore: intPointer(1), wantHome: boolPointer(true), wantAway: boolPointer(false)},
		{name: "away leads", homeScore: intPointer(0), awayScore: intPointer(1), wantHome: boolPointer(false), wantAway: boolPointer(true)},
		{name: "tie clears stale leader", homeScore: intPointer(1), awayScore: intPointer(1)},
		{name: "missing score has no winner", homeScore: nil, awayScore: intPointer(1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := makeStaging()
			f.APIStatus = fixture.APIStatus{Short: apifootball.StatusSecondHalf}
			f.HomeScore, f.AwayScore = tt.homeScore, tt.awayScore
			f.HomeWinner, f.AwayWinner = boolPointer(true), boolPointer(false)

			// Contradictory provider flags must not override a played score.
			f.UpdateResult(boolPointer(false), boolPointer(true))
			assertBoolPointer(t, "home winner", f.HomeWinner, tt.wantHome)
			assertBoolPointer(t, "away winner", f.AwayWinner, tt.wantAway)
		})
	}
}

func TestUpdateResult_PenaltyAndExceptionalOutcomes(t *testing.T) {
	t.Run("penalty winner comes from shootout", func(t *testing.T) {
		f := makeStaging()
		f.APIStatus = fixture.APIStatus{Short: apifootball.StatusPenaltyDone}
		f.HomeScore, f.AwayScore = intPointer(1), intPointer(1)
		f.HomePenalty, f.AwayPenalty = intPointer(5), intPointer(4)
		f.UpdateResult(nil, nil)
		assertBoolPointer(t, "home winner", f.HomeWinner, boolPointer(true))
		assertBoolPointer(t, "away winner", f.AwayWinner, boolPointer(false))
	})

	t.Run("tied penalty has no winner", func(t *testing.T) {
		f := makeStaging()
		f.APIStatus = fixture.APIStatus{Short: apifootball.StatusPenaltyDone}
		f.HomePenalty, f.AwayPenalty = intPointer(4), intPointer(4)
		f.UpdateResult(boolPointer(true), boolPointer(false))
		assertBoolPointer(t, "home winner", f.HomeWinner, nil)
		assertBoolPointer(t, "away winner", f.AwayWinner, nil)
	})

	t.Run("exceptional result uses exact provider flags", func(t *testing.T) {
		f := makeStaging()
		f.APIStatus = fixture.APIStatus{Short: apifootball.StatusWalkover}
		f.HomeScore, f.AwayScore = intPointer(0), intPointer(3)
		f.UpdateResult(boolPointer(true), boolPointer(false))
		assertBoolPointer(t, "home winner", f.HomeWinner, boolPointer(true))
		assertBoolPointer(t, "away winner", f.AwayWinner, boolPointer(false))

		f.UpdateResult(nil, nil)
		assertBoolPointer(t, "cleared home winner", f.HomeWinner, nil)
		assertBoolPointer(t, "cleared away winner", f.AwayWinner, nil)
	})
}

// Invariant validator ---------------------------------------------

func TestValidateInvariants_CatchesInconsistentTimestamps(t *testing.T) {
	f := makeStaging()
	f.State = fixture.StateActive
	// active requires ActivatedAt != nil — leaving it nil should fail.
	if err := f.ValidateInvariants(); !errors.Is(err, fixture.ErrStateTimestampMismatch) {
		t.Errorf("expected ErrStateTimestampMismatch, got %v", err)
	}

	now := time.Now().UTC()
	f.ActivatedAt = &now
	f.CompletedAt = &now
	// active must NOT have CompletedAt set.
	if err := f.ValidateInvariants(); !errors.Is(err, fixture.ErrStateTimestampMismatch) {
		t.Errorf("expected ErrStateTimestampMismatch on completed_at-during-active, got %v", err)
	}
}

// TestUpdatePenalty verifies that shootout state mirrors the current provider
// response, including clearing stale values when the response omits them.
func TestUpdatePenalty(t *testing.T) {
	f := makeStaging()
	if f.HomePenalty != nil || f.AwayPenalty != nil {
		t.Fatalf("penalty should start nil")
	}
	h, a := 5, 6
	f.UpdatePenalty(&h, &a)
	if f.HomePenalty == nil || *f.HomePenalty != 5 || f.AwayPenalty == nil || *f.AwayPenalty != 6 {
		t.Fatalf("after capture = %v/%v, want 5/6", f.HomePenalty, f.AwayPenalty)
	}
	// A subsequent nil/nil poll clears the captured shootout.
	f.UpdatePenalty(nil, nil)
	if f.HomePenalty != nil || f.AwayPenalty != nil {
		t.Errorf("nil poll left stale penalty: %v/%v", f.HomePenalty, f.AwayPenalty)
	}
}

// intPointer constructs a score pointer for result-state tables.
func intPointer(value int) *int { return &value }

// boolPointer constructs a winner pointer for result-state tables.
func boolPointer(value bool) *bool { return &value }

// assertBoolPointer compares nullable winner values by presence and value.
func assertBoolPointer(t *testing.T, name string, got, want *bool) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
		return
	}
	if *got != *want {
		t.Fatalf("%s = %v, want %v", name, *got, *want)
	}
}
