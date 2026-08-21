// Twitter search-result decoding, URL normalization, and HTTP response helpers.
package twitter

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type extractedTweet struct {
	TweetURL        string  `json:"tweet_url"`
	Text            string  `json:"text"`
	Datetime        string  `json:"datetime"`
	IsPromoted      bool    `json:"is_promoted"`
	HasVideo        bool    `json:"has_video"`
	DurationSeconds float64 `json:"duration_seconds"`
	AgeMinutes      float64 `json:"-"` // computed in Go, not JSON
}

// decodeExtractResult round-trips the Playwright evaluate result
// through JSON to get typed extractedTweet slices. Computes
// AgeMinutes from Datetime after decode — JS timezone math is a
// footgun.
func decodeExtractResult(raw any) ([]extractedTweet, error) {
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var tweets []extractedTweet
	if err := json.Unmarshal(buf, &tweets); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range tweets {
		if tweets[i].Datetime == "" {
			continue
		}
		t, parseErr := time.Parse(time.RFC3339, tweets[i].Datetime)
		if parseErr != nil {
			continue
		}
		tweets[i].AgeMinutes = now.Sub(t).Minutes()
	}
	return tweets, nil
}

// normalizeExcludeIDs converts the exclude_urls slice into a set of
// tweet IDs. Callers may pass URLs in either /user/status/ID or
// /i/status/ID shape — we extract the ID from both.
func normalizeExcludeIDs(urls []string) map[string]struct{} {
	out := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		tid := extractTweetIDFromURL(u)
		if tid != "" && tid != "unknown" {
			out[tid] = struct{}{}
		}
	}
	return out
}

// extractTweetIDFromURL pulls the numeric tweet ID from a Twitter URL.
// Ports scrape.py's extract_tweet_id_from_url. Returns "unknown" for
// unrecognized URLs — matches Python's sentinel so downstream branches
// don't need to change shape.
func extractTweetIDFromURL(u string) string {
	if u == "" || !strings.Contains(u, "/status/") {
		return "unknown"
	}
	after := u[strings.Index(u, "/status/")+len("/status/"):]
	// Strip query string (?ref_src=...).
	if q := strings.Index(after, "?"); q >= 0 {
		after = after[:q]
	}
	// Strip fragment (# in path).
	if h := strings.Index(after, "#"); h >= 0 {
		after = after[:h]
	}
	// Strip trailing slash / anything past the ID.
	if slash := strings.Index(after, "/"); slash >= 0 {
		after = after[:slash]
	}
	if after == "" {
		return "unknown"
	}
	return after
}

// extractUsernameFromURL pulls the @username out of a tweet URL. Ports
// scrape.py's extract_username_from_url. Returns "Unknown" for
// /i/status/... URLs (composer view, no username in path).
func extractUsernameFromURL(u string) string {
	if u == "" {
		return "Unknown"
	}
	trimmed := u
	trimmed = strings.TrimPrefix(trimmed, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	parts := strings.Split(trimmed, "/")
	if len(parts) >= 3 && parts[1] != "i" {
		return parts[1]
	}
	return "Unknown"
}

// MinSnowflakeLen is the guard from scrape.py — Twitter snowflakes
// have been ≥18 digits since ~early 2020. Shorter numeric IDs are
// upstream X-side rendering quirks for deleted/quoted/edge-case
// tweets that won't syndicate to a downloadable video. Same threshold
// as archive/src/activities/download.py.
const MinSnowflakeLen = 18

// isTruncatedSnowflake reports whether an ID looks like a truncated
// snowflake and should be rejected. Same shape as scrape.py's
// is_truncated_snowflake (the "unknown" sentinel + non-digit IDs
// return false — callers handle those elsewhere).
func isTruncatedSnowflake(tid string) bool {
	if tid == "" || tid == "unknown" {
		return false
	}
	for _, r := range tid {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(tid) < MinSnowflakeLen
}

// truncate returns s truncated to max Unicode code points. Used for tweet text
// where downstream storage expects at most 200 characters. Byte slicing could
// split a multibyte rune and produce text PostgreSQL rejects as invalid UTF-8.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// writeSearchOK emits a 200 JSON body.
func writeSearchOK(w http.ResponseWriter, body SearchResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// writeSearchError emits a structured error body with the given HTTP
// status. error_class is a stable enum callers can branch on.
func writeSearchError(w http.ResponseWriter, status int, class, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(SearchErrorBody{
		Status:     "error",
		ErrorClass: class,
		Message:    message,
	})
}
