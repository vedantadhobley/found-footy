// Conformance checks run the deployed scroll loop on the offline model's DOM batches.
package twitter

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

// windowPage needs no browser: the scroll loop uses only Evaluate. Embedding
// makes any unexpected call outside that seam fail instead of silently succeeding.
type windowPage struct {
	playwright.Page
	Batches        []windowBatch
	Scrolls        int
	Extractions    int
	KeepTimestamps bool
}

// Evaluate preserves each batch's ages relative to the real decoder's clock.
// Conformance inputs stay well away from boundaries; deterministic model tests
// separately pin nanosecond equality without changing the production clock.
func (p *windowPage) Evaluate(expression string, _ ...any) (any, error) {
	switch expression {
	case extractTweetsJS:
		if p.Scrolls >= len(p.Batches) {
			return nil, fmt.Errorf("test capture ended without a terminal observation")
		}
		p.Extractions++
		batch := p.Batches[p.Scrolls]
		tweets := slices.Clone(batch.Tweets)
		now := time.Now().UTC()
		for i := range tweets {
			if published, err := time.Parse(time.RFC3339, tweets[i].Datetime); err == nil && !p.KeepTimestamps {
				tweets[i].Datetime = now.Add(published.Sub(batch.At)).Format(time.RFC3339Nano)
			}
		}
		return tweets, nil
	case `() => window.scrollBy(0, window.innerHeight)`:
		p.Scrolls++
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected page evaluation: %q", expression)
	}
}

// TestSearchWindowRollingConformance anchors the research model to current code.
func TestSearchWindowRollingConformance(t *testing.T) {
	for _, scenario := range windowScenarios() {
		t.Run(scenario.Name, func(t *testing.T) {
			checkpoint, err := newWindowCheckpoint(windowRolling, windowAnchor())
			require.NoError(t, err)
			for _, attempt := range scenario.Attempts {
				if !attempt.State.Usable() {
					continue // Feed-state classification runs before the production loop.
				}
				excluded := normalizeExcludeIDs(checkpoint.SeenURLs)
				want, err := runWindowAttempt(context.Background(), &checkpoint, attempt)
				require.NoError(t, err)
				assertWindowConformance(t, attempt, excluded, want)
			}
		})
	}

	// An old organic text tweet stops before the video filter; an ad does not.
	old := windowTweet(90, -4*time.Minute)
	old.HasVideo = false
	ad := old
	ad.TweetURL, ad.IsPromoted = windowURL(91), true
	missing := windowTweet(92, 0)
	missing.Datetime = ""
	for name, attempt := range map[string]windowAttempt{
		"old_organic_before_newer_video": windowFeed(time.Minute, old, windowTweet(1, 0)),
		"promoted_and_missing_time":      windowFeed(time.Minute, ad, missing, windowTweet(1, 0), old),
		"empty_after_scroll": {MaxScrolls: 10, Batches: []windowBatch{
			{At: windowAnchor()}, {At: windowAnchor()},
		}},
		"seen_counter_ignores_text_and_repeated_dom": windowFeed(time.Minute,
			windowTweet(1, 0), windowTweet(1, 0), extractedTweet{TweetURL: windowURL(80)},
			windowTweet(2, 0), windowTweet(3, 0), windowTweet(4, 0)),
	} {
		t.Run(name, func(t *testing.T) {
			checkpoint, err := newWindowCheckpoint(windowRolling, windowAnchor())
			require.NoError(t, err)
			checkpoint.SeenURLs = []string{windowURL(1), windowURL(2), windowURL(3)}
			excluded := normalizeExcludeIDs(checkpoint.SeenURLs)
			attempt.State = "rendered"
			want, err := runWindowAttempt(context.Background(), &checkpoint, attempt)
			require.NoError(t, err)
			assertWindowConformance(t, attempt, excluded, want)
		})
	}
}

// assertWindowConformance checks result and bounded work, not just candidate count.
func assertWindowConformance(t *testing.T, attempt windowAttempt, excluded map[string]struct{}, want windowResult) {
	t.Helper()
	page := &windowPage{Batches: attempt.Batches}
	service := &Service{maxScrolls: attempt.MaxScrolls, consecutiveSeenStop: 3}
	videos, stop, scrolls, stats, err := service.scrollAndExtract(context.Background(), page, excluded, 3, nil)
	require.NoError(t, err)
	var urls []string
	for _, video := range videos {
		urls = append(urls, video.TweetURL)
	}
	require.Equal(t, want, windowResult{URLs: urls, Stop: stop, Extractions: page.Extractions,
		Scrolls: scrolls, Parsed: stats.tweetsParsed, VideoTweets: stats.videoTweets})
	require.Equal(t, scrolls, page.Scrolls)
}
