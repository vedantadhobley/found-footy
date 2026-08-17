// Unit tests for auth.go — EnsureAuthenticated, BackupCookies,
// handleAuthenticate, handleAuthVerify. Uses a fake sessionBrowser
// so no Playwright / Firefox needed.
package twitter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// fakeBrowser is a test double for the sessionBrowser interface.
// Behavior is controlled via public fields set per-test; each method
// increments its call counter so assertions can inspect the flow.
type fakeBrowser struct {
	// Behavior knobs — set per test.
	verifyErr        error
	replaceErr       error
	getCookiesResult []Cookie
	getCookiesErr    error
	done             chan struct{}

	// Call counters (atomic — tests exercise concurrent auth flows).
	verifyCalls  int64
	replaceCalls int64
	getCalls     int64

	// Captured — what was passed on the most recent call.
	mu              sync.Mutex
	lastReplacedSet []Cookie
}

func (f *fakeBrowser) Done() <-chan struct{} { return f.done }

func (f *fakeBrowser) VerifySession(_ context.Context, _ time.Duration) error {
	atomic.AddInt64(&f.verifyCalls, 1)
	return f.verifyErr
}

func (f *fakeBrowser) ReplaceCookies(cs []Cookie) error {
	atomic.AddInt64(&f.replaceCalls, 1)
	f.mu.Lock()
	f.lastReplacedSet = append([]Cookie(nil), cs...)
	f.mu.Unlock()
	return f.replaceErr
}

func (f *fakeBrowser) GetCookies() ([]Cookie, error) {
	atomic.AddInt64(&f.getCalls, 1)
	if f.getCookiesErr != nil {
		return nil, f.getCookiesErr
	}
	return f.getCookiesResult, nil
}

// Navigate isn't used by the auth flow; return nil to satisfy the
// interface. Search tests that need Navigate go through the real
// *Browser via testcontainers, not this fake.
func (f *fakeBrowser) Navigate(_ context.Context, _ string, _ time.Duration) (playwright.Page, error) {
	return nil, nil
}

// newTestService builds a Service wired to fake + a tempdir cookie
// path with short-tuned timeouts for fast tests.
func newTestService(t *testing.T, fake *fakeBrowser) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "twitter_cookies.json")
	svc := NewService(fake, ServiceOptions{
		CookieFile:    cookieFile,
		WarmPathTTL:   500 * time.Millisecond,
		VerifyTimeout: 2 * time.Second,
	})
	return svc, cookieFile
}

// TestStatusExposesBuildIdentity guards the Twitter half of the production
// release verifier. Worker and API expose the same values through Prometheus.
func TestStatusExposesBuildIdentity(t *testing.T) {
	fake := &fakeBrowser{}
	want := BuildInfo{
		GitSHA:   "0123456789abcdef",
		BuiltAt:  "2026-08-17T12:34:56Z",
		ImageTag: "0123456789abcdef",
	}
	svc := NewService(fake, ServiceOptions{Build: want})
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response struct {
		Build BuildInfo `json:"build"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	if response.Build != want {
		t.Fatalf("build = %+v, want %+v", response.Build, want)
	}
}

// TestEnsureAuthenticated_FirstBoot_NoCookieFile — cookie file missing,
// verify fails (no session). Service goes StateUnauthenticated.
func TestEnsureAuthenticated_FirstBoot_NoCookieFile(t *testing.T) {
	fake := &fakeBrowser{verifyErr: errors.New("logged-in indicator missing")}
	svc, _ := newTestService(t, fake)

	err := svc.EnsureAuthenticated(context.Background())
	if err == nil {
		t.Fatal("expected error on first-boot with no cookies")
	}
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}
	if state, _ := svc.State(); state != StateUnauthenticated {
		t.Errorf("state = %s, want unauthenticated", state)
	}
	if atomic.LoadInt64(&fake.verifyCalls) != 1 {
		t.Errorf("verify calls = %d, want 1", fake.verifyCalls)
	}
	if atomic.LoadInt64(&fake.replaceCalls) != 0 {
		t.Errorf("no reload should happen when file missing; replaceCalls = %d", fake.replaceCalls)
	}
}

// TestEnsureAuthenticated_HappyPath — cookie file present, reload
// succeeds, verify succeeds. Service goes StateHealthy.
func TestEnsureAuthenticated_HappyPath(t *testing.T) {
	fake := &fakeBrowser{verifyErr: nil}
	svc, cookieFile := newTestService(t, fake)

	if err := WriteBackup(cookieFile, validCookies(), time.Now()); err != nil {
		t.Fatalf("seed cookies: %v", err)
	}

	if err := svc.EnsureAuthenticated(context.Background()); err != nil {
		t.Fatalf("EnsureAuthenticated: %v", err)
	}
	if state, _ := svc.State(); state != StateHealthy {
		t.Errorf("state = %s, want healthy", state)
	}
	if atomic.LoadInt64(&fake.replaceCalls) != 1 {
		t.Errorf("replace calls = %d, want 1 (fresh file triggers reload)", fake.replaceCalls)
	}
	if atomic.LoadInt64(&fake.verifyCalls) != 1 {
		t.Errorf("verify calls = %d, want 1", fake.verifyCalls)
	}
}

// TestEnsureAuthenticated_WarmPath — second call within warmPathTTL
// skips both reload and verify.
func TestEnsureAuthenticated_WarmPath(t *testing.T) {
	fake := &fakeBrowser{verifyErr: nil}
	svc, cookieFile := newTestService(t, fake)
	if err := WriteBackup(cookieFile, validCookies(), time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First call: reload + verify.
	if err := svc.EnsureAuthenticated(context.Background()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	v1 := atomic.LoadInt64(&fake.verifyCalls)
	r1 := atomic.LoadInt64(&fake.replaceCalls)

	// Second call within TTL: no verify, no reload.
	if err := svc.EnsureAuthenticated(context.Background()); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if atomic.LoadInt64(&fake.verifyCalls) != v1 {
		t.Errorf("warm-path did not skip verify: v1=%d v2=%d", v1, fake.verifyCalls)
	}
	if atomic.LoadInt64(&fake.replaceCalls) != r1 {
		t.Errorf("warm-path did not skip reload: r1=%d r2=%d", r1, fake.replaceCalls)
	}
}

// TestEnsureAuthenticated_WarmPathExpires — after warmPathTTL, verify
// runs again even if cookies haven't changed on disk.
func TestEnsureAuthenticated_WarmPathExpires(t *testing.T) {
	fake := &fakeBrowser{verifyErr: nil}
	svc, cookieFile := newTestService(t, fake)
	if err := WriteBackup(cookieFile, validCookies(), time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.EnsureAuthenticated(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	v1 := atomic.LoadInt64(&fake.verifyCalls)

	// Wait past the TTL (test config: 500ms).
	time.Sleep(700 * time.Millisecond)

	if err := svc.EnsureAuthenticated(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if atomic.LoadInt64(&fake.verifyCalls) != v1+1 {
		t.Errorf("expected verify to re-run after TTL expiry: v1=%d v2=%d", v1, fake.verifyCalls)
	}
}

// TestEnsureAuthenticated_ExternalReload — file mtime advances (e.g.,
// VNC container wrote new cookies). Next EnsureAuthenticated call
// reloads even inside the warm-path window.
func TestEnsureAuthenticated_ExternalReload(t *testing.T) {
	fake := &fakeBrowser{verifyErr: nil}
	svc, cookieFile := newTestService(t, fake)
	if err := WriteBackup(cookieFile, validCookies(), time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.EnsureAuthenticated(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	r1 := atomic.LoadInt64(&fake.replaceCalls)
	v1 := atomic.LoadInt64(&fake.verifyCalls)

	// External writer rotates ct0 and rewrites the file.
	time.Sleep(1100 * time.Millisecond) // ensure mtime advances on 1s-granular fs
	c := validCookies()
	c[1].Value = "csrf_rotated_by_other_instance"
	if err := WriteBackup(cookieFile, c, time.Now()); err != nil {
		t.Fatalf("external write: %v", err)
	}

	if err := svc.EnsureAuthenticated(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if atomic.LoadInt64(&fake.replaceCalls) != r1+1 {
		t.Errorf("expected reload on mtime advance: r1=%d r2=%d", r1, fake.replaceCalls)
	}
	if atomic.LoadInt64(&fake.verifyCalls) != v1+1 {
		t.Errorf("expected verify after reload: v1=%d v2=%d", v1, fake.verifyCalls)
	}

	// Fake received the rotated ct0 value.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	found := false
	for _, ck := range fake.lastReplacedSet {
		if ck.Name == "ct0" && ck.Value == "csrf_rotated_by_other_instance" {
			found = true
		}
	}
	if !found {
		t.Errorf("browser did not receive the rotated cookie set: %+v", fake.lastReplacedSet)
	}
}

// TestEnsureAuthenticated_VerifyFailsAfterReload — cookies loaded
// successfully but verify fails (Twitter rejected the session).
// Service goes StateUnauthenticated with a wrapping ErrUnauthenticated.
func TestEnsureAuthenticated_VerifyFailsAfterReload(t *testing.T) {
	fake := &fakeBrowser{verifyErr: errors.New("indicator missing")}
	svc, cookieFile := newTestService(t, fake)
	if err := WriteBackup(cookieFile, validCookies(), time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := svc.EnsureAuthenticated(context.Background())
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated wrap, got %v", err)
	}
	if state, _ := svc.State(); state != StateUnauthenticated {
		t.Errorf("state = %s, want unauthenticated", state)
	}
}

// TestEnsureAuthenticated_UnreadableCookieFile_ProceedsToVerify —
// a cookie file that exists but is corrupt (bad JSON, missing
// auth_token, empty) is NOT a hard error. maybeReloadCookies bumps
// lastLoadedMtime so we don't retry the same broken file every call,
// then EnsureAuthenticated falls through to verify — verify decides
// whether the current browser state is still authed (unlikely, but
// possible if browser has stale-but-valid cookies from a prior
// session) or should flip to unauth.
//
// This is the code path that lets the smoke-test flow "move cookie
// file aside → restart container → operator logs in via VNC" work:
// stopped container gets restarted with an unreadable/missing
// shared file, unauth is reached via verify failure, operator can
// re-auth without the state getting stuck at StateLoading.
func TestEnsureAuthenticated_UnreadableCookieFile_ProceedsToVerify(t *testing.T) {
	fake := &fakeBrowser{verifyErr: errors.New("indicator missing")}
	svc, cookieFile := newTestService(t, fake)

	// Seed a corrupt file (bad JSON) so ReadBackup errors but
	// BackupFileMtime succeeds.
	if err := os.WriteFile(cookieFile, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}

	err := svc.EnsureAuthenticated(context.Background())
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated (verify decides auth outcome), got %v", err)
	}
	if state, _ := svc.State(); state != StateUnauthenticated {
		t.Errorf("state = %s, want unauthenticated (not stuck at loading)", state)
	}
	if atomic.LoadInt64(&fake.verifyCalls) != 1 {
		t.Errorf("verify should run despite bad cookie file: got %d calls", fake.verifyCalls)
	}
	if atomic.LoadInt64(&fake.replaceCalls) != 0 {
		t.Errorf("replace should NOT run when ReadBackup fails: got %d calls", fake.replaceCalls)
	}
}

// TestEnsureAuthenticated_UnreadableCookieFile_MtimeAdvanced —
// second EnsureAuthenticated call with the same broken file should
// skip the ReadBackup retry (mtime not advanced past the one we
// recorded on the first failed read). Prevents log floods + repeated
// wasted syscalls on a persistently-broken file.
func TestEnsureAuthenticated_UnreadableCookieFile_MtimeAdvanced(t *testing.T) {
	fake := &fakeBrowser{verifyErr: errors.New("indicator missing")}
	svc, cookieFile := newTestService(t, fake)

	if err := os.WriteFile(cookieFile, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First call absorbs the ReadBackup failure + verify runs.
	_ = svc.EnsureAuthenticated(context.Background())
	v1 := atomic.LoadInt64(&fake.verifyCalls)

	// Wait past warm-path TTL so verify runs again.
	time.Sleep(700 * time.Millisecond)

	// Second call: mtime unchanged relative to what we recorded, so
	// no retry of ReadBackup — but verify still fires because auth
	// state is unauth and warm-path expired.
	_ = svc.EnsureAuthenticated(context.Background())
	if atomic.LoadInt64(&fake.verifyCalls) != v1+1 {
		t.Errorf("verify should re-run after TTL expiry: got %d calls (want %d)", fake.verifyCalls, v1+1)
	}
}

// TestEnsureAuthenticated_ReplaceCookiesFails — browser's cookie
// replace failed (browser is wedged). Service escalates to
// StateFailed — infra issue, not auth issue.
func TestEnsureAuthenticated_ReplaceCookiesFails(t *testing.T) {
	fake := &fakeBrowser{replaceErr: errors.New("browser context closed")}
	svc, cookieFile := newTestService(t, fake)
	if err := WriteBackup(cookieFile, validCookies(), time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := svc.EnsureAuthenticated(context.Background())
	if err == nil {
		t.Fatal("expected error when browser replace fails")
	}
	if errors.Is(err, ErrUnauthenticated) {
		t.Errorf("browser-death error should NOT wrap ErrUnauthenticated: %v", err)
	}
	if state, _ := svc.State(); state != StateFailed {
		t.Errorf("state = %s, want failed", state)
	}
}

// TestEnsureAuthenticated_ConcurrentCalls_SerializedByAuthMu — five
// concurrent callers should produce exactly one verify call (the
// first grabs the mutex, the other four fall through to warm-path
// after it releases).
func TestEnsureAuthenticated_ConcurrentCalls_SerializedByAuthMu(t *testing.T) {
	fake := &fakeBrowser{verifyErr: nil}
	svc, cookieFile := newTestService(t, fake)
	if err := WriteBackup(cookieFile, validCookies(), time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const N = 5
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.EnsureAuthenticated(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent EnsureAuthenticated failed: %v", err)
	}

	// First call does the real work; subsequent callers see
	// StateHealthy + fresh lastAuthCheck and take the warm path.
	if got := atomic.LoadInt64(&fake.verifyCalls); got != 1 {
		t.Errorf("verify calls = %d, want 1 (warm-path should dedupe concurrent callers)", got)
	}
}

// TestBackupCookies_FingerprintDedupe — writing the same cookie set
// twice only produces one file (fingerprint unchanged the second
// time). The write-side counter proves the atomic-file logic wasn't
// exercised on the no-op call.
func TestBackupCookies_FingerprintDedupe(t *testing.T) {
	fake := &fakeBrowser{getCookiesResult: validCookies()}
	svc, cookieFile := newTestService(t, fake)

	if err := svc.BackupCookies(context.Background()); err != nil {
		t.Fatalf("first backup: %v", err)
	}
	firstMtime, err := BackupFileMtime(cookieFile)
	if err != nil {
		t.Fatalf("mtime after first: %v", err)
	}

	time.Sleep(1100 * time.Millisecond) // to detect an unwanted rewrite

	if err := svc.BackupCookies(context.Background()); err != nil {
		t.Fatalf("second backup: %v", err)
	}
	secondMtime, err := BackupFileMtime(cookieFile)
	if err != nil {
		t.Fatalf("mtime after second: %v", err)
	}
	if !secondMtime.Equal(firstMtime) {
		t.Errorf("unchanged cookies caused a rewrite: first=%v second=%v", firstMtime, secondMtime)
	}
}

// TestBackupCookies_RewriteOnRotation — Twitter rotating ct0 changes
// the fingerprint, which forces a write. The file's mtime must
// advance so other instances see the change on their next check.
func TestBackupCookies_RewriteOnRotation(t *testing.T) {
	initial := validCookies()
	fake := &fakeBrowser{getCookiesResult: initial}
	svc, cookieFile := newTestService(t, fake)

	if err := svc.BackupCookies(context.Background()); err != nil {
		t.Fatalf("first backup: %v", err)
	}
	firstMtime, _ := BackupFileMtime(cookieFile)

	time.Sleep(1100 * time.Millisecond)

	rotated := validCookies()
	rotated[1].Value = "csrf_ROTATED"
	fake.getCookiesResult = rotated
	if err := svc.BackupCookies(context.Background()); err != nil {
		t.Fatalf("rotated backup: %v", err)
	}
	secondMtime, _ := BackupFileMtime(cookieFile)
	if !secondMtime.After(firstMtime) {
		t.Errorf("rotation should force rewrite: first=%v second=%v", firstMtime, secondMtime)
	}

	// File actually contains the rotated value.
	got, _, err := ReadBackup(cookieFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, c := range got {
		if c.Name == "ct0" && c.Value != "csrf_ROTATED" {
			t.Errorf("persisted ct0 = %q, want csrf_ROTATED", c.Value)
		}
	}
}

// TestHandleAuthenticate_Healthy — state healthy → 200 with state field.
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

// TestSetState_EmitsAuthExpiredOnTransition — T/b.5 hardening #3.
// SetState(StateUnauthenticated) FROM a non-unauth state must emit
// action=twitter.auth_expired via the auditEmit callback. Grafana/Loki
// alerts key on this action string.
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
