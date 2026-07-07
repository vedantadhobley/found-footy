// Tests for fixture types + state machine transitions.
package fixture_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
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
	terminals := []string{"FT", "AET", "PEN", "CANC", "ABD", "AWD", "WO"}
	for _, code := range terminals {
		if !(fixture.APIStatus{Short: code}).Terminal() {
			t.Errorf("APIStatus(%q).Terminal() = false, want true", code)
		}
	}
	nonTerminals := []string{"NS", "TBD", "1H", "PST", "SUSP"}
	for _, code := range nonTerminals {
		if (fixture.APIStatus{Short: code}).Terminal() {
			t.Errorf("APIStatus(%q).Terminal() = true, want false", code)
		}
	}

	lives := []string{"1H", "HT", "2H", "ET", "BT", "P", "LIVE"}
	for _, code := range lives {
		if !(fixture.APIStatus{Short: code}).Live() {
			t.Errorf("APIStatus(%q).Live() = false, want true", code)
		}
	}
	nonLives := []string{"NS", "TBD", "FT", "PST", "CANC"}
	for _, code := range nonLives {
		if (fixture.APIStatus{Short: code}).Live() {
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
