// dto_test.go — mapper unit tests (no HTTP, no DB).
package api

import (
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
)

// TestDeriveLastActivity — last_activity_at is max(activation, first terminal
// observation, latest known-scorer event); direct-complete legacy rows fall back
// to completed_at and unknown-scorer placeholders stay excluded.
func TestDeriveLastActivity(t *testing.T) {
	t0 := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	tGoal := t0.Add(20 * time.Minute)
	tLaterUnknown := t0.Add(50 * time.Minute)
	tDone := t0.Add(100 * time.Minute)
	tRetired := tDone.Add(time.Hour)

	id, name := 7, "Scorer"
	known := func(fs time.Time) *event.Event {
		return &event.Event{Player: event.Player{ID: &id, Name: &name}, FirstSeenAt: fs}
	}
	unknown := func(fs time.Time) *event.Event {
		return &event.Event{Player: event.Player{}, FirstSeenAt: fs}
	}
	fActive := &fixture.Fixture{ActivatedAt: &t0}

	// staging (no activation, no events) → nil
	if got := deriveLastActivity(&fixture.Fixture{}, nil); got != nil {
		t.Errorf("staging: got %v, want nil", got)
	}
	// activated, no events → activation time (the floor)
	if got := deriveLastActivity(fActive, nil); got == nil || !got.Equal(t0) {
		t.Errorf("activated-only: got %v, want %v", got, t0)
	}
	// activated + a known goal → the goal's first-seen
	if got := deriveLastActivity(fActive, []*event.Event{known(tGoal)}); got == nil || !got.Equal(tGoal) {
		t.Errorf("known goal: got %v, want %v", got, tGoal)
	}
	// a LATER unknown-scorer placeholder must NOT count → still the known goal
	if got := deriveLastActivity(fActive, []*event.Event{known(tGoal), unknown(tLaterUnknown)}); got == nil || !got.Equal(tGoal) {
		t.Errorf("unknown excluded: got %v, want %v", got, tGoal)
	}
	// Graceful completion keeps terminal observation as recency even though the
	// process row moves to completed an hour later.
	fDone := &fixture.Fixture{
		State: fixture.StateCompleted, ActivatedAt: &t0,
		TerminalObservedAt: &tDone, CompletedAt: &tRetired,
	}
	if got := deriveLastActivity(fDone, []*event.Event{known(tGoal)}); got == nil || !got.Equal(tDone) {
		t.Errorf("graceful completion: got %v, want %v", got, tDone)
	}
	// Fresh historical and pre-migration completed rows have no observation.
	fLegacy := &fixture.Fixture{State: fixture.StateCompleted, ActivatedAt: &t0, CompletedAt: &tDone}
	if got := deriveLastActivity(fLegacy, nil); got == nil || !got.Equal(tDone) {
		t.Errorf("legacy completion: got %v, want %v", got, tDone)
	}
}

// TestToPenaltyDTO — a shootout maps to {home, away} only when BOTH sides have
// a penalty score; anything else (no shootout, or a half-populated poll) is nil.
func TestToPenaltyDTO(t *testing.T) {
	p := func(n int) *int { return &n }

	if got := toPenaltyDTO(nil, nil); got != nil {
		t.Errorf("no shootout → nil, got %+v", got)
	}
	if got := toPenaltyDTO(p(5), nil); got != nil {
		t.Errorf("home-only → nil, got %+v", got)
	}
	if got := toPenaltyDTO(nil, p(6)); got != nil {
		t.Errorf("away-only → nil, got %+v", got)
	}
	got := toPenaltyDTO(p(5), p(6))
	if got == nil || got.Home != 5 || got.Away != 6 {
		t.Errorf("shootout → {5,6}, got %+v", got)
	}
}

// TestToEventDTO_Assist — the assister surfaces on the event DTO when present,
// null when the vendor reported none (independent of the scorer).
func TestToEventDTO_Assist(t *testing.T) {
	id, name := 7, "Scorer"
	aid, aname := 8, "Assister"
	base := func(assist event.Player) *event.Event {
		return &event.Event{
			FixtureID: 1, Type: event.Type("goal"),
			Player: event.Player{ID: &id, Name: &name}, Assist: assist,
		}
	}
	// with an assister → populated
	d := toEventDTO(base(event.Player{ID: &aid, Name: &aname}), nil, false)
	if d.Assist == nil || d.Assist.ID != 8 || d.Assist.Name != "Assister" {
		t.Errorf("with assist: got %+v, want {8, Assister}", d.Assist)
	}
	// no assister → nil
	if d := toEventDTO(base(event.Player{}), nil, false); d.Assist != nil {
		t.Errorf("no assist: got %+v, want nil", d.Assist)
	}
}
