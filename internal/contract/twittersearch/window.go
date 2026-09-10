// Fixed search boundaries and conservative early-stop eligibility shared across discovery.
package twittersearch

import (
	"fmt"
	"time"
)

// SearchWindow fixes the oldest eligible tweet independently of retry time.
// AllowSeenStop permits a heuristic join to previously scanned results; it is
// not proof that X returned every relevant post. Responses echo the applied input.
type SearchWindow struct {
	EarliestTweetAt time.Time `json:"earliest_tweet_at"`
	AllowSeenStop   bool      `json:"allow_seen_stop"`
}

// Validate rejects a missing boundary instead of silently falling back to now.
func (w SearchWindow) Validate() error {
	if w.EarliestTweetAt.IsZero() {
		return fmt.Errorf("search window requires earliest_tweet_at")
	}
	return nil
}

// Equal compares instants, not time-zone representations, across JSON/SQL codecs.
func (w SearchWindow) Equal(other SearchWindow) bool {
	return w.EarliestTweetAt.Equal(other.EarliestTweetAt) && w.AllowSeenStop == other.AllowSeenStop
}

// After permits the next shortcut only after reaching the floor or joining a
// previously eligible scan. Empty, capped, unavailable, and unknown stops revoke
// eligibility. This never changes the immutable timestamp.
func (w SearchWindow) After(state ResultState, stopReason string) SearchWindow {
	w.AllowSeenStop = state == ResultRendered &&
		(stopReason == "age" || (stopReason == "consecutive_seen" && w.AllowSeenStop))
	return w
}

// ValidateWindow keeps absolute and legacy relative bounds mutually exclusive.
func (r SearchRequest) ValidateWindow() error {
	if r.Window == nil {
		return nil // Historical callers retain the relative default and semantics.
	}
	if r.MaxAgeMinutes != 0 {
		return fmt.Errorf("window and max_age_minutes are mutually exclusive")
	}
	return r.Window.Validate()
}
