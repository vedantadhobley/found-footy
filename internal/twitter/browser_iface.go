// browser_iface.go — sessionBrowser is the subset of *Browser that
// *Service depends on. Extracted so the auth flow (EnsureAuthenticated,
// BackupCookies, maybeReloadCookies) can be exercised with a fake in
// unit tests without spinning up Playwright + Firefox.
//
// Production wire-up: NewBrowser returns *Browser, which satisfies
// this interface — assigning to Service.browser is a no-op cast.
package twitter

import (
	"context"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// sessionBrowser abstracts the browser operations Service performs.
// Kept minimal on purpose — every additional method here is another
// thing test fakes have to implement.
type sessionBrowser interface {
	VerifySession(ctx context.Context, timeout time.Duration) error
	ReplaceCookies(cookies []Cookie) error
	GetCookies() ([]Cookie, error)
	Navigate(ctx context.Context, url string, timeout time.Duration) (playwright.Page, error)
}
