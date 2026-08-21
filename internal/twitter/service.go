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
	StateDegraded        State = "degraded" // browser/search availability was inconclusive; retry, do not request login
	StateUnauthenticated State = "unauthenticated"
	StateFailed          State = "failed" // browser dead / unrecoverable
)

// BuildInfo identifies the exact source and image that produced a service.
// The values are immutable for the process lifetime and surface through
// /status so the release command can verify Twitter alongside Go services
// that expose the shared Prometheus deploy-info gauge.
type BuildInfo struct {
	GitSHA   string `json:"git_sha"`
	BuiltAt  string `json:"built_at"`
	ImageTag string `json:"image_tag"`
}

// ServiceOptions configures a Service. Defaults land in NewService
// so callers can pass a zero value for T/a-style usage.
type ServiceOptions struct {
	// Build identifies the binary and image running this service. Production
	// sets every field; zero values remain useful for local unit tests.
	Build BuildInfo

	// CookieFile is the shared backup file path. Both this instance
	// and every other instance (headless + VNC) read from and write
	// to it. Default: /config/twitter_cookies.json.
	CookieFile string

	// ReauthURL and ReauthCommand are optional operator instructions returned
	// by /authenticate when the shared session expires.
	ReauthURL     string
	ReauthCommand string

	// WarmPathTTL is the window during which a prior VerifySession
	// success lets EnsureAuthenticated skip the ~3-4s x.com round-trip
	// and return healthy immediately. Default: 60s.
	WarmPathTTL time.Duration

	// VerifyTimeout bounds a single VerifySession navigation. Default:
	// 15s — Twitter's SPA can be slow to render the logged-in indicator
	// under load; anything shorter false-flags healthy sessions as
	// expired.
	VerifyTimeout time.Duration

	// PageLoadTimeout bounds the initial navigation to a search URL.
	// Default: 30s — matches Python's driver.set_page_load_timeout(30).
	// Rare that Twitter takes >30s to render the search shell; longer
	// waits usually indicate a wedged browser or upstream outage.
	PageLoadTimeout time.Duration

	// TweetFeedTimeout bounds the wait for the first article to appear
	// after navigation. Default: 10s. Only an actual timeout is a legitimate
	// no-feed result; locator, page, and browser failures remain errors.
	TweetFeedTimeout time.Duration

	// MaxScrolls caps the scroll loop. Default: 10 (matches Python).
	// Safety valve against runaway scrolling on abnormal feed shape.
	MaxScrolls int

	// ConsecutiveSeenStop is the fourth stop condition (NEW vs Python).
	// After N consecutive tweets whose IDs are in exclude_urls, stop
	// scrolling — late-attempt searches walk through mostly-known
	// tweets and this cuts the waste. Default: 3.
	ConsecutiveSeenStop int

	// ScrollJitterMin / ScrollJitterMax bound the random sleep between
	// scroll actions — non-uniform cadence to blunt bot-velocity detection
	// (baseline stealth #4 per twitter-port.md T/c). Default tightened to
	// 250ms / 500ms (2026-08-05): the anti-detection value is the variance,
	// not the pause length, so a tight range keeps the signal-blunting while
	// cutting per-scroll latency ~2.5×. Watch auth-health if scraping ramps.
	// Jitter interval is [min, max) — set both equal to disable for
	// deterministic tests.
	ScrollJitterMin time.Duration
	ScrollJitterMax time.Duration

	// AuditEmit is called on structured state transitions that need
	// external observability (Grafana/Loki alerting). It fires on
	// any-state → StateUnauthenticated as `twitter.auth_expired` and
	// any-state → StateFailed as `twitter.browser_failed`. Nil
	// (default) makes it a no-op.
	//
	// Contract: called synchronously from SetState after s.mu is released.
	// Implementations still should not block the state transition.
	AuditEmit func(action string, fields map[string]any)
}

// Defaults for the scroll/extract loop constants that don't come from
// the request. Kept as package-level names so tests can reference
// them without a Service instance.
const (
	defaultMaxAgeMinutes = 3 // per twitter-search-query.md D4
)

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
	cookieFile          string
	reauthURL           string
	reauthCommand       string
	warmPathTTL         time.Duration
	verifyTimeout       time.Duration
	pageLoadTimeout     time.Duration
	tweetFeedTimeout    time.Duration
	maxScrolls          int
	consecutiveSeenStop int
	scrollJitterMin     time.Duration
	scrollJitterMax     time.Duration
	auditEmit           func(string, map[string]any)
	build               BuildInfo

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
	busyCount       int // in-flight auth/search operations (surface as bool via /status)

	lastCookieBackupAttempt time.Time
	lastCookieBackupSuccess time.Time
	lastCookieBackupError   string
	lastCookieReloadAttempt time.Time
	lastCookieReloadSuccess time.Time
	lastCookieReloadError   string
	startedAt               time.Time
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
	if opts.PageLoadTimeout == 0 {
		opts.PageLoadTimeout = 30 * time.Second
	}
	if opts.TweetFeedTimeout == 0 {
		opts.TweetFeedTimeout = 10 * time.Second
	}
	if opts.MaxScrolls == 0 {
		opts.MaxScrolls = 10
	}
	if opts.ConsecutiveSeenStop == 0 {
		opts.ConsecutiveSeenStop = 3
	}
	if opts.ScrollJitterMin == 0 {
		opts.ScrollJitterMin = 250 * time.Millisecond
	}
	if opts.ScrollJitterMax == 0 {
		opts.ScrollJitterMax = 500 * time.Millisecond
	}
	svc := &Service{
		browser:             b,
		cookieFile:          opts.CookieFile,
		reauthURL:           opts.ReauthURL,
		reauthCommand:       opts.ReauthCommand,
		warmPathTTL:         opts.WarmPathTTL,
		verifyTimeout:       opts.VerifyTimeout,
		pageLoadTimeout:     opts.PageLoadTimeout,
		tweetFeedTimeout:    opts.TweetFeedTimeout,
		maxScrolls:          opts.MaxScrolls,
		consecutiveSeenStop: opts.ConsecutiveSeenStop,
		scrollJitterMin:     opts.ScrollJitterMin,
		scrollJitterMax:     opts.ScrollJitterMax,
		auditEmit:           opts.AuditEmit,
		build:               opts.Build,
		state:               StateStarting,
		startedAt:           time.Now().UTC(),
	}
	if done := b.Done(); done != nil {
		go func() {
			<-done
			svc.MarkBrowserExited()
		}()
	}
	return svc
}

// SetState transitions the service to a new state with an
// optional human-readable reason (surfaced in /status responses).
// Concurrent-safe.
//
// Emits one audit event when crossing into an externally actionable terminal
// state: `twitter.auth_expired` for StateUnauthenticated and
// `twitter.browser_failed` for StateFailed. Repeated writes of the same state
// do not re-emit. StateFailed is terminal for this process; only a fresh
// container may return it to service.
func (s *Service) SetState(newState State, reason string) {
	s.mu.Lock()
	prevState := s.state
	if prevState == StateFailed && newState != StateFailed {
		s.mu.Unlock()
		return
	}
	s.state = newState
	s.stateReason = reason
	if newState == StateHealthy || newState == StateDegraded || newState == StateUnauthenticated {
		s.lastAuthCheck = time.Now().UTC()
	}
	lastLoadedMtime := s.lastLoadedMtime
	s.mu.Unlock()

	if s.auditEmit == nil {
		return
	}
	action := ""
	switch {
	case newState == StateUnauthenticated && prevState != StateUnauthenticated:
		action = "twitter.auth_expired"
	case newState == StateFailed && prevState != StateFailed:
		action = "twitter.browser_failed"
	}
	if action != "" {
		s.auditEmit(action, map[string]any{
			"reason":            reason,
			"previous_state":    string(prevState),
			"last_loaded_mtime": lastLoadedMtime.Format(time.RFC3339),
		})
	}
}

// MarkBrowserExited records loss of the service's critical Firefox child.
// cmd/twitter exits non-zero after this transition so Docker can rebuild the
// entire Playwright/browser unit from the shared cookie backup.
func (s *Service) MarkBrowserExited() {
	s.SetState(StateFailed, "browser process exited")
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
//	GET  /status        Always 200; JSON snapshot for the dashboard.
//	POST /search        (T/c) fires a search against the current session.
//	GET  /authenticate  Reports auth state; on unauthenticated returns
//	                    503 + reauth instructions (VNC container URL,
//	                    docker command to start it). Operator-facing.
//	POST /auth/verify   Forces a full VerifySession bypassing the warm
//	                    path. Used after raw-Firefox capture and by
//	                    scheduled maintenance to force reload, verification,
//	                    and cookie persistence.
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
		"state":              string(s.state),
		"reason":             s.stateReason,
		"started_at":         s.startedAt,
		"last_auth_check":    s.lastAuthCheck,
		"last_loaded_mtime":  s.lastLoadedMtime,
		"cookie_fingerprint": s.lastFingerprint.Hex(),
		"cookie_backup": map[string]any{
			"last_attempt": s.lastCookieBackupAttempt,
			"last_success": s.lastCookieBackupSuccess,
			"last_error":   s.lastCookieBackupError,
		},
		"cookie_reload": map[string]any{
			"last_attempt": s.lastCookieReloadAttempt,
			"last_success": s.lastCookieReloadSuccess,
			"last_error":   s.lastCookieReloadError,
		},
		"busy":  s.busyCount > 0,
		"build": s.build,
	})
}
