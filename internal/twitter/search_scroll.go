// Twitter search-feed scrolling and browser-side extraction script.
package twitter

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/mxschmitt/playwright-go"
)

type searchStats struct {
	tweetsParsed int
	videoTweets  int
}

func (s *Service) scrollAndExtract(
	ctx context.Context,
	page playwright.Page,
	excludeIDs map[string]struct{},
	maxAgeMinutes int,
) (videos []VideoRef, stopReason string, scrolls int, stats searchStats, err error) {
	processed := make(map[string]struct{}) // tweet IDs seen in this scroll session
	consecutiveSeen := 0

	for scrollCount := 0; scrollCount < s.maxScrolls; scrollCount++ {
		if err := ctx.Err(); err != nil {
			return videos, stopReason, scrollCount, stats, err
		}

		raw, extractErr := page.Evaluate(extractTweetsJS)
		if extractErr != nil {
			return videos, stopReason, scrollCount, stats, fmt.Errorf("evaluate: %w", extractErr)
		}
		tweets, parseErr := decodeExtractResult(raw)
		if parseErr != nil {
			return videos, stopReason, scrollCount, stats, fmt.Errorf("decode extract: %w", parseErr)
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
			stats.tweetsParsed++
			if t.HasVideo {
				stats.videoTweets++
			}

			// Promoted entries are inserted independently of chronological order.
			// Ignore them before the age stop so one stale ad cannot hide newer
			// organic results below it.
			if t.IsPromoted {
				continue
			}

			// Stop #1: organic tweet older than max_age_minutes → stop scroll.
			// Only meaningful if we have an age (t.AgeMinutes > 0);
			// missing age falls through (rare — Twitter usually renders
			// the <time datetime> attribute).
			if shouldStopAtAge(t, maxAgeMinutes) {
				return videos, stopAge, scrollCount, stats, nil
			}

			// Filters (silent skips — not stop conditions).
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
					return videos, stopConsecutiveSeen, scrollCount, stats, nil
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
			return videos, stopFeedExhausted, scrollCount, stats, nil
		}

		// Scroll one viewport height. Playwright's evaluate is fire-
		// and-forget for the scroll effect — return value ignored.
		if _, err := page.Evaluate(`() => window.scrollBy(0, window.innerHeight)`); err != nil {
			return videos, stopReason, scrollCount, stats, fmt.Errorf("scroll: %w", err)
		}

		// Timing jitter — random 250-500ms sleep between scrolls by default.
		// Baseline stealth #4 per twitter-port.md T/c.
		// This jitter varies UI cadence; it does not generate a secret or token.
		if err := waitForScroll(ctx, nextScrollJitter(s.scrollJitterMin, s.scrollJitterMax)); err != nil {
			return videos, stopReason, scrollCount, stats, err
		}
	}

	// Stop #2: max_scrolls exhausted (loop counter fell off the top).
	return videos, stopMaxScrolls, s.maxScrolls, stats, nil
}

func shouldStopAtAge(tweet extractedTweet, maxAgeMinutes int) bool {
	return !tweet.IsPromoted &&
		tweet.AgeMinutes > 0 &&
		tweet.AgeMinutes > float64(maxAgeMinutes)
}

func nextScrollJitter(minimum, maximum time.Duration) time.Duration {
	delta := maximum - minimum
	if delta <= 0 {
		return minimum
	}
	return minimum + time.Duration(rand.Int64N(int64(delta))) //nolint:gosec
}

func waitForScroll(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
