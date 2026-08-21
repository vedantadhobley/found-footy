// http_test.go verifies the raw-login service's read-only HTTP contract.
package twitterauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/twitter"
)

func TestHealthRequiresCapturedLiveAuth(t *testing.T) {
	expires := time.Now().UTC().Add(time.Hour)
	capturer := NewCapturer(fakeSource{
		cookies: []twitter.Cookie{{Name: "auth_token", Value: "secret", Domain: ".x.com", Path: "/", Expires: float64(expires.Unix())}},
		expires: expires,
	}, filepath.Join(t.TempDir(), "cookies.json"), time.Second, BuildInfo{})

	recorder := httptest.NewRecorder()
	capturer.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("pre-capture health = %d, want 503", recorder.Code)
	}
	if err := capturer.CaptureOnce(context.Background()); err != nil {
		t.Fatalf("CaptureOnce: %v", err)
	}
	recorder = httptest.NewRecorder()
	capturer.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("post-capture health = %d, want 200", recorder.Code)
	}
}
