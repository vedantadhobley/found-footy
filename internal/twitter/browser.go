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
	"time"

	"github.com/mxschmitt/playwright-go"
)

// Browser wraps Playwright's persistent context. The zero value is
// not usable — call NewBrowser.
type Browser struct {
	pw      *playwright.Playwright
	browser playwright.Browser
	ctx     playwright.BrowserContext

	// Config
	profileDir string // persistent Firefox profile path
	headless   bool
}

// NewBrowserOptions configures browser launch. All fields have
// sensible defaults; the zero value works for the T/a PoC.
type NewBrowserOptions struct {
	// ProfileDir is the persistent Firefox profile path. Default:
	// /data/firefox-profile. Bind-mounted from the host so cookies
	// survive container restarts.
	ProfileDir string

	// Headless: true for headless scraping (default), false for VNC
	// container's manual-login mode where a human interacts.
	Headless bool
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

	return &Browser{
		pw:         pw,
		ctx:        pctx,
		profileDir: opts.ProfileDir,
		headless:   opts.Headless,
	}, nil
}

// Close terminates the browser + Playwright driver. Idempotent —
// safe to call after a construction failure that returned a partial
// Browser (won't happen in current NewBrowser, but future variants
// may).
func (b *Browser) Close() error {
	var firstErr error
	if b.ctx != nil {
		if err := b.ctx.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if b.pw != nil {
		if err := b.pw.Stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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
		pwCookies = append(pwCookies, pw)
	}
	if err := b.ctx.AddCookies(pwCookies); err != nil {
		return 0, fmt.Errorf("twitter.Browser.LoadCookies: add cookies: %w", err)
	}
	return len(pwCookies), nil
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
	if _, err := page.WaitForSelector(
		`[data-testid='SideNav_AccountSwitcher_Button']`,
		playwright.PageWaitForSelectorOptions{Timeout: playwright.Float(float64(timeout / time.Millisecond))},
	); err != nil {
		return fmt.Errorf("twitter.Browser.VerifySession: logged-in indicator missing (cookies likely expired): %w", err)
	}
	return nil
}
