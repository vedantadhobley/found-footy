// Twitter service state machine + HTTP surface. Wraps a Browser and
// exposes /health + /status. T/a scope: prove Playwright-Go + Firefox
// works in Docker. Search + auth endpoints land in T/b + T/c.
package twitter

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// State reports the current service condition. Transitions:
//
//	starting → healthy (cookies loaded, session verified)
//	starting → unauthenticated (cookies missing/expired)
//	healthy → unauthenticated (auth expired mid-run)
//	any → failed (browser dead / unrecoverable)
//
// StateLoading is transient — used only while a cookie reload from
// the shared backup file is in flight so /status can distinguish
// "actively adopting fresh cookies" from a stuck starting state.
type State string

const (
	StateStarting        State = "starting"
	StateLoading         State = "loading" // reloading cookies from shared backup file
	StateHealthy         State = "healthy"
	StateUnauthenticated State = "unauthenticated"
	StateFailed          State = "failed" // browser dead / unrecoverable
)

// ServiceOptions configures a Service. Defaults land in NewService
// so callers can pass a zero value for T/a-style usage.
type ServiceOptions struct {
	// CookieFile is the shared backup file path. Both this instance
	// and every other instance (headless + VNC) read from and write
	// to it. Default: /config/twitter_cookies.json.
	CookieFile string

	// WarmPathTTL is the window during which a prior VerifySession
	// success lets EnsureAuthenticated skip the ~3-4s x.com round-trip
	// and return healthy immediately. Default: 60s.
	WarmPathTTL time.Duration

	// VerifyTimeout bounds a single VerifySession navigation. Default:
	// 15s — Twitter's SPA can be slow to render the logged-in indicator
	// under load; anything shorter false-flags healthy sessions as
	// expired.
	VerifyTimeout time.Duration
}

// Service is the state-machine wrapper around a sessionBrowser
// (concrete *Browser in production; fakes in tests). Exposes HTTP
// handlers via ServeMux registration; thread-safe via mu.
//
// Auth-flow state (cookieFile, warmPathTTL, lastLoadedMtime,
// lastFingerprint, busy) is on the same struct but the operations
// that touch it live in auth.go — cleaner file-level split without
// splitting the concurrency boundary.
type Service struct {
	browser sessionBrowser

	// Config (immutable post-construction — no lock needed).
	cookieFile    string
	warmPathTTL   time.Duration
	verifyTimeout time.Duration

	// authMu serializes EnsureAuthenticated calls so we don't fire
	// multiple concurrent VerifySession round-trips on the same browser.
	// Held only during auth-flow work; /status reads never block on it.
	authMu sync.Mutex

	mu              sync.RWMutex
	state           State
	stateReason     string
	lastAuthCheck   time.Time // last VerifySession attempt (success or fail)
	lastLoadedMtime time.Time // mtime of cookie file at last successful reload
	lastFingerprint CookieFingerprint
	busy            bool // authMu is currently held (surface via /status)
	startedAt       time.Time
}

// NewService constructs a Service in StateStarting. The browser
// argument must already be initialized (NewBrowser succeeded).
// Accepts sessionBrowser rather than *Browser so tests can inject
// a fake without launching Playwright.
func NewService(b sessionBrowser, opts ServiceOptions) *Service {
	if opts.CookieFile == "" {
		opts.CookieFile = "/config/twitter_cookies.json"
	}
	if opts.WarmPathTTL == 0 {
		opts.WarmPathTTL = 60 * time.Second
	}
	if opts.VerifyTimeout == 0 {
		opts.VerifyTimeout = 15 * time.Second
	}
	return &Service{
		browser:       b,
		cookieFile:    opts.CookieFile,
		warmPathTTL:   opts.WarmPathTTL,
		verifyTimeout: opts.VerifyTimeout,
		state:         StateStarting,
		startedAt:     time.Now().UTC(),
	}
}

// SetState transitions the service to a new state with an
// optional human-readable reason (surfaced in /status responses).
// Concurrent-safe.
func (s *Service) SetState(newState State, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = newState
	s.stateReason = reason
	if newState == StateHealthy || newState == StateUnauthenticated {
		s.lastAuthCheck = time.Now().UTC()
	}
}

// State returns the current state + reason without holding the lock
// for callers.
func (s *Service) State() (State, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state, s.stateReason
}

// RegisterHandlers installs the service's HTTP surface on mux.
//
//	GET  /health        HTTP 200 iff state == healthy. Orchestrator probe.
//	GET  /status        Always 200; JSON snapshot for scaler + dashboard.
//	POST /search        (T/c) fires a search against the current session.
//	GET  /authenticate  Reports auth state; on unauthenticated returns
//	                    503 + reauth instructions (VNC container URL,
//	                    docker command to start it). Operator-facing.
//	POST /auth/verify   Forces a full VerifySession bypassing the warm
//	                    path. Used by the VNC container after manual
//	                    login to prompt other instances to reload
//	                    cookies from the shared backup file.
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/authenticate", s.handleAuthenticate)
	mux.HandleFunc("/auth/verify", s.handleAuthVerify)
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	state, reason := s.State()
	if state == StateHealthy {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "healthy",
		})
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": string(state),
		"reason": reason,
	})
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"state":             string(s.state),
		"reason":            s.stateReason,
		"started_at":        s.startedAt,
		"last_auth_check":   s.lastAuthCheck,
		"last_loaded_mtime": s.lastLoadedMtime,
		"cookie_fingerprint": s.lastFingerprint.Hex(),
		"busy":              s.busy,
	})
}
