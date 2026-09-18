// Replay runner configuration tests cover the exact historical-repair selector.
package main

import (
	"testing"

	"github.com/google/uuid"
)

// TestExactReplaySelector requires the target event and every reviewed
// candidate together, rejecting partial or ambiguous operator input.
func TestExactReplaySelector(t *testing.T) {
	eventID := uuid.New()
	first := uuid.New()
	second := uuid.New()

	t.Run("exact selector", func(t *testing.T) {
		t.Setenv("TARGET_EVENT_ID", eventID.String())
		t.Setenv("REPLAY_CANDIDATE_IDS", first.String()+","+second.String())
		gotEvent, gotCandidates, err := exactReplaySelector()
		if err != nil {
			t.Fatalf("exactReplaySelector: %v", err)
		}
		if gotEvent != eventID || len(gotCandidates) != 2 ||
			gotCandidates[0] != first || gotCandidates[1] != second {
			t.Fatalf("event=%s candidates=%v", gotEvent, gotCandidates)
		}
	})

	t.Run("event without candidates", func(t *testing.T) {
		t.Setenv("TARGET_EVENT_ID", eventID.String())
		t.Setenv("REPLAY_CANDIDATE_IDS", "")
		if _, _, err := exactReplaySelector(); err == nil {
			t.Fatal("partial selector unexpectedly accepted")
		}
	})

	t.Run("duplicate candidate", func(t *testing.T) {
		t.Setenv("TARGET_EVENT_ID", eventID.String())
		t.Setenv("REPLAY_CANDIDATE_IDS", first.String()+","+first.String())
		if _, _, err := exactReplaySelector(); err == nil {
			t.Fatal("duplicate candidate unexpectedly accepted")
		}
	})
}
