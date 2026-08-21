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
//  5. Wait for the first tweet — up to 10s. Only a real timeout is a
//     legitimate empty result; other Playwright failures are errors.
//  6. Scroll loop with four stop conditions (age / max_scrolls /
//     feed_exhausted / consecutive_already_seen). DOM extraction via a single JS
//     evaluate per scroll for IPC efficiency.
//  7. BackupCookies on success — persists Twitter's rotated csrf
//     tokens to the shared file. Fingerprint dedupe skips no-op writes.
//  8. Return SearchResponse with videos and feed/extraction diagnostics.
//
// Stealth: random 250–500ms jitter between scroll actions (baseline #4
// per twitter-port.md T/c). User-Agent + Accept-Language rotation is
// deferred to T/b.5 hardening.
package twitter

import (
	"encoding/json"
	"errors"
	"fmt"
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
// telemetry fields are omitempty so the
// S7 client can ignore them without decode errors.
type SearchResponse struct {
	Status          string     `json:"status"` // "success"
	Videos          []VideoRef `json:"videos"`
	Count           int        `json:"count"`
	Query           string     `json:"query,omitempty"`
	StopReason      string     `json:"stop_reason,omitempty"`
	Scrolls         int        `json:"scrolls,omitempty"`
	InitialArticles int        `json:"initial_articles,omitempty"`
	TweetsParsed    int        `json:"tweets_parsed,omitempty"`
	VideoTweets     int        `json:"video_tweets,omitempty"`
	Elapsed         string     `json:"elapsed,omitempty"`
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
	stopFeedTimeout     = "feed_timeout"
	stopFeedExhausted   = "feed_exhausted"
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

	// Mark busy for /status visibility. Held for the whole search (nav +
	// scroll + backup). /status reflects this via `busy: true`.
	s.setBusy(true)
	defer s.setBusy(false)

	// mtime check — reload cookies if the shared file has been updated
	// since we last loaded (raw re-auth captured, another instance
	// wrote fresh cookies). No verify hop — we're about to navigate to
	// the search URL anyway and check the switcher button there.
	s.mu.RLock()
	lastLoadedMtime := s.lastLoadedMtime
	s.mu.RUnlock()
	// A malformed backup is absorbed inside maybeReloadCookies and the current
	// browser session remains usable. A browser-level replacement failure is
	// terminal for this process and must not be laundered into an empty search.
	if _, err := s.maybeReloadCookies(r.Context(), lastLoadedMtime); err != nil {
		writeSearchError(w, http.StatusInternalServerError, errClassInternal, "reload cookies: "+err.Error())
		return
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

	articles := page.Locator(`article[data-testid='tweet']`)
	// Wait for at least one tweet to render. Absent remains a successful empty
	// result, but gets its own stop class so it cannot be confused with a feed
	// that rendered and later exhausted.
	if err := articles.First().WaitFor(
		playwright.LocatorWaitForOptions{Timeout: playwright.Float(float64(s.tweetFeedTimeout / time.Millisecond))},
	); err != nil {
		// Only a real timeout means "no feed rendered." Locator contract
		// failures, page closure, and other Playwright errors must retry through
		// Temporal instead of being laundered into a successful empty result.
		if !errors.Is(err, playwright.ErrTimeout) {
			writeSearchError(w, http.StatusInternalServerError, errClassInternal, "wait for tweet feed: "+err.Error())
			return
		}
		// Authentication was proven on this navigation. Preserve any cookie
		// refresh even when the feed itself did not render; persistence failure
		// is exposed through /status and audit logs without discarding a valid
		// empty search result.
		_ = s.BackupCookies(r.Context())
		writeSearchOK(w, SearchResponse{
			Status:     "success",
			Videos:     nil,
			Count:      0,
			Query:      req.Query,
			StopReason: stopFeedTimeout,
			Scrolls:    0,
			Elapsed:    time.Since(start).String(),
		})
		return
	}
	initialArticles, _ := articles.Count()

	// Let the first rendered tweet finish painting before extraction. The feed
	// itself is already proven present above; this short, best-effort wait only
	// reduces partial-DOM reads and must not redefine what counts as a result.
	_ = page.Locator(
		`article[data-testid='tweet'] [data-testid='tweetText']`,
	).First().WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(2000),
	})

	videos, stopReason, scrolls, stats, extractErr := s.scrollAndExtract(r.Context(), page, excludeIDs, maxAgeMinutes)
	if extractErr != nil {
		writeSearchError(w, http.StatusInternalServerError, errClassInternal, "extract: "+extractErr.Error())
		return
	}

	// Persist any rotated csrf tokens to the shared file. Fingerprint
	// dedupe means no-op writes when nothing rotated.
	_ = s.BackupCookies(r.Context())

	writeSearchOK(w, SearchResponse{
		Status:          "success",
		Videos:          videos,
		Count:           len(videos),
		Query:           req.Query,
		StopReason:      stopReason,
		Scrolls:         scrolls,
		InitialArticles: initialArticles,
		TweetsParsed:    stats.tweetsParsed,
		VideoTweets:     stats.videoTweets,
		Elapsed:         time.Since(start).String(),
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
	if err := page.Locator(
		`[data-testid='primaryColumn'], [data-testid='SideNav_AccountSwitcher_Button']`,
	).First().WaitFor(playwright.LocatorWaitForOptions{Timeout: playwright.Float(5000)}); err == nil {
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
// (age / max_scrolls / feed_exhausted / consecutive_already_seen). Returns the
// videos collected, the stop reason that terminated the loop, and the
// scroll count actually performed.
//
// DOM extraction happens in a single JS block per scroll (see
// extractTweetsJS) rather than per-tweet Playwright locators to
// minimize IPC round-trips.
