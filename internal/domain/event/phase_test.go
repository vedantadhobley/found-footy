// phase_test.go — DerivePhase first-match-wins ordering, incl. the load-bearing
// "removed wins even after complete" and "detected is the fallthrough" cases.
package event_test

import (
	"testing"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

func TestDerivePhase(t *testing.T) {
	tests := []struct {
		name                              string
		removed, triggered, discoveryDone bool
		want                              event.Phase
	}{
		{"detected: nothing set", false, false, false, event.PhaseDetected},
		{"searching: triggered, not done", false, true, false, event.PhaseSearching},
		{"complete: triggered + done", false, true, true, event.PhaseComplete},
		{"removed wins over detected", true, false, false, event.PhaseRemoved},
		{"removed wins over searching", true, true, false, event.PhaseRemoved},
		{"removed wins over complete (VAR after clips)", true, true, true, event.PhaseRemoved},
		// completed_at can only be set after a trigger in practice, but the
		// derivation must still resolve complete over searching regardless.
		{"complete beats searching when both true", false, true, true, event.PhaseComplete},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := event.DerivePhase(tc.removed, tc.triggered, tc.discoveryDone)
			if got != tc.want {
				t.Errorf("DerivePhase(removed=%v, triggered=%v, done=%v) = %q, want %q",
					tc.removed, tc.triggered, tc.discoveryDone, got, tc.want)
			}
		})
	}
}
