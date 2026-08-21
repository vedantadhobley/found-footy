// Package twitterauth captures raw-Firefox cookies for the Playwright search
// fleet without automating the login browser.
package twitterauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vedantadhobley/found-footy/internal/twitter"
)

var (
	// ErrProfileNotReady means Firefox has not created its cookie database yet.
	ErrProfileNotReady = errors.New("twitter auth: Firefox profile not ready")
	// ErrNotAuthenticated means the profile lacks a non-expired auth token.
	ErrNotAuthenticated = errors.New("twitter auth: profile is not authenticated")
	// ErrProfileBusy means Firefox still owns its exclusive cookie-database lock.
	ErrProfileBusy = errors.New("twitter auth: close Firefox to capture cookies")
)

const sqliteExecutable = "/usr/bin/sqlite3"

const firefoxCookieQuery = `
SELECT
  name,
  value,
  host AS domain,
  path,
  expiry AS expires,
  isHttpOnly AS http_only,
  isSecure AS secure,
  sameSite AS same_site
FROM moz_cookies
WHERE
  lower(ltrim(host, '.')) IN ('x.com', 'twitter.com')
  OR lower(ltrim(host, '.')) LIKE '%.x.com'
  OR lower(ltrim(host, '.')) LIKE '%.twitter.com'
ORDER BY name, host, path;
`

// CookieSource reads a complete browser-neutral snapshot from the raw Firefox
// profile. Tests replace SQLiteSource with a deterministic fake.
type CookieSource interface {
	Read(context.Context) ([]twitter.Cookie, time.Time, error)
}

// SQLiteSource reads Firefox's cookies.sqlite through the distribution's
// sqlite3 CLI after Firefox releases its profile lock. This process never
// copies, locks, or mutates the database.
type SQLiteSource struct {
	ProfileDir string
	Now        func() time.Time
}

type sqliteCookie struct {
	Name       string  `json:"name"`
	Value      string  `json:"value"`
	Domain     string  `json:"domain"`
	Path       string  `json:"path"`
	Expires    float64 `json:"expires"`
	HTTPOnly   int     `json:"http_only"`
	Secure     int     `json:"secure"`
	SameSiteID int     `json:"same_site"`
}

// Read queries the current Firefox cookie database and returns only unexpired
// X/Twitter cookies. A non-expired auth_token is the publication gate.
func (s SQLiteSource) Read(ctx context.Context) ([]twitter.Cookie, time.Time, error) {
	database := filepath.Join(s.ProfileDir, "cookies.sqlite")
	if _, err := os.Stat(database); err != nil {
		if os.IsNotExist(err) {
			return nil, time.Time{}, ErrProfileNotReady
		}
		return nil, time.Time{}, fmt.Errorf("twitter auth: stat cookie database: %w", err)
	}

	cmd := exec.CommandContext(
		ctx,
		sqliteExecutable,
		"-batch",
		"-readonly",
		"-json",
		"-cmd", ".timeout 1000",
		database,
		firefoxCookieQuery,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if strings.Contains(strings.ToLower(message), "database is locked") {
			return nil, time.Time{}, ErrProfileBusy
		}
		if message == "" {
			message = err.Error()
		}
		return nil, time.Time{}, fmt.Errorf("twitter auth: read cookie database: %s", message)
	}

	return decodeFirefoxCookies(stdout.Bytes(), s.now())
}

func (s SQLiteSource) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func decodeFirefoxCookies(data []byte, now time.Time) ([]twitter.Cookie, time.Time, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, time.Time{}, ErrNotAuthenticated
	}

	var rows []sqliteCookie
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, time.Time{}, fmt.Errorf("twitter auth: decode sqlite output: %w", err)
	}

	cookies := make([]twitter.Cookie, 0, len(rows))
	var authExpiresAt time.Time
	for _, row := range rows {
		if row.Name == "" || row.Value == "" || row.Domain == "" {
			continue
		}
		if row.Expires > 0 && row.Expires <= float64(now.Unix()) {
			continue
		}
		cookie := twitter.Cookie{
			Name:     row.Name,
			Value:    row.Value,
			Domain:   row.Domain,
			Path:     row.Path,
			Expires:  row.Expires,
			HTTPOnly: row.HTTPOnly != 0,
			Secure:   row.Secure != 0,
			SameSite: firefoxSameSite(row.SameSiteID),
		}
		cookies = append(cookies, cookie)
		if row.Name == "auth_token" && row.Expires > float64(now.Unix()) {
			authExpiresAt = time.Unix(int64(row.Expires), 0).UTC()
		}
	}
	if authExpiresAt.IsZero() {
		return nil, time.Time{}, ErrNotAuthenticated
	}
	return cookies, authExpiresAt, nil
}

func firefoxSameSite(value int) string {
	switch value {
	case 1:
		return "Lax"
	case 2:
		return "Strict"
	default:
		return "None"
	}
}

// State is the raw-login capture state exposed through /health and /status.
type State string

const (
	StateStarting State = "starting"
	StateWaiting  State = "waiting_for_login"
	StateReady    State = "ready"
	StateDegraded State = "degraded"
)

// BuildInfo identifies the auth binary and image running the VNC service.
type BuildInfo struct {
	GitSHA   string `json:"git_sha"`
	BuiltAt  string `json:"built_at"`
	ImageTag string `json:"image_tag"`
}

// Status is the secret-free capture evidence returned by /status.
type Status struct {
	State         State     `json:"state"`
	Reason        string    `json:"reason"`
	LastAttempt   time.Time `json:"last_attempt"`
	LastCapture   time.Time `json:"last_capture"`
	AuthExpiresAt time.Time `json:"auth_expires_at"`
	CookieCount   int       `json:"cookie_count"`
	Fingerprint   string    `json:"cookie_fingerprint"`
	LastError     string    `json:"last_error"`
	StartedAt     time.Time `json:"started_at"`
	Build         BuildInfo `json:"build"`
}

// Capturer polls a raw Firefox profile and publishes changed valid snapshots.
type Capturer struct {
	source       CookieSource
	cookieFile   string
	pollInterval time.Duration
	now          func() time.Time

	mu              sync.RWMutex
	status          Status
	lastFingerprint twitter.CookieFingerprint
}

// NewCapturer constructs a capture loop with no browser-automation dependency.
func NewCapturer(source CookieSource, cookieFile string, pollInterval time.Duration, build BuildInfo) *Capturer {
	return &Capturer{
		source:       source,
		cookieFile:   cookieFile,
		pollInterval: pollInterval,
		now:          func() time.Time { return time.Now().UTC() },
		status: Status{
			State:     StateStarting,
			Reason:    "waiting for first profile read",
			StartedAt: time.Now().UTC(),
			Build:     build,
		},
	}
}

// CaptureOnce reads, validates, and conditionally publishes one snapshot.
func (c *Capturer) CaptureOnce(ctx context.Context) error {
	now := c.now()
	c.mu.Lock()
	c.status.LastAttempt = now
	c.mu.Unlock()

	cookies, authExpiresAt, err := c.source.Read(ctx)
	if err != nil {
		c.recordFailure(err)
		return err
	}
	fingerprint := twitter.Fingerprint(cookies)

	c.mu.RLock()
	unchanged := fingerprint == c.lastFingerprint
	c.mu.RUnlock()
	if !unchanged {
		if err := twitter.WriteBackup(c.cookieFile, cookies, now); err != nil {
			c.recordFailure(err)
			return err
		}
	}

	c.mu.Lock()
	c.lastFingerprint = fingerprint
	c.status.State = StateReady
	c.status.Reason = "authenticated cookie snapshot captured"
	c.status.LastCapture = now
	c.status.AuthExpiresAt = authExpiresAt
	c.status.CookieCount = len(cookies)
	c.status.Fingerprint = fingerprint.Hex()
	c.status.LastError = ""
	c.mu.Unlock()
	return nil
}

func (c *Capturer) recordFailure(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if errors.Is(err, ErrProfileNotReady) || errors.Is(err, ErrProfileBusy) || errors.Is(err, ErrNotAuthenticated) {
		c.status.State = StateWaiting
		c.status.Reason = err.Error()
	} else {
		c.status.State = StateDegraded
		c.status.Reason = "cookie capture failed"
	}
	c.status.LastError = err.Error()
}

// Run captures immediately, then repeats until the context is canceled.
func (c *Capturer) Run(ctx context.Context) {
	_ = c.CaptureOnce(ctx)
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.CaptureOnce(ctx)
		}
	}
}

// Status returns a concurrency-safe snapshot without cookie values.
func (c *Capturer) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}
