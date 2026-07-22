// Tests for the pure-Go helpers of the search endpoint. The scroll
// loop + DOM extraction + HTTP handler are covered end-to-end via the
// dev stack smoke tests — Playwright.Page's interface surface is
// large enough that faking it here would be more code than value.
package twitter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExtractTweetIDFromURL — cases ported from scrape.py's
// extract_tweet_id_from_url. The "unknown" sentinel matches Python
// so downstream branches don't need to change shape.
func TestExtractTweetIDFromURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"basic user status", "https://x.com/username/status/1234567890123456789", "1234567890123456789"},
		{"i/status form", "https://x.com/i/status/1234567890123456789", "1234567890123456789"},
		{"twitter.com host", "https://twitter.com/user/status/9876543210987654321", "9876543210987654321"},
		{"trailing query params", "https://x.com/user/status/1234567890123456789?ref_src=foo&s=bar", "1234567890123456789"},
		{"trailing hash", "https://x.com/user/status/1234567890123456789#photo1", "1234567890123456789"},
		{"trailing slash", "https://x.com/user/status/1234567890123456789/photo/1", "1234567890123456789"},
		{"empty string", "", "unknown"},
		{"no /status/ segment", "https://x.com/username", "unknown"},
		{"malformed", "not-a-url", "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractTweetIDFromURL(tc.in); got != tc.want {
				t.Errorf("extractTweetIDFromURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestExtractUsernameFromURL — cases ported from scrape.py's
// extract_username_from_url. /i/status/ URLs return "Unknown" per
// Python's convention.
func TestExtractUsernameFromURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://x.com/BillOReilly/status/1234", "BillOReilly"},
		{"https://twitter.com/CristianoRonaldo/status/9999", "CristianoRonaldo"},
		{"https://x.com/i/status/1234", "Unknown"}, // composer-view URL — no username in path
		{"", "Unknown"},
		{"malformed", "Unknown"},
	}
	for _, tc := range tests {
		if got := extractUsernameFromURL(tc.in); got != tc.want {
			t.Errorf("extractUsernameFromURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestIsTruncatedSnowflake — Twitter snowflakes have been ≥18 digits
// since ~early 2020. Shorter IDs are upstream-X rendering quirks and
// won't syndicate to a downloadable video.
func TestIsTruncatedSnowflake(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"1234567890123456789", false}, // 19 digits, valid
		{"123456789012345678", false},  // exactly 18, valid
		{"12345678901234567", true},    // 17, truncated
		{"1234", true},                 // very short
		{"", false},                    // empty — handled by caller
		{"unknown", false},             // sentinel — handled by caller
		{"not-a-number", false},        // non-digit — handled by caller
	}
	for _, tc := range tests {
		if got := isTruncatedSnowflake(tc.in); got != tc.want {
			t.Errorf("isTruncatedSnowflake(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestNormalizeExcludeIDs — Discovery passes exclude_urls in both
// /user/status/ID and /i/status/ID shapes. Both must reduce to the
// same tweet ID set for the consecutive-seen counter to work.
func TestNormalizeExcludeIDs(t *testing.T) {
	urls := []string{
		"https://x.com/user1/status/111111111111111111",
		"https://x.com/i/status/222222222222222222",
		"https://twitter.com/user3/status/333333333333333333?ref_src=twsrc",
		"",             // empty — dropped
		"not-a-url",    // malformed — dropped (unknown sentinel)
	}
	got := normalizeExcludeIDs(urls)
	want := map[string]struct{}{
		"111111111111111111": {},
		"222222222222222222": {},
		"333333333333333333": {},
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d; got=%v", len(got), len(want), got)
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("missing expected ID %q; got=%v", id, got)
		}
	}
}

// TestDecodeExtractResult_AgeComputation — the JS evaluate returns
// raw ISO datetime; Go computes age. Verifies the round-trip through
// JSON and the age math.
func TestDecodeExtractResult_AgeComputation(t *testing.T) {
	// Simulate what Playwright.Page.Evaluate returns — []any of maps.
	raw := []any{
		map[string]any{
			"tweet_url":        "https://x.com/user/status/1234",
			"text":             "goal!",
			"datetime":         "2020-01-01T00:00:00Z", // very old
			"is_promoted":      false,
			"has_video":        true,
			"duration_seconds": 12.5,
		},
		map[string]any{
			"tweet_url":        "https://x.com/user/status/5678",
			"text":             "",
			"datetime":         "", // missing age
			"is_promoted":      true,
			"has_video":        false,
			"duration_seconds": 0.0,
		},
	}
	tweets, err := decodeExtractResult(raw)
	if err != nil {
		t.Fatalf("decodeExtractResult: %v", err)
	}
	if len(tweets) != 2 {
		t.Fatalf("len = %d, want 2", len(tweets))
	}
	if tweets[0].TweetURL != "https://x.com/user/status/1234" {
		t.Errorf("tweet_url mismatch: %q", tweets[0].TweetURL)
	}
	if tweets[0].AgeMinutes <= 0 {
		t.Errorf("2020-dated tweet should have positive age; got %v", tweets[0].AgeMinutes)
	}
	if tweets[0].DurationSeconds != 12.5 {
		t.Errorf("duration mismatch: got %v", tweets[0].DurationSeconds)
	}
	if !tweets[0].HasVideo {
		t.Errorf("has_video should be true")
	}
	if tweets[1].AgeMinutes != 0 {
		t.Errorf("empty-datetime tweet should have zero age; got %v", tweets[1].AgeMinutes)
	}
	if !tweets[1].IsPromoted {
		t.Errorf("is_promoted should be true")
	}
}

// TestHandleSearch_MethodNotAllowed — GET/PUT/DELETE should all fail
// with 405 + Allow: POST header. The T/a stub used GET; the T/c
// handler is POST-only.
func TestHandleSearch_MethodNotAllowed(t *testing.T) {
	fake := &fakeBrowser{}
	svc, _ := newTestService(t, fake)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/search", nil)
		svc.handleSearch(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: got status %d, want 405", method, rec.Code)
		}
		if allow := rec.Header().Get("Allow"); allow != "POST" {
			t.Errorf("method %s: Allow header = %q, want POST", method, allow)
		}
		var body SearchErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("method %s: decode body: %v", method, err)
		}
		if body.ErrorClass != errClassMethodNotAllowed {
			t.Errorf("method %s: error_class = %q", method, body.ErrorClass)
		}
	}
}

// TestHandleSearch_EmptyQuery — POST with empty or whitespace-only
// query returns 400 with error_class=empty_query.
func TestHandleSearch_EmptyQuery(t *testing.T) {
	fake := &fakeBrowser{}
	svc, _ := newTestService(t, fake)

	tests := []struct {
		name string
		body string
	}{
		{"empty string", `{"query":""}`},
		{"whitespace only", `{"query":"   "}`},
		{"missing field", `{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(tc.body))
			svc.handleSearch(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("got status %d, want 400", rec.Code)
			}
			var body SearchErrorBody
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if body.ErrorClass != errClassEmptyQuery {
				t.Errorf("error_class = %q, want %q", body.ErrorClass, errClassEmptyQuery)
			}
		})
	}
}

// TestHandleSearch_MalformedJSON — bad JSON body returns 400
// error_class=bad_request.
func TestHandleSearch_MalformedJSON(t *testing.T) {
	fake := &fakeBrowser{}
	svc, _ := newTestService(t, fake)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{not valid json`))
	svc.handleSearch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	var body SearchErrorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.ErrorClass != errClassBadRequest {
		t.Errorf("error_class = %q, want %q", body.ErrorClass, errClassBadRequest)
	}
}

// TestBuildSearchURL — the URL construction pattern. Twitter's Latest
// tab requires src=typed_query + f=live to sort by newness rather than
// relevance.
func TestBuildSearchURL(t *testing.T) {
	got := buildSearchURL(`(salah OR liverpool) filter:videos`)
	// URL-encoded space + colon + parens.
	if !strings.Contains(got, "src=typed_query") {
		t.Errorf("missing src=typed_query in %q", got)
	}
	if !strings.Contains(got, "f=live") {
		t.Errorf("missing f=live in %q", got)
	}
	if !strings.HasPrefix(got, "https://x.com/search?q=") {
		t.Errorf("wrong prefix in %q", got)
	}
}

// TestTruncate — bounded to 200 chars per scrape.py's default.
func TestTruncate(t *testing.T) {
	long := strings.Repeat("a", 300)
	if got := truncate(long, 200); len(got) != 200 {
		t.Errorf("len = %d, want 200", len(got))
	}
	if got := truncate("short", 200); got != "short" {
		t.Errorf("short string mangled: %q", got)
	}
}
