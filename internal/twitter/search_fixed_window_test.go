// Fixed-window browser tests run the real scroll loop without Firefox or X traffic.
package twitter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
)

// TestFixedSearchWindowScansPastKnownPrefixUntilEligible reproduces the partial
// scan counterexample against production code, not the experiment's model.
func TestFixedSearchWindowScansPastKnownPrefixUntilEligible(t *testing.T) {
	feed := windowFeed(8*time.Minute, windowTweet(4, 40*time.Second), windowTweet(3, 30*time.Second),
		windowTweet(2, 20*time.Second), windowTweet(1, 10*time.Second), windowTweet(99, -4*time.Minute))
	excluded := normalizeExcludeIDs([]string{windowURL(4), windowURL(3), windowURL(2)})
	for _, allowed := range []bool{false, true} {
		page := &windowPage{Batches: feed.Batches, KeepTimestamps: true}
		service := &Service{maxScrolls: 10, consecutiveSeenStop: 3}
		window := &twittercontract.SearchWindow{EarliestTweetAt: windowAnchor().Add(-3 * time.Minute), AllowSeenStop: allowed}
		videos, stop, _, _, err := service.scrollAndExtract(context.Background(), page, excluded, 0, window)
		require.NoError(t, err)
		if allowed {
			require.Empty(t, videos)
			require.Equal(t, stopConsecutiveSeen, stop)
		} else {
			require.Len(t, videos, 1)
			require.Equal(t, windowURL(1), videos[0].TweetURL)
			require.Equal(t, stopAge, stop)
		}
	}
}

// TestFixedSearchWindowMatchesBufferedReference starts conservatively and
// preserves the synthetic outage/pre-observation candidates across all cases.
func TestFixedSearchWindowMatchesBufferedReference(t *testing.T) {
	for _, scenario := range windowScenarios() {
		t.Run(scenario.Name, func(t *testing.T) {
			window := twittercontract.SearchWindow{EarliestTweetAt: windowAnchor().Add(-3 * time.Minute)}
			var urls []string
			for _, attempt := range scenario.Attempts {
				if !attempt.State.Usable() {
					window = window.After(attempt.State, "")
					continue
				}
				page := &windowPage{Batches: attempt.Batches, KeepTimestamps: true}
				service := &Service{maxScrolls: attempt.MaxScrolls, consecutiveSeenStop: 3}
				videos, stop, scrolls, _, err := service.scrollAndExtract(context.Background(), page, normalizeExcludeIDs(urls), 0, &window)
				require.NoError(t, err)
				require.LessOrEqual(t, scrolls, attempt.MaxScrolls)
				for _, video := range videos {
					urls = append(urls, video.TweetURL)
				}
				window = window.After(attempt.State, stop)
			}
			var want []string
			for _, id := range scenario.WantIDs[windowReference] {
				want = append(want, windowURL(id))
			}
			require.Equal(t, want, urls)
		})
	}
}

// TestFixedSearchWindowBoundaryAndGuards pins the runtime timestamp predicate and
// ensures invalid HTTP bounds fail before the handler touches the browser.
func TestFixedSearchWindowBoundaryAndGuards(t *testing.T) {
	window := &twittercontract.SearchWindow{EarliestTweetAt: windowAnchor()}
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		tweet := windowTweet(1, offset)
		require.Equal(t, offset < 0, shouldStopAtTime(tweet, 1, window))
		tweet.IsPromoted = true
		require.False(t, shouldStopAtTime(tweet, 1, window))
	}
	for _, timestamp := range []string{"", "not-a-time"} {
		require.False(t, shouldStopAtTime(extractedTweet{Datetime: timestamp}, 1, window))
	}
	for _, body := range []string{
		`{"query":"goal","window":{}}`,
		`{"query":"goal","window":{"earliest_tweet_at":"invalid"}}`,
		`{"query":"goal","max_age_minutes":3,"window":{"earliest_tweet_at":"2026-09-09T20:00:00Z"}}`,
	} {
		recorder := httptest.NewRecorder()
		(&Service{}).handleSearch(recorder, httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(body)))
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	}
}
