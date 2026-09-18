// event_replay_unit_test.go pins replay identity and mode semantics without Postgres.
package pg_test

import (
	"testing"

	"github.com/google/uuid"

	pginfra "github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// TestStableEventReplayWorkflowID pins the operator-supplied idempotency key.
func TestStableEventReplayWorkflowID(t *testing.T) {
	eventID := uuid.MustParse("6e6cf592-4755-487b-b3f1-1758318bd3b8")
	got, err := pginfra.StableEventReplayWorkflowID(eventID, "kane-54-20260918")
	if err != nil {
		t.Fatalf("StableEventReplayWorkflowID: %v", err)
	}
	want := "event-replay-6e6cf592-4755-487b-b3f1-1758318bd3b8-kane-54-20260918"
	if got != want {
		t.Fatalf("workflow ID=%q, want %q", got, want)
	}
	for _, operation := range []string{"", "UPPER", "two words", "x"} {
		if _, err := pginfra.StableEventReplayWorkflowID(eventID, operation); err == nil {
			t.Errorf("operation %q unexpectedly accepted", operation)
		}
	}
}

// TestEventReplayModes keeps search and candidate retry independent.
func TestEventReplayModes(t *testing.T) {
	cases := []struct {
		mode     pginfra.EventReplayMode
		searches bool
		retries  bool
	}{
		{pginfra.EventReplayFreshSearch, true, false},
		{pginfra.EventReplayRetryTransient, false, true},
		{pginfra.EventReplayRecover, true, true},
	}
	for _, tc := range cases {
		if !tc.mode.Valid() || tc.mode.Searches() != tc.searches ||
			tc.mode.RetriesTransient() != tc.retries {
			t.Errorf("mode %q semantics changed", tc.mode)
		}
	}
	if pginfra.EventReplayMode("everything").Valid() {
		t.Fatal("unknown replay mode accepted")
	}
}
