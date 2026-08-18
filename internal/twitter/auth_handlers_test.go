// Authentication HTTP-handler tests.
package twitter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestHandleAuthenticate_Healthy requires a healthy service to return its
// current state with HTTP 200.
func TestHandleAuthenticate_Healthy(t *testing.T) {
	fake := &fakeBrowser{}
	svc, _ := newTestService(t, fake)
	svc.SetState(StateHealthy, "verified")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/authenticate", nil)
	svc.handleAuthenticate(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["state"] != "healthy" {
		t.Errorf("state = %v, want healthy", body["state"])
	}
}

// TestHandleAuthenticate_UnauthenticatedWithReauthConfig verifies configured
// operator instructions are returned with the unauthenticated state.
func TestHandleAuthenticate_UnauthenticatedWithReauthConfig(t *testing.T) {
	fake := &fakeBrowser{}
	svc := NewService(fake, ServiceOptions{
		CookieFile:    filepath.Join(t.TempDir(), "twitter_cookies.json"),
		ReauthURL:     "http://twitter-vnc.luv:5900",
		ReauthCommand: "make twitter-vnc-up",
	})
	svc.SetState(StateUnauthenticated, "verify failed: session expired")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/authenticate", nil)
	svc.handleAuthenticate(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["action_required"] != "manual_reauth" {
		t.Errorf("action_required = %v", body["action_required"])
	}
	if body["reauth_url"] != "http://twitter-vnc.luv:5900" {
		t.Errorf("reauth_url = %v", body["reauth_url"])
	}
	if body["reauth_command"] != "make twitter-vnc-up" {
		t.Errorf("reauth_command = %v", body["reauth_command"])
	}
	msg, _ := body["message"].(string)
	if msg == "" {
		t.Error("message should be non-empty")
	}
}

// TestHandleAuthenticate_UnauthenticatedNoConfig verifies zero-value operator
// instructions produce a valid fallback response.
func TestHandleAuthenticate_UnauthenticatedNoConfig(t *testing.T) {
	fake := &fakeBrowser{}
	svc, _ := newTestService(t, fake)
	svc.SetState(StateUnauthenticated, "no cookies")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/authenticate", nil)
	svc.handleAuthenticate(rec, req)

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if _, hasURL := body["reauth_url"]; hasURL {
		t.Error("reauth_url should be absent when env unset")
	}
	if body["message"] == "" {
		t.Error("fallback message should be present")
	}
}

// TestHandleAuthVerify_POSTOnly — GET returns 405 (would otherwise
// let health-check pollers accidentally trigger x.com traffic).
func TestHandleAuthVerify_POSTOnly(t *testing.T) {
	fake := &fakeBrowser{}
	svc, _ := newTestService(t, fake)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/verify", nil)
	svc.handleAuthVerify(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be rejected: code = %d", rec.Code)
	}
	if atomic.LoadInt64(&fake.verifyCalls) != 0 {
		t.Errorf("GET should not trigger verify")
	}
}

// TestHandleAuthVerify_ForcesVerify — POST invokes EnsureAuthenticated;
// success flips state to healthy and returns 200.
func TestHandleAuthVerify_ForcesVerify(t *testing.T) {
	fake := &fakeBrowser{verifyErr: nil}
	svc, cookieFile := newTestService(t, fake)
	if err := WriteBackup(cookieFile, validCookies(), time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/verify", nil)
	svc.handleAuthVerify(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if state, _ := svc.State(); state != StateHealthy {
		t.Errorf("state = %s, want healthy", state)
	}
	if atomic.LoadInt64(&fake.verifyCalls) < 1 {
		t.Errorf("expected verify to be invoked")
	}
}
