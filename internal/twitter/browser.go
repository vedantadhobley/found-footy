// Playwright-Go + Firefox wrapper. Owns the browser lifecycle:
// launch persistent context, apply stealth patches, load cookies
// from disk, verify session via a probe navigation. Higher-level
// concerns (search, scroll, extraction) live in T/c's search.go.
//
// Concurrency model: one Browser per service process; multiple
// concurrent calls to Navigate share the underlying Playwright
// context but each opens its own Page (Playwright's Page is
// per-tab, cheap to create/close). Callers should serialize search
// requests at the service layer if we want single-threaded scroll
// behavior — the browser itself doesn't enforce that.
package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// Browser wraps Playwright's persistent context. The zero value is
// not usable — call NewBrowser.
type Browser struct {
	pw        *playwright.Playwright
	browser   playwright.Browser
	ctx       playwright.BrowserContext
	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
	closeErr  error

	// Config
	profileDir string // persistent Firefox profile path
	headless   bool
}

// NewBrowserOptions configures browser launch. All fields have
// sensible defaults; the zero value works for the T/a PoC.
type NewBrowserOptions struct {
	// ProfileDir is the persistent Firefox profile path. Default:
	// /data/firefox-profile. Lives in the container's writable layer
	// (headless) — each container has its own private profile, no
	// shared volume, no cross-instance SQLite locking. VNC container
	// mounts its own dedicated volume here for operator session
	// persistence. See decisions.md 2026-07-23 for the ephemeral-vs-
	// shared rationale.
	ProfileDir string

	// Headless: true for headless scraping (default), false for VNC
	// container's manual-login mode where a human interacts.
	Headless bool

	// FirefoxUserPrefs is a map of Firefox about:config preferences
	// applied at persistent-context launch. Passed verbatim to
	// Playwright's LaunchPersistentContextOptions.FirefoxUserPrefs.
	//
	// Values must be JSON-serializable and match the pref's Firefox-
	// side type — passing the wrong type is a silent no-op with no
	// error surface, so caller is responsible for correctness. See
	// cmd/twitter/main.go idleCPUFirefoxPrefs for the production
	// prefs (CPU-savings + cold-start-speedup set).
	FirefoxUserPrefs map[string]any
}

// NewBrowser launches Playwright + Firefox in a persistent context
// with the stealth init script applied to every page. Returns a
// Browser handle usable for navigation. Caller is responsible for
// Close.
func NewBrowser(opts NewBrowserOptions) (*Browser, error) {
	if opts.ProfileDir == "" {
		opts.ProfileDir = "/data/firefox-profile"
	}
	if err := os.MkdirAll(opts.ProfileDir, 0o755); err != nil {
		return nil, fmt.Errorf("twitter.NewBrowser: mkdir profile: %w", err)
	}

	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("twitter.NewBrowser: playwright.Run: %w", err)
	}

	launchOpts := playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(opts.Headless),
	}
	if len(opts.FirefoxUserPrefs) > 0 {
		launchOpts.FirefoxUserPrefs = opts.FirefoxUserPrefs
	}
	pctx, err := pw.Firefox.LaunchPersistentContext(opts.ProfileDir, launchOpts)
	if err != nil {
		_ = pw.Stop()
		return nil, fmt.Errorf("twitter.NewBrowser: launch persistent context: %w", err)
	}

	// Apply stealth patches so every subsequent page load has them.
	if err := pctx.AddInitScript(playwright.Script{Content: playwright.String(StealthInitScript)}); err != nil {
		_ = pctx.Close()
		_ = pw.Stop()
		return nil, fmt.Errorf("twitter.NewBrowser: add stealth init script: %w", err)
	}

	b := &Browser{
		pw:         pw,
		browser:    pctx.Browser(),
		ctx:        pctx,
		done:       make(chan struct{}),
		profileDir: opts.ProfileDir,
		headless:   opts.Headless,
	}
	// The persistent context closes when Firefox exits. Browser disconnect is
	// registered as a second signal so driver/browser loss cannot leave Go PID 1
	// serving stale health. Both callbacks converge through doneOnce.
	pctx.OnClose(func(playwright.BrowserContext) { b.markDone() })
	if b.browser != nil {
		b.browser.OnDisconnected(func(playwright.Browser) { b.markDone() })
		if !b.browser.IsConnected() {
			b.markDone()
		}
	}
	if pctx.IsClosed() {
		b.markDone()
	}
	return b, nil
}

// Done closes once when the persistent context or its Firefox browser exits.
// The service treats this browser as a critical child process and lets the
// container restart the complete Playwright unit.
func (b *Browser) Done() <-chan struct{} { return b.done }

func (b *Browser) markDone() {
	b.doneOnce.Do(func() {
		if b.done != nil {
			close(b.done)
		}
	})
}

// Close terminates the browser + Playwright driver. Idempotent —
// safe to call after a construction failure that returned a partial
// Browser (won't happen in current NewBrowser, but future variants
// may).
func (b *Browser) Close() error {
	b.closeOnce.Do(func() {
		if b.ctx != nil {
			if err := b.ctx.Close(); err != nil && b.closeErr == nil {
				b.closeErr = err
			}
		}
		if b.pw != nil {
			if err := b.pw.Stop(); err != nil && b.closeErr == nil {
				b.closeErr = err
			}
		}
		b.markDone()
	})
	return b.closeErr
}

// Navigate opens a fresh page, goes to url, waits for network idle,
// and returns the resulting page. Caller closes the page when done.
// Bounded by the given timeout via a context — long-running loads
// on a wedged browser surface as an error rather than hanging.
func (b *Browser) Navigate(_ context.Context, url string, timeout time.Duration) (playwright.Page, error) {
	page, err := b.ctx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("twitter.Browser.Navigate: new page: %w", err)
	}
	_, err = page.Goto(url, playwright.PageGotoOptions{
		Timeout:   playwright.Float(float64(timeout / time.Millisecond)),
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	})
	if err != nil {
		_ = page.Close()
		return nil, fmt.Errorf("twitter.Browser.Navigate: goto %s: %w", url, err)
	}
	return page, nil
}

// Cookie is the on-disk shape written by both Python (session.py's
// _backup_cookies_to_host) and our Go version. See CookieBackup
// wrapper for the {exported_at, cookies} envelope.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	SameSite string  `json:"sameSite,omitempty"`
}

// CookieBackup mirrors Python's on-disk shape:
//
//	{"exported_at": "...", "cookies": [...]}
//
// Unknown top-level keys are ignored on read; extra fields we choose
// to add later (captured_by_instance, auth_token_expires_at, etc.)
// don't break Python's parser either. See video-dedup Q4 sign-off.
type CookieBackup struct {
	ExportedAt string   `json:"exported_at"`
	Cookies    []Cookie `json:"cookies"`
}

// LoadCookies reads a JSON backup file and adds each cookie to the
// browser context. Errors on missing/malformed file. Returns the
// count of cookies added.
func (b *Browser) LoadCookies(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("twitter.Browser.LoadCookies: read %s: %w", path, err)
	}
	var backup CookieBackup
	if err := json.Unmarshal(data, &backup); err != nil {
		return 0, fmt.Errorf("twitter.Browser.LoadCookies: parse: %w", err)
	}
	if len(backup.Cookies) == 0 {
		return 0, nil
	}

	pwCookies := make([]playwright.OptionalCookie, 0, len(backup.Cookies))
	for _, c := range backup.Cookies {
		pw := playwright.OptionalCookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   playwright.String(c.Domain),
			Path:     playwright.String(c.Path),
			HttpOnly: playwright.Bool(c.HTTPOnly),
			Secure:   playwright.Bool(c.Secure),
		}
		if c.Expires > 0 {
			pw.Expires = playwright.Float(c.Expires)
		}
		// Preserve SameSite through the round-trip so we don't re-create
		// the nil that cookieFromPW has to guard. nil = leave unset.
		pw.SameSite = pwSameSite(c.SameSite)
		pwCookies = append(pwCookies, pw)
	}
	if err := b.ctx.AddCookies(pwCookies); err != nil {
		return 0, fmt.Errorf("twitter.Browser.LoadCookies: add cookies: %w", err)
	}
	return len(pwCookies), nil
}

// GetCookies returns the current cookies from the browser context,
// converted to our on-disk Cookie shape. Used by the auth backup
// flow to persist fresh cookies after every successful search
// (Twitter rotates csrf token + occasionally guest_id on each
// authenticated request; we preserve those refreshes so container
// restarts pick up the latest session state).
func (b *Browser) GetCookies() ([]Cookie, error) {
	pw, err := b.ctx.Cookies()
	if err != nil {
		return nil, fmt.Errorf("twitter.Browser.GetCookies: %w", err)
	}
	out := make([]Cookie, 0, len(pw))
	for _, c := range pw {
		out = append(out, cookieFromPW(c))
	}
	return out, nil
}

// cookieFromPW converts a Playwright cookie to our on-disk shape.
// SameSite is a *SameSiteAttribute in playwright-go and is nil whenever
// the cookie carries no SameSite attribute — common for cookies added
// via AddCookies without one, which is exactly what LoadCookies /
// ReplaceCookies used to do. Dereferencing it unconditionally
// (`string(*c.SameSite)`) panicked on the backup after every cookie
// load; the nil-guard here is mandatory.
func cookieFromPW(c playwright.Cookie) Cookie {
	ss := ""
	if c.SameSite != nil {
		ss = string(*c.SameSite)
	}
	return Cookie{
		Name:     c.Name,
		Value:    c.Value,
		Domain:   c.Domain,
		Path:     c.Path,
		Expires:  c.Expires,
		HTTPOnly: c.HttpOnly,
		Secure:   c.Secure,
		SameSite: ss,
	}
}

// pwSameSite maps our stored SameSite string back to Playwright's
// *SameSiteAttribute so LoadCookies / ReplaceCookies preserve it on the
// way IN — otherwise the read→store→load round-trip silently drops
// SameSite and manufactures the nil that cookieFromPW then has to guard.
// Empty / unrecognized → nil (leave unset; Playwright applies its default).
func pwSameSite(s string) *playwright.SameSiteAttribute {
	// playwright-go exposes these constants as *SameSiteAttribute already.
	switch s {
	case string(*playwright.SameSiteAttributeStrict):
		return playwright.SameSiteAttributeStrict
	case string(*playwright.SameSiteAttributeLax):
		return playwright.SameSiteAttributeLax
	case string(*playwright.SameSiteAttributeNone):
		return playwright.SameSiteAttributeNone
	}
	return nil
}

// ReplaceCookies clears the current context's cookies and adds the
// provided set. Used by the auth reload flow when another instance
// or the VNC container has written fresh cookies to the shared
// backup file (detected via mtime); we discard our in-memory
// cookies and adopt the fresh ones before the next verify.
func (b *Browser) ReplaceCookies(cookies []Cookie) error {
	if err := b.ctx.ClearCookies(); err != nil {
		return fmt.Errorf("twitter.Browser.ReplaceCookies: clear: %w", err)
	}
	if len(cookies) == 0 {
		return nil
	}
	pwCookies := make([]playwright.OptionalCookie, 0, len(cookies))
	for _, c := range cookies {
		pw := playwright.OptionalCookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   playwright.String(c.Domain),
			Path:     playwright.String(c.Path),
			HttpOnly: playwright.Bool(c.HTTPOnly),
			Secure:   playwright.Bool(c.Secure),
		}
		if c.Expires > 0 {
			pw.Expires = playwright.Float(c.Expires)
		}
		// Preserve SameSite through the round-trip so we don't re-create
		// the nil that cookieFromPW has to guard. nil = leave unset.
		pw.SameSite = pwSameSite(c.SameSite)
		pwCookies = append(pwCookies, pw)
	}
	if err := b.ctx.AddCookies(pwCookies); err != nil {
		return fmt.Errorf("twitter.Browser.ReplaceCookies: add: %w", err)
	}
	return nil
}

// VerifySession navigates to x.com/home and returns nil if a
// logged-in indicator is present, or an error describing the miss.
// Used at startup to determine whether the loaded cookies are still
// valid; feeds the service state machine's transition to
// authenticated vs unauthenticated.
//
// Logged-in indicator: presence of the primary column tweet
// composer (`[data-testid='SideNav_AccountSwitcher_Button']` is the
// most reliable). Absence signals cookie expiry → VNC re-auth needed.
func (b *Browser) VerifySession(ctx context.Context, timeout time.Duration) error {
	page, err := b.Navigate(ctx, "https://x.com/home", timeout)
	if err != nil {
		return err
	}
	defer func() { _ = page.Close() }()

	// Give the SPA a moment to render logged-in-vs-logged-out state.
	if err := page.Locator(`[data-testid='SideNav_AccountSwitcher_Button']`).WaitFor(
		playwright.LocatorWaitForOptions{Timeout: playwright.Float(float64(timeout / time.Millisecond))},
	); err != nil {
		return fmt.Errorf("twitter.Browser.VerifySession: logged-in indicator missing (cookies likely expired): %w", err)
	}
	return nil
}
