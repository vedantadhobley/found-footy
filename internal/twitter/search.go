// Minimal search endpoint — navigates x.com/search?q=<q>&f=live
// (Latest tab, reverse-chronological), waits for the tweet feed to
// render, extracts a handful of tweets from the DOM, returns JSON.
// This is a T/a-plus-one PoC: it proves we can actually pull tweets
// from Go, not just log in. Real T/c work (scroll loop, exhaustive
// extraction, exclude_urls handling, timing jitter) lands separately.
package twitter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// SearchResult is one tweet the DOM extraction found.
type SearchResult struct {
	TweetURL string `json:"tweet_url"`
	Username string `json:"username"`
	Text     string `json:"text"`
}

// SearchResponse is the /search endpoint payload.
type SearchResponse struct {
	Query   string         `json:"query"`
	URL     string         `json:"url"`
	Count   int            `json:"count"`
	Tweets  []SearchResult `json:"tweets"`
	Elapsed string         `json:"elapsed"`
}

// handleSearch registers ?q=<query> as the search input, navigates to
// Twitter's search Latest tab, waits for the tweet feed, extracts up
// to 20 tweets, returns JSON. Serialized on the browser instance —
// concurrent calls block on b.mu (declared in browser.go if needed;
// for the PoC we assume single-caller).
func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, `{"error":"missing q parameter"}`, http.StatusBadRequest)
		return
	}
	state, _ := s.State()
	if state != StateHealthy {
		http.Error(w, `{"error":"service not healthy","state":"`+string(state)+`"}`, http.StatusServiceUnavailable)
		return
	}

	start := time.Now()
	searchURL := fmt.Sprintf(
		"https://x.com/search?q=%s&src=typed_query&f=live",
		url.QueryEscape(q),
	)

	page, err := s.browser.Navigate(r.Context(), searchURL, 30*time.Second)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "navigation failed: " + err.Error(),
			"url":   searchURL,
		})
		return
	}
	defer func() { _ = page.Close() }()

	// Wait for the primary feed to render at least one tweet.
	if _, err := page.WaitForSelector(
		`article[data-testid='tweet']`,
		playwright.PageWaitForSelectorOptions{Timeout: playwright.Float(15000)},
	); err != nil {
		writeJSON(w, http.StatusOK, SearchResponse{
			Query:   q,
			URL:     searchURL,
			Count:   0,
			Tweets:  nil,
			Elapsed: time.Since(start).String(),
		})
		return
	}

	// Give the SPA a moment to hydrate — Twitter's initial paint
	// often shows shells before the tweet text populates.
	time.Sleep(2 * time.Second)

	tweetsRaw, err := page.Evaluate(`() => {
		const results = [];
		const nodes = document.querySelectorAll("article[data-testid='tweet']");
		for (const n of nodes) {
			const statusLink = n.querySelector("a[href*='/status/']");
			if (!statusLink) continue;
			const url = statusLink.href;
			const nameEl = n.querySelector("[data-testid='User-Name']");
			const textEl = n.querySelector("[data-testid='tweetText']");
			results.push({
				tweet_url: url,
				username: nameEl ? nameEl.innerText.split('\n')[0] : '',
				text: textEl ? textEl.innerText : ''
			});
			if (results.length >= 20) break;
		}
		return results;
	}`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "extract failed: " + err.Error(),
		})
		return
	}

	// Round-trip through JSON to normalize any playwright Go interface
	// values into our typed SearchResult slice.
	tweetsJSON, _ := json.Marshal(tweetsRaw)
	var tweets []SearchResult
	_ = json.Unmarshal(tweetsJSON, &tweets)

	writeJSON(w, http.StatusOK, SearchResponse{
		Query:   q,
		URL:     searchURL,
		Count:   len(tweets),
		Tweets:  tweets,
		Elapsed: time.Since(start).String(),
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
