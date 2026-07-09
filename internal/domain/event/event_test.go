// Tests for event types + state machine transitions + natural_key
// composition.
package event_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// helpers ---------------------------------------------------------

func player(id int, name string) event.Player {
	return event.Player{ID: &id, Name: &name}
}

func unknownPlayer() event.Player {
	return event.Player{}
}

func makeGoal() *event.Event {
	return event.New(
		5000,
		event.Team{ID: 40, Name: "Liverpool"},
		player(234, "D. Szoboszlai"),
		event.TypeGoal,
		"Normal Goal",
		23, nil, 1,
		time.Date(2026, 7, 8, 15, 23, 0, 0, time.UTC),
	)
}

// Enums -----------------------------------------------------------

func TestType_Valid(t *testing.T) {
	valid := []event.Type{event.TypeGoal, event.TypeCard, event.TypeSubst, event.TypeVar, event.TypeMissedPenalty}
	for _, v := range valid {
		if !v.Valid() {
			t.Errorf("Type(%q).Valid() = false, want true", v)
		}
	}
	// Canonical form is lowercase per the 2026-07-09 lowercase-canonical
	// policy — "Goal" (title case) is now bogus, "goal" is valid.
	for _, bogus := range []event.Type{"", "Goal" /* was canonical, now bogus */, "PenaltyShootout"} {
		if bogus.Valid() {
			t.Errorf("Type(%q).Valid() = true, want false", bogus)
		}
	}
}

// TestTrackableEventType — locks in the classification rules matching
// docs/api-football/events-shape.md + real vendor emission audit.
func TestTrackableEventType(t *testing.T) {
	cases := []struct {
		name       string
		apiType    apifootball.APIEventType
		apiDetail  apifootball.APIEventDetail
		apiComment string
		wantType   event.Type
		wantOK     bool
	}{
		{"normal goal", apifootball.EventTypeGoal, apifootball.DetailNormalGoal, "", event.TypeGoal, true},
		{"penalty goal", apifootball.EventTypeGoal, apifootball.DetailPenalty, "", event.TypeGoal, true},
		{"own goal", apifootball.EventTypeGoal, apifootball.DetailOwnGoal, "", event.TypeGoal, true},
		{"missed penalty (open play)", apifootball.EventTypeGoal, apifootball.DetailMissedPenalty, "", event.TypeMissedPenalty, true},
		{"missed penalty (shootout)", apifootball.EventTypeGoal, apifootball.DetailMissedPenalty, "Penalty Shootout", "", false},
		{"shootout goal", apifootball.EventTypeGoal, apifootball.DetailPenalty, "Penalty Shootout", "", false},
		{"shootout goal case-insensitive", apifootball.EventTypeGoal, apifootball.DetailPenalty, "penalty shootout", "", false},
		{"red card", apifootball.EventTypeCard, apifootball.DetailRedCard, "Foul", event.TypeCard, true},
		{"red card serious foul", apifootball.EventTypeCard, apifootball.DetailRedCard, "Serious foul", event.TypeCard, true},
		{"yellow card skipped", apifootball.EventTypeCard, apifootball.DetailYellowCard, "Foul", "", false},
		{"subst skipped", apifootball.EventTypeSubst, apifootball.DetailSubstitution, "", "", false},
		{"var goal-cancelled skipped", apifootball.EventTypeVar, apifootball.DetailGoalCancelled, "", "", false},
		{"var penalty-confirmed skipped", apifootball.EventTypeVar, apifootball.DetailPenaltyConfirmed, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := event.TrackableEventType(tc.apiType, tc.apiDetail, tc.apiComment)
			if got != tc.wantType || ok != tc.wantOK {
				t.Errorf("TrackableEventType(%q, %q, %q) = (%q, %v), want (%q, %v)",
					tc.apiType, tc.apiDetail, tc.apiComment, got, ok, tc.wantType, tc.wantOK)
			}
		})
	}
}

func TestRemovalReason_Valid(t *testing.T) {
	for _, r := range []event.RemovalReason{event.RemovalVAR, event.RemovalPolicy, event.RemovalAssetGone} {
		if !r.Valid() {
			t.Errorf("RemovalReason(%q).Valid() = false, want true", r)
		}
	}
	for _, bogus := range []event.RemovalReason{"", "VAR" /* uppercase */, "typo"} {
		if bogus.Valid() {
			t.Errorf("RemovalReason(%q).Valid() = true, want false", bogus)
		}
	}
}

func TestPlayer_Known(t *testing.T) {
	if !player(234, "Szoboszlai").Known() {
		t.Error("populated Player.Known() = false, want true")
	}
	if unknownPlayer().Known() {
		t.Error("empty Player.Known() = true, want false")
	}
	id := 234
	if (event.Player{ID: &id}).Known() {
		t.Error("Player with ID but no Name should not be Known()")
	}
}

// ComposeNaturalKey ----------------------------------------------

func TestComposeNaturalKey(t *testing.T) {
	pid := 234
	got := event.ComposeNaturalKey(40, &pid, event.TypeGoal, 1)
	if got != "40_234_goal_1" {
		t.Errorf("known player key = %q, want 40_234_goal_1", got)
	}

	got = event.ComposeNaturalKey(40, nil, event.TypeGoal, 1)
	if got != "40_unknown_goal_1" {
		t.Errorf("unknown player key = %q, want 40_unknown_goal_1", got)
	}
}

// Construction ---------------------------------------------------

func TestNew_PopulatesRequiredFields(t *testing.T) {
	e := makeGoal()
	if e.ID.String() == "" {
		t.Error("New: ID is empty")
	}
	if e.FixtureID != 5000 {
		t.Errorf("FixtureID = %d, want 5000", e.FixtureID)
	}
	if e.NaturalKey != "40_234_goal_1" {
		t.Errorf("NaturalKey = %q, want 40_234_goal_1", e.NaturalKey)
	}
	if e.MonitorComplete || e.DownloadComplete || e.Removed {
		t.Error("New event must start with all flags false")
	}
	if err := e.ValidateInvariants(); err != nil {
		t.Errorf("new event fails invariants: %v", err)
	}
}

// State transitions ---------------------------------------------

func TestMarkMonitorComplete_Idempotent(t *testing.T) {
	e := makeGoal()
	at := time.Date(2026, 7, 8, 15, 24, 30, 0, time.UTC)
	if err := e.MarkMonitorComplete(at); err != nil {
		t.Fatalf("first MarkMonitorComplete: %v", err)
	}
	if !e.MonitorComplete {
		t.Error("MonitorComplete not set after first call")
	}
	first := e.UpdatedAt

	if err := e.MarkMonitorComplete(at.Add(1 * time.Minute)); err != nil {
		t.Errorf("second MarkMonitorComplete: %v", err)
	}
	if !e.UpdatedAt.Equal(first) {
		t.Errorf("second MarkMonitorComplete mutated UpdatedAt %v → %v", first, e.UpdatedAt)
	}
}

func TestMarkMonitorComplete_RejectsRemoved(t *testing.T) {
	e := makeGoal()
	at := time.Date(2026, 7, 8, 15, 24, 30, 0, time.UTC)
	if err := e.Remove(event.RemovalVAR, at); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	err := e.MarkMonitorComplete(at.Add(1 * time.Minute))
	if !errors.Is(err, event.ErrRemovedEvent) {
		t.Errorf("MarkMonitorComplete on removed = %v, want ErrRemovedEvent", err)
	}
}

func TestMarkDownloadComplete_Idempotent(t *testing.T) {
	e := makeGoal()
	at := time.Date(2026, 7, 8, 15, 35, 0, 0, time.UTC)
	if err := e.MarkDownloadComplete(at); err != nil {
		t.Fatalf("first MarkDownloadComplete: %v", err)
	}
	if !e.DownloadComplete {
		t.Error("DownloadComplete not set")
	}
	if err := e.MarkDownloadComplete(at); err != nil {
		t.Errorf("second MarkDownloadComplete: %v", err)
	}
}

func TestMarkDownloadComplete_RejectsRemoved(t *testing.T) {
	e := makeGoal()
	at := time.Date(2026, 7, 8, 15, 24, 30, 0, time.UTC)
	if err := e.Remove(event.RemovalVAR, at); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	err := e.MarkDownloadComplete(at.Add(1 * time.Minute))
	if !errors.Is(err, event.ErrRemovedEvent) {
		t.Errorf("MarkDownloadComplete on removed = %v, want ErrRemovedEvent", err)
	}
}

func TestRemove_HappyPath(t *testing.T) {
	e := makeGoal()
	at := time.Date(2026, 7, 8, 15, 30, 0, 0, time.UTC)
	if err := e.Remove(event.RemovalVAR, at); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !e.Removed {
		t.Error("Removed flag not set")
	}
	if e.RemovedReason == nil || *e.RemovedReason != event.RemovalVAR {
		t.Errorf("RemovedReason = %v, want var", e.RemovedReason)
	}
	if e.RemovedAt == nil || !e.RemovedAt.Equal(at) {
		t.Errorf("RemovedAt = %v, want %v", e.RemovedAt, at)
	}
	if err := e.ValidateInvariants(); err != nil {
		t.Errorf("post-Remove invariants: %v", err)
	}
}

func TestRemove_SameReasonIsIdempotent(t *testing.T) {
	e := makeGoal()
	at := time.Date(2026, 7, 8, 15, 30, 0, 0, time.UTC)
	if err := e.Remove(event.RemovalVAR, at); err != nil {
		t.Fatalf("first Remove: %v", err)
	}
	if err := e.Remove(event.RemovalVAR, at.Add(1*time.Minute)); err != nil {
		t.Errorf("idempotent Remove: %v", err)
	}
}

func TestRemove_DifferentReasonErrors(t *testing.T) {
	e := makeGoal()
	at := time.Date(2026, 7, 8, 15, 30, 0, 0, time.UTC)
	if err := e.Remove(event.RemovalVAR, at); err != nil {
		t.Fatalf("first Remove: %v", err)
	}
	err := e.Remove(event.RemovalPolicy, at.Add(1*time.Minute))
	if !errors.Is(err, event.ErrAlreadyRemoved) {
		t.Errorf("second Remove with different reason = %v, want ErrAlreadyRemoved", err)
	}
}

func TestRemove_InvalidReasonErrors(t *testing.T) {
	e := makeGoal()
	err := e.Remove(event.RemovalReason("typo"), time.Now())
	if !errors.Is(err, event.ErrInvalidRemovalReason) {
		t.Errorf("Remove with invalid reason = %v, want ErrInvalidRemovalReason", err)
	}
}

// UpdatePlayer + UpdateMinute -------------------------------------

func TestUpdatePlayer_PopulatesFromUnknown(t *testing.T) {
	e := event.New(
		5000,
		event.Team{ID: 40, Name: "Liverpool"},
		unknownPlayer(),
		event.TypeGoal, "Normal Goal", 23, nil, 1,
		time.Now().UTC(),
	)
	if e.Player.Known() {
		t.Fatal("test setup: player must start unknown")
	}
	e.UpdatePlayer(player(234, "Szoboszlai"), time.Now().UTC())
	if !e.Player.Known() {
		t.Error("UpdatePlayer did not populate player")
	}
	// natural_key must NOT change — that's the immutability rule.
	if e.NaturalKey != "40_unknown_goal_1" {
		t.Errorf("natural_key changed to %q; should be immutable", e.NaturalKey)
	}
}

func TestUpdateMinute(t *testing.T) {
	e := makeGoal()
	extra := 3
	at := time.Date(2026, 7, 8, 15, 26, 0, 0, time.UTC)
	e.UpdateMinute(45, &extra, at)
	if e.Minute != 45 {
		t.Errorf("Minute = %d, want 45", e.Minute)
	}
	if e.Extra == nil || *e.Extra != 3 {
		t.Errorf("Extra = %v, want 3", e.Extra)
	}
	if !e.UpdatedAt.Equal(at) {
		t.Errorf("UpdatedAt = %v, want %v", e.UpdatedAt, at)
	}
}

// Invariant validator --------------------------------------------

func TestValidateInvariants_CatchesInconsistentRemoval(t *testing.T) {
	e := makeGoal()
	e.Removed = true // set without going through Remove()
	err := e.ValidateInvariants()
	if !errors.Is(err, event.ErrStateTimestampMismatch) {
		t.Errorf("expected ErrStateTimestampMismatch on removed=true without RemovedReason, got %v", err)
	}

	e = makeGoal()
	reason := event.RemovalVAR
	e.RemovedReason = &reason // set without Remove()
	err = e.ValidateInvariants()
	if !errors.Is(err, event.ErrStateTimestampMismatch) {
		t.Errorf("expected ErrStateTimestampMismatch on RemovedReason set with removed=false, got %v", err)
	}
}
