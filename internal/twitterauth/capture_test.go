// capture_test.go verifies raw Firefox decoding and atomic publication gates.
package twitterauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/twitter"
)

type fakeSource struct {
	cookies []twitter.Cookie
	expires time.Time
	err     error
}

func (f fakeSource) Read(context.Context) ([]twitter.Cookie, time.Time, error) {
	return f.cookies, f.expires, f.err
}

func TestDecodeFirefoxCookiesRequiresLiveAuthToken(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	data := []byte(`[
  {"name":"auth_token","value":"secret","domain":".x.com","path":"/","expires":1900000000,"http_only":1,"secure":1,"same_site":1},
  {"name":"expired","value":"old","domain":".x.com","path":"/","expires":1700000000,"http_only":0,"secure":0,"same_site":0}
]`)
	cookies, expires, err := decodeFirefoxCookies(data, now)
	if err != nil {
		t.Fatalf("decodeFirefoxCookies: %v", err)
	}
	if len(cookies) != 1 || cookies[0].Name != "auth_token" {
		t.Fatalf("cookies = %+v, want only live auth_token", cookies)
	}
	if !expires.Equal(time.Unix(1_900_000_000, 0).UTC()) {
		t.Fatalf("expiry = %s", expires)
	}
	if cookies[0].SameSite != "Lax" || !cookies[0].HTTPOnly || !cookies[0].Secure {
		t.Fatalf("cookie flags not preserved: %+v", cookies[0])
	}
}

func TestDecodeFirefoxCookiesRejectsMissingAuth(t *testing.T) {
	_, _, err := decodeFirefoxCookies([]byte(`[{"name":"ct0","value":"csrf","domain":".x.com","path":"/","expires":1900000000}]`), time.Unix(1_800_000_000, 0))
	if !errors.Is(err, ErrNotAuthenticated) {
		t.Fatalf("error = %v, want ErrNotAuthenticated", err)
	}
}

func TestDecodeFirefoxCookiesTreatsEmptySQLiteOutputAsUnauthenticated(t *testing.T) {
	_, _, err := decodeFirefoxCookies(nil, time.Unix(1_800_000_000, 0))
	if !errors.Is(err, ErrNotAuthenticated) {
		t.Fatalf("error = %v, want ErrNotAuthenticated", err)
	}
}

func TestCaptureOncePublishesCompatibleBackup(t *testing.T) {
	expires := time.Now().UTC().Add(24 * time.Hour)
	path := filepath.Join(t.TempDir(), "twitter_cookies.json")
	capturer := NewCapturer(fakeSource{
		cookies: []twitter.Cookie{
			{Name: "auth_token", Value: "secret", Domain: ".x.com", Path: "/", Expires: float64(expires.Unix()), HTTPOnly: true, Secure: true},
			{Name: "ct0", Value: "csrf", Domain: ".x.com", Path: "/", Expires: float64(expires.Unix()), Secure: true},
		},
		expires: expires,
	}, path, time.Second, BuildInfo{GitSHA: "abc"})
	if err := capturer.CaptureOnce(context.Background()); err != nil {
		t.Fatalf("CaptureOnce: %v", err)
	}
	cookies, _, err := twitter.ReadBackup(path)
	if err != nil {
		t.Fatalf("ReadBackup: %v", err)
	}
	if len(cookies) != 2 {
		t.Fatalf("persisted cookies = %d, want 2", len(cookies))
	}
	status := capturer.Status()
	if status.State != StateReady || status.CookieCount != 2 || status.Fingerprint == "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestCaptureOnceDoesNotOverwriteOnUnauthenticatedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twitter_cookies.json")
	original := []byte("preserve me")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	capturer := NewCapturer(fakeSource{err: ErrNotAuthenticated}, path, time.Second, BuildInfo{})
	if err := capturer.CaptureOnce(context.Background()); !errors.Is(err, ErrNotAuthenticated) {
		t.Fatalf("CaptureOnce error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preserved file: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("backup changed to %q", got)
	}
	if capturer.Status().State != StateWaiting {
		t.Fatalf("state = %s, want waiting", capturer.Status().State)
	}
}

func TestCaptureOnceTreatsOpenFirefoxLockAsWaiting(t *testing.T) {
	capturer := NewCapturer(fakeSource{err: ErrProfileBusy}, filepath.Join(t.TempDir(), "cookies.json"), time.Second, BuildInfo{})
	if err := capturer.CaptureOnce(context.Background()); !errors.Is(err, ErrProfileBusy) {
		t.Fatalf("CaptureOnce error = %v, want ErrProfileBusy", err)
	}
	status := capturer.Status()
	if status.State != StateWaiting || status.Reason != ErrProfileBusy.Error() {
		t.Fatalf("status = %+v", status)
	}
}
