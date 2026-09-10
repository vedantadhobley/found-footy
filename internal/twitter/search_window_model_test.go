// Offline search-window policy model; never compiled into the Twitter service.
package twitter

import (
	"context"
	"fmt"
	"time"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
)

type windowPolicy string

const (
	windowRolling    windowPolicy = "rolling_3m_seen3"
	windowFixed      windowPolicy = "first_seen_minus_3m_seen3"
	windowExact      windowPolicy = "first_seen_seen3"
	windowReference  windowPolicy = "first_seen_minus_3m_no_seen_stop"
	windowCaptureEnd              = "capture_end"
)

// windowCheckpoint models persisted policy inputs, not the as-built SQL schema.
// Each policy owns a separate candidate history, including after serialization.
type windowCheckpoint struct {
	Policy          windowPolicy
	EarliestTweetAt time.Time
	SeenURLs        []string
}

// windowBatch records ordered, already-extracted DOM observations. At is the
// extraction time, not the query start; the deployed rolling rule uses that time.
type windowBatch struct {
	At     time.Time
	Tweets []extractedTweet
}

type windowAttempt struct {
	State      twittercontract.ResultState
	Batches    []windowBatch
	MaxScrolls int
}

// windowResult counts simulated extraction/scroll work, never X HTTP requests.
// Capture end and scroll cap explicitly do not prove complete search coverage.
type windowResult struct {
	URLs        []string
	Stop        string
	Extractions int
	Scrolls     int
	Parsed      int
	VideoTweets int
}

// newWindowCheckpoint fixes the anchor before any search can succeed or fail.
// A missing event timestamp needs an explicit production policy, not a now fallback.
func newWindowCheckpoint(policy windowPolicy, firstSeen time.Time) (windowCheckpoint, error) {
	if firstSeen.IsZero() {
		return windowCheckpoint{}, fmt.Errorf("missing event first-seen timestamp")
	}
	cutoff := firstSeen.Add(-3 * time.Minute)
	switch policy {
	case windowRolling, windowFixed, windowReference:
	case windowExact:
		cutoff = firstSeen
	default:
		return windowCheckpoint{}, fmt.Errorf("unknown policy %q", policy)
	}
	return windowCheckpoint{Policy: policy, EarliestTweetAt: cutoff.UTC()}, nil
}

// runWindowAttempt is an explicit experiment model of search_scroll.go's order:
// within-page dedup, promoted skip, time stop, media/ID filters, then seen stop.
// The conformance test separately runs the real production loop against fake DOM
// batches, so this model cannot silently redefine the rolling baseline.
func runWindowAttempt(ctx context.Context, checkpoint *windowCheckpoint, attempt windowAttempt) (windowResult, error) {
	var out windowResult
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !attempt.State.Usable() {
		out.Stop = "unavailable"
		return out, nil
	}
	if attempt.State == twittercontract.ResultExplicitEmpty {
		out.Stop = stopExplicitEmpty
		return out, nil
	}
	excluded := normalizeExcludeIDs(checkpoint.SeenURLs)
	processed := make(map[string]bool)
	consecutiveSeen := 0
	for i := 0; i < attempt.MaxScrolls; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if i >= len(attempt.Batches) {
			out.Stop = windowCaptureEnd
			break
		}
		batch := attempt.Batches[i]
		out.Extractions++
		for _, tweet := range batch.Tweets {
			id := extractTweetIDFromURL(tweet.TweetURL)
			if id == "" || id == "unknown" || processed[id] {
				continue
			}
			processed[id] = true
			out.Parsed++
			if tweet.HasVideo {
				out.VideoTweets++
			}
			if tweet.IsPromoted {
				continue
			}
			if windowStopsAtTime(checkpoint, tweet, batch.At) {
				out.Stop = stopAge
				break
			}
			if !tweet.HasVideo || isTruncatedSnowflake(id) {
				continue
			}
			if _, found := excluded[id]; found {
				consecutiveSeen++
				if checkpoint.Policy != windowReference && consecutiveSeen >= 3 {
					out.Stop = stopConsecutiveSeen
					break
				}
				continue
			}
			consecutiveSeen = 0
			out.URLs = append(out.URLs, tweet.TweetURL)
		}
		if out.Stop != "" {
			break
		}
		if len(batch.Tweets) == 0 && i >= 1 {
			out.Stop = stopFeedExhausted
			break
		}
		out.Scrolls++
	}
	if out.Stop == "" {
		out.Stop = stopMaxScrolls
	}
	checkpoint.SeenURLs = append(checkpoint.SeenURLs, out.URLs...)
	return out, nil
}

// windowStopsAtTime preserves the deployed unknown-time behavior. Unknown time
// does not prove that a tweet is eligible or that everything below it is older.
func windowStopsAtTime(checkpoint *windowCheckpoint, tweet extractedTweet, observedAt time.Time) bool {
	published, err := time.Parse(time.RFC3339, tweet.Datetime)
	if err != nil {
		return false
	}
	if checkpoint.Policy == windowRolling {
		tweet.AgeMinutes = observedAt.Sub(published).Minutes()
		return shouldStopAtAge(tweet, 3)
	}
	return !tweet.IsPromoted && published.Before(checkpoint.EarliestTweetAt)
}
