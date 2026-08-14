// dto_test.go — mapper unit tests (no HTTP, no DB).
package api

import "testing"

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
