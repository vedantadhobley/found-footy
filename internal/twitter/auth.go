// auth.go — auth flow methods on *Service. EnsureAuthenticated is
// the single entry point that every search / status-consumer calls
// before trusting the browser session. BackupCookies persists fresh
// cookies to the shared file after successful work.
//
// Flow (EnsureAuthenticated):
//
//  1. mtime check — if the shared cookie file's mtime is newer than
//     what this instance last loaded, another instance (or the VNC
//     container's manual re-auth) wrote fresh cookies; reload them
//     into the browser context before verifying.
//  2. warm path — if the last successful VerifySession was within
//     warmPathTTL AND no reload happened, skip the ~3-4s round-trip
//     to x.com/home and return healthy immediately. Kills 95%+ of
//     the per-search verify cost when the service is hot.
//  3. full verify — navigate x.com/home, look for the account
//     switcher button. Success → StateHealthy; failure →
//     StateUnauthenticated (operator alerted, VNC container needs
//     to be started for manual re-auth).
//
// Concurrency: serialized by authMu. Concurrent callers block; the
// second call typically hits the warm path (first call just refreshed
// lastAuthCheck) and returns immediately.
package twitter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// vncURLEnv is the env var pointing at the VNC container's noVNC
// endpoint. Surfaced in /authenticate responses so operators can
// click through to log in without hunting for the URL. Default is
// empty — deployment sets it in .env.
const vncURLEnv = "TWITTER_VNC_URL"

// vncStartCmdEnv is the docker/make command the operator runs to
// start the VNC container when it's not running (per decisions.md
// 2026-07-21 — VNC is opt-in, not always on). Surfaced verbatim so
// operators can copy-paste. Default is empty.
const vncStartCmdEnv = "TWITTER_VNC_START_CMD"

// ErrUnauthenticated signals that the service's cookies are stale
// or missing and the browser session is not logged in. Callers
// (search handlers, health checks) use errors.Is against this to
// distinguish "no session, human needs to re-auth via VNC" from
// browser/network errors.
var ErrUnauthenticated = errors.New("twitter: session unauthenticated")

// EnsureAuthenticated runs the mtime → warm-path → verify sequence
// and returns nil if the browser session is authenticated.
//
// On authentication failure (verify returned no logged-in indicator),
// returns an error wrapping ErrUnauthenticated and transitions the
// service to StateUnauthenticated. Callers should propagate the
// signal so downstream orchestrators (scaler, operator alerts) know
// the fleet is degraded.
//
// On infra failure (cookie file corrupt, browser dead), returns a
// wrapping error WITHOUT ErrUnauthenticated — the session state is
// unknown, not proven bad. Failed browser transitions to StateFailed.
func (s *Service) EnsureAuthenticated(ctx context.Context) error {
	s.authMu.Lock()
	s.setBusy(true)
	defer func() {
		s.setBusy(false)
		s.authMu.Unlock()
	}()

	// Snapshot the state we need under the read lock. Everything past
	// this point can read these local copies without re-locking; the
	// only writes happen via s.SetState (own lock) or explicit s.mu
	// sections below.
	s.mu.RLock()
	lastLoadedMtime := s.lastLoadedMtime
	lastAuthCheck := s.lastAuthCheck
	state := s.state
	s.mu.RUnlock()

	// Step 1: mtime check. If the shared file has been rewritten since
	// we last loaded, adopt the fresh cookies before deciding anything.
	reloaded, err := s.maybeReloadCookies(lastLoadedMtime)
	if err != nil {
		// Cookie file is present but broken (bad JSON, no auth_token,
		// browser refused to add cookies). Session state is unknown —
		// don't lie about it. Return error; state stays wherever it was.
		return fmt.Errorf("twitter.EnsureAuthenticated: reload cookies: %w", err)
	}

	// Step 2: warm path. If we didn't just reload and the last verify
	// was recent + successful, trust it — skip the round-trip.
	if !reloaded && state == StateHealthy && time.Since(lastAuthCheck) < s.warmPathTTL {
		return nil
	}

	// Step 3: full verify. Navigate x.com/home; look for the logged-in
	// indicator. Bounded by verifyTimeout; caller's ctx cancellation
	// aborts early.
	verifyCtx, cancel := context.WithTimeout(ctx, s.verifyTimeout)
	defer cancel()
	if err := s.browser.VerifySession(verifyCtx, s.verifyTimeout); err != nil {
		reason := "verify failed: " + err.Error()
		s.SetState(StateUnauthenticated, reason)
		return fmt.Errorf("twitter.EnsureAuthenticated: %w: %v", ErrUnauthenticated, err)
	}
	s.SetState(StateHealthy, "verified")
	return nil
}

// maybeReloadCookies checks the backup file's mtime. If it's newer
// than lastLoadedMtime, reads the file and pushes the cookies into
// the browser context, updating lastLoadedMtime + lastFingerprint.
//
// Returns (reloaded, err):
//   - (false, nil) — file missing or mtime unchanged; caller proceeds
//     with existing browser cookies.
//   - (true, nil) — fresh cookies successfully loaded into browser.
//   - (_, err) — file exists but read/parse/replace failed. Caller
//     treats as an infrastructure error, not an auth failure.
func (s *Service) maybeReloadCookies(lastLoadedMtime time.Time) (bool, error) {
	mtime, err := BackupFileMtime(s.cookieFile)
	if err != nil {
		// Missing file is normal on first boot before anyone has logged
		// in via VNC. Not an error — just nothing to reload.
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", s.cookieFile, err)
	}
	if !mtime.After(lastLoadedMtime) {
		return false, nil
	}

	s.SetState(StateLoading, "reloading cookies from shared backup file")

	cookies, _, err := ReadBackup(s.cookieFile)
	if err != nil {
		return false, fmt.Errorf("read backup: %w", err)
	}
	if err := s.browser.ReplaceCookies(cookies); err != nil {
		// Browser is in a weird state. Escalate to failed — process
		// restart is cleaner than trying to recover an unknown context.
		s.SetState(StateFailed, "replace cookies failed: "+err.Error())
		return false, fmt.Errorf("replace cookies: %w", err)
	}

	fp := Fingerprint(cookies)
	s.mu.Lock()
	s.lastLoadedMtime = mtime
	s.lastFingerprint = fp
	s.mu.Unlock()
	return true, nil
}

// BackupCookies grabs the current browser cookies, computes their
// fingerprint, and — if it's changed since the last write — persists
// them to the shared backup file. Callers invoke this after every
// successful search so Twitter's rotated csrf tokens end up on disk
// for other instances to pick up.
//
// Fingerprint dedupe means quiet windows (Twitter didn't rotate
// anything) skip the write entirely — no mtime change, other
// instances don't get spuriously woken up to reload identical
// cookies.
//
// Refuses to write if the browser's current cookies don't include
// auth_token (WriteBackup's guard fires). Callers can log this as
// "browser lost auth mid-search" and trigger a re-auth path.
func (s *Service) BackupCookies(_ context.Context) error {
	cookies, err := s.browser.GetCookies()
	if err != nil {
		return fmt.Errorf("twitter.BackupCookies: %w", err)
	}
	// Filter to Twitter-domain cookies for the fingerprint — same set
	// we'd persist. Prevents unrelated cookies (from a stray redirect)
	// from constantly flipping the fingerprint and forcing writes.
	filtered := filterToTwitterDomain(cookies)
	fp := Fingerprint(filtered)

	s.mu.RLock()
	unchanged := fp == s.lastFingerprint
	s.mu.RUnlock()
	if unchanged {
		return nil
	}

	if err := WriteBackup(s.cookieFile, cookies, time.Now().UTC()); err != nil {
		return fmt.Errorf("twitter.BackupCookies: %w", err)
	}

	// Record the fingerprint + the mtime we just produced. Reading the
	// mtime back from stat is more accurate than time.Now() (filesystems
	// may round) and matches what other instances will see.
	mtime, statErr := BackupFileMtime(s.cookieFile)
	s.mu.Lock()
	s.lastFingerprint = fp
	if statErr == nil {
		s.lastLoadedMtime = mtime
	}
	s.mu.Unlock()
	return nil
}

// setBusy toggles the busy flag. Held only during EnsureAuthenticated
// so /status callers can distinguish "quiescent" from "actively
// checking session." Under s.mu (not authMu) — /status readers should
// see the current value without waiting on the auth flow.
func (s *Service) setBusy(v bool) {
	s.mu.Lock()
	s.busy = v
	s.mu.Unlock()
}

// handleAuthenticate is the operator-facing endpoint that reports
// the current auth state + instructions to re-auth.
//
// If healthy → 200 { state: "healthy" }.
//
// If unauthenticated → 503 { state, action_required, reauth_url,
// reauth_command, message } — a machine-readable envelope the
// vedanta-systems portal can render as an alert card, plus a
// human-readable message an operator can act on directly. Includes
// the VNC container's URL and the docker command to start it
// (per decisions.md 2026-07-21: VNC container is opt-in, not always
// running).
//
// If starting/loading/failed → 503 with the state but no reauth
// instructions — either not our problem yet (transient) or a
// process restart is the fix (failed).
func (s *Service) handleAuthenticate(w http.ResponseWriter, r *http.Request) {
	state, reason := s.State()
	w.Header().Set("Content-Type", "application/json")

	if state == StateHealthy {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"state": string(state),
		})
		return
	}

	resp := map[string]any{
		"state":  string(state),
		"reason": reason,
	}
	if state == StateUnauthenticated {
		vncURL := os.Getenv(vncURLEnv)
		startCmd := os.Getenv(vncStartCmdEnv)
		resp["action_required"] = "manual_reauth"
		if vncURL != "" {
			resp["reauth_url"] = vncURL
		}
		if startCmd != "" {
			resp["reauth_command"] = startCmd
		}
		resp["message"] = buildReauthMessage(vncURL, startCmd)
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(resp)
}

// buildReauthMessage assembles the human-readable re-auth instruction
// from whatever URL + command are configured. Handles partial config
// (only URL set, only command set, neither set) cleanly.
func buildReauthMessage(vncURL, startCmd string) string {
	switch {
	case vncURL != "" && startCmd != "":
		return fmt.Sprintf("Twitter session expired. Start the VNC container: `%s`. Then log in at %s.", startCmd, vncURL)
	case vncURL != "":
		return fmt.Sprintf("Twitter session expired. Log in via VNC at %s (start the container first if it's not running).", vncURL)
	case startCmd != "":
		return fmt.Sprintf("Twitter session expired. Start the VNC container (`%s`) and log in.", startCmd)
	default:
		return "Twitter session expired. Start the VNC container and log in manually — see docs/rebuild/proposals/twitter-port.md."
	}
}

// handleAuthVerify forces a full EnsureAuthenticated cycle. The VNC
// container calls this after a successful manual login to prompt the
// headless fleet to reload cookies + verify (rather than waiting for
// the next warm-path expiry). POST-only — GET responses would let
// dashboard health probes accidentally trigger real x.com traffic.
//
// Response mirrors handleAuthenticate: 200 { state: "healthy" } on
// success, 503 with reason on failure.
func (s *Service) handleAuthVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	err := s.EnsureAuthenticated(r.Context())
	state, reason := s.State()
	w.Header().Set("Content-Type", "application/json")

	if err == nil && state == StateHealthy {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"state": string(state),
		})
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	resp := map[string]any{
		"state":  string(state),
		"reason": reason,
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	_ = json.NewEncoder(w).Encode(resp)
}
