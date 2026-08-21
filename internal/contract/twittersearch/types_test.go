// Result-state contract tests for browser-to-worker search accounting.
package twittersearch

import "testing"

func TestResultStateUsableAndKnown(t *testing.T) {
	for _, state := range []ResultState{
		ResultRendered, ResultExplicitEmpty, ResultLogin, ResultUpstreamError, ResultUnknownTimeout,
	} {
		if !state.Known() {
			t.Errorf("%q must be a known bounded state", state)
		}
	}
	if !ResultRendered.Usable() || !ResultExplicitEmpty.Usable() {
		t.Fatal("rendered and explicit-empty observations must be usable")
	}
	for _, state := range []ResultState{ResultLogin, ResultUpstreamError, ResultUnknownTimeout, "unexpected"} {
		if state.Usable() {
			t.Errorf("%q must not consume a logical search", state)
		}
	}
	if ResultState("unexpected").Known() {
		t.Fatal("unknown state must not become a metric label")
	}
}
