// search.go — the twitter service's /search endpoint. Consumes JSON
// requests from Discovery workflow (via internal/infra/twitter's HTTP
// client) and navigates a headless Playwright Firefox to Twitter's
// search results, scrolls through the Latest feed, extracts tweets
// carrying video content, and returns them as VideoRef entries.
//
// Design ref: docs/design/proposals/twitter-port.md § T/c. Behavioral
// invariants ported from archive/twitter/session.py _do_search +
// archive/twitter/scrape.py per docs/design/python-functional-spec.md.
//
// Search flow (top-to-bottom):
//
//  1. Validate request — POST-only, empty query rejected.
//  2. mtime check — reload cookies from shared backup file if newer
//     (reuses maybeReloadCookies from auth.go — no separate call to
//     EnsureAuthenticated on the hot path).
//  3. Build search URL — `q + " filter:videos"` appended by Discovery's
//     query builder is already in req.Query; service adds URL params
//     `src=typed_query&f=live` (Latest sort).
//  4. Navigate + combined verify — navigate directly to search URL,
//     check for SideNav_AccountSwitcher_Button as part of that page's
//     load. Present → session valid; absent + URL redirected to /login
//     → transition to StateUnauthenticated. Saves ~3-4s vs a separate
//     /home verify per the 2026-07-22 design note.
//  5. Wait for tweet feed — up to 10s. If absent, treat as legitimate
//     empty result (0 videos), not an error.
//  6. Scroll loop with four stop conditions (age / max_scrolls / empty /
//     consecutive_already_seen). DOM extraction via a single JS
//     evaluate per scroll for IPC efficiency.
//  7. BackupCookies on success — persists Twitter's rotated csrf
//     tokens to the shared file. Fingerprint dedupe skips no-op writes.
//  8. Return SearchResponse with videos + stop_reason + scroll count.
//
// Stealth: random 0.5–3s jitter between scroll actions (baseline #4
// per twitter-port.md T/c). User-Agent + Accept-Language rotation is
// deferred to T/b.5 hardening.
package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// VideoRef is one tweet carrying a video that passed the extraction
// filters. JSON tag alignment with internal/infra/twitter's client-
// side VideoRef is load-bearing — that struct decodes what this one
// encodes.
type VideoRef struct {
	TweetURL        string  `json:"tweet_url"`
	TweetText       string  `json:"tweet_text"`
	VideoPageURL    string  `json:"video_page_url"`
	DurationSeconds float64 `json:"duration_seconds"`

	// Fields NOT on the S7 client's VideoRef today but useful for
	// observability + LLM validation downstream. omitempty keeps the
	// wire format backward-compatible (client parses only the fields
	// it declares).
	Username   string  `json:"username,omitempty"`
	AgeMinutes float64 `json:"age_minutes,omitempty"`
}

// SearchRequest is the JSON body of a POST /search call. Mirrors
// internal/infra/twitter's client-side SearchRequest 1:1.
type SearchRequest struct {
	Query         string   `json:"query"`
	ExcludeURLs   []string `json:"exclude_urls,omitempty"`
	MaxAgeMinutes int      `json:"max_age_minutes,omitempty"`
}

// SearchResponse is the payload returned on a successful search. Extra
// telemetry fields (StopReason, Scrolls, Elapsed) are omitempty so the
// S7 client can ignore them without decode errors.
type SearchResponse struct {
	Status     string     `json:"status"` // "success"
	Videos     []VideoRef `json:"videos"`
	Count      int        `json:"count"`
	Query      string     `json:"query,omitempty"`
	StopReason string     `json:"stop_reason,omitempty"` // age / max_scrolls / empty / consecutive_seen
	Scrolls    int        `json:"scrolls,omitempty"`
	Elapsed    string     `json:"elapsed,omitempty"`
}

// SearchErrorBody is the structured error payload. error_class is a
// stable enum so callers can branch on it without regex-matching
// error messages. Same taxonomy shape as /authenticate.
type SearchErrorBody struct {
	Status     string `json:"status"` // "error"
	ErrorClass string `json:"error_class"`
	Message    string `json:"message"`
	ReauthURL  string `json:"reauth_url,omitempty"`
}

// Stop-reason constants. Kept as an unexported enum-shape rather than
// a typed enum because the values live inside SearchResponse.StopReason
// (JSON string) — enum ceremony would just make the code noisier.
const (
	stopAge             = "age"
	stopMaxScrolls      = "max_scrolls"
	stopEmpty           = "empty"
	stopConsecutiveSeen = "consecutive_seen"
)

// error_class enum values.
const (
	errClassBadRequest       = "bad_request"
	errClassEmptyQuery       = "empty_query"
	errClassMethodNotAllowed = "method_not_allowed"
	errClassAuthExpired      = "auth_expired"
	errClassNavigation       = "navigation_failed"
	errClassInternal         = "internal"
)

// handleSearch is the POST /search handler. See file-level docstring
// for the full flow.
func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeSearchError(w, http.StatusMethodNotAllowed, errClassMethodNotAllowed, "POST required")
		return
	}

	var req SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSearchError(w, http.StatusBadRequest, errClassBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeSearchError(w, http.StatusBadRequest, errClassEmptyQuery, "query is required")
		return
	}

	maxAgeMinutes := req.MaxAgeMinutes
	if maxAgeMinutes <= 0 {
		maxAgeMinutes = defaultMaxAgeMinutes
	}

	excludeIDs := normalizeExcludeIDs(req.ExcludeURLs)

	// Mark busy for scaler visibility. Held for the whole search (nav +
	// scroll + backup). /status reflects this via `busy: true`.
	s.setBusy(true)
	defer s.setBusy(false)

	// mtime check — reload cookies if the shared file has been updated
	// since we last loaded (VNC re-auth completed, another instance
	// wrote fresh cookies). No verify hop — we're about to navigate to
	// the search URL anyway and check the switcher button there.
	s.mu.RLock()
	lastLoadedMtime := s.lastLoadedMtime
	s.mu.RUnlock()
	if _, err := s.maybeReloadCookies(lastLoadedMtime); err != nil {
		// Reload failed (corrupt file, browser wedged). Continue anyway —
		// verify against current context will decide the real outcome.
	}

	start := time.Now()
	searchURL := buildSearchURL(req.Query)

	page, err := s.browser.Navigate(r.Context(), searchURL, s.pageLoadTimeout)
	if err != nil {
		writeSearchError(w, http.StatusBadGateway, errClassNavigation, "navigate: "+err.Error())
		return
	}
	defer func() { _ = page.Close() }()

	// Combined verify+search: check for the logged-in indicator on this
	// same page load. Timeout is short (5s) — the SPA shell renders
	// quickly if the session is valid; a longer wait means we're either
	// dealing with a slow page or an auth redirect.
	if !verifyOnSearchPage(page) {
		s.SetState(StateUnauthenticated, "search navigation redirected to login/flow")
		writeSearchError(w, http.StatusServiceUnavailable, errClassAuthExpired, "session unauthenticated")
		return
	}
	// Verify passed inline on this page load — mark healthy. Bypasses the
	// separate /home hop that EnsureAuthenticated does.
	s.SetState(StateHealthy, "verified inline with search")

	// Wait for at least one tweet to render. Absent = legitimate empty
	// result (query matched nothing in the age window), NOT an error.
	if _, err := page.WaitForSelector(
		`article[data-testid='tweet']`,
		playwright.PageWaitForSelectorOptions{Timeout: playwright.Float(float64(s.tweetFeedTimeout / time.Millisecond))},
	); err != nil {
		writeSearchOK(w, SearchResponse{
			Status:     "success",
			Videos:     nil,
			Count:      0,
			Query:      req.Query,
			StopReason: stopEmpty,
			Scrolls:    0,
			Elapsed:    time.Since(start).String(),
		})
		return
	}

	// Wait for the first tweet to hydrate — the initial paint shows shells
	// before the text populates. Event-driven: proceed the instant the tweet
	// text renders, capped at 2s so a slow or text-less (bare-video) tweet
	// still proceeds — never worse than the old blind 2s sleep. Error is
	// intentionally ignored: a miss just falls through to extraction, and
	// the per-scroll loop re-extracts anyway.
	_, _ = page.WaitForSelector(
		`article[data-testid='tweet'] [data-testid='tweetText']`,
		playwright.PageWaitForSelectorOptions{Timeout: playwright.Float(2000)},
	)

	videos, stopReason, scrolls, extractErr := s.scrollAndExtract(r.Context(), page, excludeIDs, maxAgeMinutes)
	if extractErr != nil {
		writeSearchError(w, http.StatusInternalServerError, errClassInternal, "extract: "+extractErr.Error())
		return
	}

	// Persist any rotated csrf tokens to the shared file. Fingerprint
	// dedupe means no-op writes when nothing rotated.
	_ = s.BackupCookies(r.Context())

	writeSearchOK(w, SearchResponse{
		Status:     "success",
		Videos:     videos,
		Count:      len(videos),
		Query:      req.Query,
		StopReason: stopReason,
		Scrolls:    scrolls,
		Elapsed:    time.Since(start).String(),
	})
}

// buildSearchURL composes the Twitter search URL. Query is
// URL-encoded; `filter:videos` is expected to be in req.Query already
// (Discovery's query builder appends it per D1). URL params
// src=typed_query + f=live are the service's concern.
func buildSearchURL(query string) string {
	return fmt.Sprintf(
		"https://x.com/search?q=%s&src=typed_query&f=live",
		url.QueryEscape(query),
	)
}

// verifyOnSearchPage decides whether the loaded search page is an
// authenticated session, leaning on the RELIABLE signal (the login
// redirect) rather than the presence of a specific decorative element.
//
// X always redirects logged-out users to /login or /i/flow/…, so a
// redirect is the authoritative "unauthenticated". A logged-in page is
// confirmed by any app-shell element — primaryColumn (the main content
// column, present on every authed page incl. search and painted early),
// with the sidebar AccountSwitcher kept as a fallback. If NEITHER positive
// element appears but there is ALSO no login redirect, the session is
// almost certainly valid (X would have redirected), so we proceed rather
// than fail: a genuinely broken page then yields an empty result via the
// tweet-feed wait instead of a spurious 500.
//
// The old code required the AccountSwitcher button specifically, which
// does not reliably render on the search page under headless — ~17% of
// searches false-failed to HTTP 500 with a perfectly valid session
// (decisions.md 2026-08-12).
func verifyOnSearchPage(page playwright.Page) bool {
	// Authoritative negative: a login/flow redirect (may already have
	// happened during Navigate).
	if u := page.URL(); strings.Contains(u, "/login") || strings.Contains(u, "/flow/") {
		return false
	}
	// Positive: any logged-in app-shell element. Short timeout — the shell
	// paints quickly when the session is valid.
	if _, err := page.WaitForSelector(
		`[data-testid='primaryColumn'], [data-testid='SideNav_AccountSwitcher_Button']`,
		playwright.PageWaitForSelectorOptions{Timeout: playwright.Float(5000)},
	); err == nil {
		return true
	}
	// Re-check the URL — the redirect may have landed during the wait.
	if u := page.URL(); strings.Contains(u, "/login") || strings.Contains(u, "/flow/") {
		return false
	}
	// No shell element AND no login redirect: logged-out users always get
	// redirected, so the session is valid — proceed. A broken page falls
	// through to the tweet-feed wait, which returns an empty result rather
	// than a spurious 500.
	return true
}

// scrollAndExtract implements the scroll loop with four stop conditions
// (age / max_scrolls / empty / consecutive_already_seen). Returns the
// videos collected, the stop reason that terminated the loop, and the
// scroll count actually performed.
//
// DOM extraction happens in a single JS block per scroll (see
// extractTweetsJS) rather than per-tweet Playwright locators to
// minimize IPC round-trips.
func (s *Service) scrollAndExtract(
	ctx context.Context,
	page playwright.Page,
	excludeIDs map[string]struct{},
	maxAgeMinutes int,
) (videos []VideoRef, stopReason string, scrolls int, err error) {
	processed := make(map[string]struct{}) // tweet IDs seen in this scroll session
	consecutiveSeen := 0

	for scrollCount := 0; scrollCount < s.maxScrolls; scrollCount++ {
		if err := ctx.Err(); err != nil {
			return videos, stopReason, scrollCount, err
		}

		raw, extractErr := page.Evaluate(extractTweetsJS)
		if extractErr != nil {
			return videos, stopReason, scrollCount, fmt.Errorf("evaluate: %w", extractErr)
		}
		tweets, parseErr := decodeExtractResult(raw)
		if parseErr != nil {
			return videos, stopReason, scrollCount, fmt.Errorf("decode extract: %w", parseErr)
		}

		for _, t := range tweets {
			tid := extractTweetIDFromURL(t.TweetURL)
			if tid == "" || tid == "unknown" {
				continue
			}
			if _, dup := processed[tid]; dup {
				continue
			}
			processed[tid] = struct{}{}

			// Stop #1: tweet older than max_age_minutes → stop scroll.
			// Only meaningful if we have an age (t.AgeMinutes > 0);
			// missing age falls through (rare — Twitter usually renders
			// the <time datetime> attribute).
			if t.AgeMinutes > 0 && t.AgeMinutes > float64(maxAgeMinutes) {
				return videos, stopAge, scrollCount, nil
			}

			// Filters (silent skips — not stop conditions).
			if t.IsPromoted {
				continue
			}
			if !t.HasVideo {
				continue
			}
			if isTruncatedSnowflake(tid) {
				continue
			}

			// Stop #4 (NEW vs Python): consecutive_already_seen counter.
			// Counter increments on each excluded tweet, RESETS on any
			// new-to-us tweet. Kills late-attempt scrolls through mostly
			// exclude_urls-covered feeds.
			if _, excluded := excludeIDs[tid]; excluded {
				consecutiveSeen++
				if consecutiveSeen >= s.consecutiveSeenStop {
					return videos, stopConsecutiveSeen, scrollCount, nil
				}
				continue
			}
			consecutiveSeen = 0 // any surviving tweet resets

			videos = append(videos, VideoRef{
				TweetURL:        t.TweetURL,
				TweetText:       truncate(t.Text, 200),
				VideoPageURL:    fmt.Sprintf("https://x.com/i/status/%s", tid),
				DurationSeconds: t.DurationSeconds,
				Username:        extractUsernameFromURL(t.TweetURL),
				AgeMinutes:      t.AgeMinutes,
			})
		}

		// Stop #3: empty page after >= 1 scroll. First-load emptiness
		// is handled upstream (WaitForSelector timeout before this
		// loop starts), so an empty page here means the feed has
		// exhausted mid-scroll.
		if len(tweets) == 0 && scrollCount >= 1 {
			return videos, stopEmpty, scrollCount, nil
		}

		// Scroll one viewport height. Playwright's evaluate is fire-
		// and-forget for the scroll effect — return value ignored.
		if _, err := page.Evaluate(`() => window.scrollBy(0, window.innerHeight)`); err != nil {
			return videos, stopReason, scrollCount, fmt.Errorf("scroll: %w", err)
		}

		// Timing jitter — random 500-3000ms sleep between scrolls.
		// Baseline stealth #4 per twitter-port.md T/c.
		jitter := s.scrollJitterMin + time.Duration(rand.IntN(int(s.scrollJitterMax-s.scrollJitterMin)))
		time.Sleep(jitter)
	}

	// Stop #2: max_scrolls exhausted (loop counter fell off the top).
	return videos, stopMaxScrolls, s.maxScrolls, nil
}

// extractTweetsJS is the single JS evaluate that pulls every visible
// tweet's fields in one IPC round-trip. Mirrors scrape.py's helpers
// (extract_status_link, extract_tweet_age_minutes, is_promoted_tweet,
// extract_tweet_text, extract_video_duration) but returns raw values
// — age calculation stays in Go where time.Parse is less footgun-y
// than new Date() timezone math.
const extractTweetsJS = `() => {
	const out = [];
	const nodes = document.querySelectorAll("article[data-testid='tweet']");
	for (const n of nodes) {
		// Tweet URL — first /status/ href.
		const statusLink = n.querySelector("a[href*='/status/']");
		if (!statusLink) continue;

		// Text (may be absent — tweet is only a video, no caption).
		const textEl = n.querySelector("[data-testid='tweetText']");
		const text = textEl ? textEl.innerText : "";

		// Age — raw ISO datetime from the <time> element. Go parses.
		const timeEl = n.querySelector("time[datetime]");
		const datetime = timeEl ? timeEl.getAttribute("datetime") : "";

		// Promoted / Ad label — text search on the tweet's content.
		const bodyText = n.textContent || "";
		const isPromoted = /\bPromoted\b|\bAd\b/.test(bodyText);

		// Video presence + duration — walk multiple selectors like
		// scrape.py's extract_video_duration, then try to pull the
		// duration attribute or an M:SS overlay.
		let hasVideo = false;
		let durationSeconds = 0;
		for (const sel of ["video", "[data-testid='videoPlayer']", "[data-testid='videoComponent']"]) {
			const el = n.querySelector(sel);
			if (!el) continue;
			hasVideo = true;

			// Try duration attribute first (Playwright's video element).
			const attrDur = el.duration || parseFloat(el.getAttribute("duration") || "0");
			if (attrDur && !isNaN(attrDur)) {
				durationSeconds = attrDur;
				break;
			}

			// Fallback: parse M:SS overlay text.
			for (const durSel of [
				"[aria-label*='Duration']",
				"[data-testid='videoPlayerDuration']",
			]) {
				const durEl = n.querySelector(durSel);
				if (!durEl) continue;
				const t = (durEl.textContent || "").trim();
				const m = t.match(/^(\d+):(\d{2})$/);
				if (m) {
					durationSeconds = parseInt(m[1]) * 60 + parseInt(m[2]);
					break;
				}
			}
			break;
		}

		out.push({
			tweet_url: statusLink.href,
			text: text,
			datetime: datetime,
			is_promoted: isPromoted,
			has_video: hasVideo,
			duration_seconds: durationSeconds,
		});
	}
	return out;
}`

// extractedTweet is the Go-side shape mirroring the JS output. Ages
// computed from datetime in Go.
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

// truncate returns s truncated to max bytes. Used for tweet text —
// downstream storage expects ≤200 chars per scrape.py's default.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
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
