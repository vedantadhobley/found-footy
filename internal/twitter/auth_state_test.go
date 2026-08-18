// Authentication state-transition and browser-exit tests.
package twitter

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// TestSetState_EmitsAuthExpiredOnTransition requires the first transition into
// unauthenticated state to emit the operator-facing audit action.
func TestSetState_EmitsAuthExpiredOnTransition(t *testing.T) {
	fake := &fakeBrowser{}
	var captured []map[string]any
	svc := NewService(fake, ServiceOptions{
		CookieFile:    filepath.Join(t.TempDir(), "cookies.json"),
		VerifyTimeout: time.Second,
		AuditEmit: func(action string, fields map[string]any) {
			f := make(map[string]any, len(fields)+1)
			for k, v := range fields {
				f[k] = v
			}
			f["_action"] = action
			captured = append(captured, f)
		},
	})

	// Initial state: StateStarting. Transition to Unauthenticated
	// should fire the emit.
	svc.SetState(StateUnauthenticated, "cookies missing")
	if len(captured) != 1 {
		t.Fatalf("expected 1 emit, got %d", len(captured))
	}
	if captured[0]["_action"] != "twitter.auth_expired" {
		t.Errorf("action = %v, want twitter.auth_expired", captured[0]["_action"])
	}
	if captured[0]["reason"] != "cookies missing" {
		t.Errorf("reason = %v, want %q", captured[0]["reason"], "cookies missing")
	}
	if captured[0]["previous_state"] != "starting" {
		t.Errorf("previous_state = %v, want starting", captured[0]["previous_state"])
	}
}

// TestSetState_DoesNotRe_EmitOnRepeatedUnauth — repeated
// SetState(StateUnauthenticated) calls should NOT re-emit. Alerting
// hooks would flap otherwise.
func TestSetState_DoesNotRe_EmitOnRepeatedUnauth(t *testing.T) {
	fake := &fakeBrowser{}
	emits := 0
	svc := NewService(fake, ServiceOptions{
		CookieFile:    filepath.Join(t.TempDir(), "cookies.json"),
		VerifyTimeout: time.Second,
		AuditEmit:     func(string, map[string]any) { emits++ },
	})

	svc.SetState(StateUnauthenticated, "first")
	svc.SetState(StateUnauthenticated, "second — should not re-emit")
	svc.SetState(StateUnauthenticated, "third — should not re-emit")
	if emits != 1 {
		t.Errorf("emits = %d, want 1 (only the transition fires)", emits)
	}
}

// TestSetState_EmitsOnHealthyToUnauthTransition — the primary
// production path: session was healthy, cookies expired, transition
// to unauth must alert.
func TestSetState_EmitsOnHealthyToUnauthTransition(t *testing.T) {
	fake := &fakeBrowser{}
	emits := 0
	svc := NewService(fake, ServiceOptions{
		CookieFile:    filepath.Join(t.TempDir(), "cookies.json"),
		VerifyTimeout: time.Second,
		AuditEmit:     func(string, map[string]any) { emits++ },
	})
	svc.SetState(StateHealthy, "startup verify passed")
	if emits != 0 {
		t.Errorf("healthy transition should NOT emit; got %d", emits)
	}
	svc.SetState(StateUnauthenticated, "session expired mid-run")
	if emits != 1 {
		t.Errorf("healthy → unauth should emit; got %d", emits)
	}
}

// TestSetState_NilEmitterIsSafe — auditEmit=nil (tests, standalone
// runs) must not panic. Fires quietly.
func TestSetState_NilEmitterIsSafe(t *testing.T) {
	fake := &fakeBrowser{}
	svc := NewService(fake, ServiceOptions{
		CookieFile:    filepath.Join(t.TempDir(), "cookies.json"),
		VerifyTimeout: time.Second,
		// AuditEmit deliberately unset.
	})
	// Should not panic.
	svc.SetState(StateUnauthenticated, "test")
	if state, _ := svc.State(); state != StateUnauthenticated {
		t.Errorf("state = %s, want unauthenticated", state)
	}
}

// TestBrowserExitTransitionsHealthAndEmitsOnce covers FF-017's process-loss
// boundary without launching Firefox. Closing the critical-child signal must
// make both state and /health fail before the container exits.
func TestBrowserExitTransitionsHealthAndEmitsOnce(t *testing.T) {
	fake := &fakeBrowser{done: make(chan struct{})}
	emits := make(chan string, 2)
	svc := NewService(fake, ServiceOptions{
		AuditEmit: func(action string, _ map[string]any) { emits <- action },
	})
	svc.SetState(StateHealthy, "verified")
	close(fake.done)

	deadline := time.Now().Add(time.Second)
	for {
		state, reason := svc.State()
		if state == StateFailed {
			if reason != "browser process exited" {
				t.Fatalf("failure reason = %q", reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not enter failed state after browser exit")
		}
		time.Sleep(time.Millisecond)
	}

	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, want 503", recorder.Code)
	}
	select {
	case action := <-emits:
		if action != "twitter.browser_failed" {
			t.Fatalf("audit action = %q", action)
		}
	case <-time.After(time.Second):
		t.Fatal("browser failure audit was not emitted")
	}
	// The main process also marks the state synchronously before exit. It must
	// not duplicate the watcher event.
	svc.MarkBrowserExited()
	svc.SetState(StateHealthy, "late auth completion must not revive a dead browser")
	if state, _ := svc.State(); state != StateFailed {
		t.Fatalf("terminal failed state was overwritten by %s", state)
	}
	select {
	case action := <-emits:
		t.Fatalf("duplicate audit action = %q", action)
	default:
	}
}

// TestBuildReauthMessage covers the four config-combinations.
func TestBuildReauthMessage(t *testing.T) {
	tests := []struct {
		url, cmd string
		contain  string
	}{
		{"http://vnc", "make up", "make up"},
		{"http://vnc", "", "http://vnc"},
		{"", "make up", "make up"},
		{"", "", "manually"},
	}
	for _, tt := range tests {
		got := buildReauthMessage(tt.url, tt.cmd)
		if got == "" {
			t.Errorf("empty message for (%q,%q)", tt.url, tt.cmd)
		}
		// Basic contains check — full string is docs, this is smoke.
		if !contains(got, tt.contain) {
			t.Errorf("message for (%q,%q) missing %q: %s", tt.url, tt.cmd, tt.contain, got)
		}
	}
}
