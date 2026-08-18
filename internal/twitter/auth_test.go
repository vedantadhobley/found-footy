// Unit tests for auth.go — EnsureAuthenticated, BackupCookies,
// handleAuthenticate, handleAuthVerify. Uses a fake sessionBrowser
// so no Playwright / Firefox needed.
package twitter

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
