// Search-window wire and eligibility tests pin compatibility and conservative transitions.
package twittersearch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSearchWindowAfter cannot grant a shortcut from a partial or unavailable scan.
func TestSearchWindowAfter(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		window := SearchWindow{EarliestTweetAt: time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC), AllowSeenStop: allowed}
		for _, state := range []ResultState{ResultRendered, ResultExplicitEmpty, ResultLogin, ResultUpstreamError, ResultUnknownTimeout, ""} {
			for _, stop := range []string{"age", "consecutive_seen", "max_scrolls", "feed_exhausted", "explicit_empty", "feed_timeout", ""} {
				got := window.After(state, stop)
				want := state == ResultRendered && (stop == "age" || (stop == "consecutive_seen" && allowed))
				require.Equal(t, want, got.AllowSeenStop, "prior=%t state=%s stop=%s", allowed, state, stop)
				require.True(t, window.EarliestTweetAt.Equal(got.EarliestTweetAt))
			}
		}
	}
}

// TestSearchWindowWire excludes silent fallback and ambiguous simultaneous limits.
func TestSearchWindowWire(t *testing.T) {
	window := SearchWindow{EarliestTweetAt: time.Date(2026, 9, 9, 20, 0, 0, 123456000, time.FixedZone("offset", -4*3600))}
	request := SearchRequest{Query: "player team filter:videos", Window: &window}
	require.NoError(t, request.ValidateWindow())
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "max_age_minutes")
	var decoded SearchRequest
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.True(t, window.Equal(*decoded.Window))
	utc := window
	utc.EarliestTweetAt = utc.EarliestTweetAt.UTC()
	require.True(t, utc.Equal(window))
	request.MaxAgeMinutes = 3
	require.Error(t, request.ValidateWindow())
	request.MaxAgeMinutes = 0
	request.Window = &SearchWindow{}
	require.Error(t, request.ValidateWindow())
	request.Window = nil
	request.MaxAgeMinutes = 1440 // Maintenance and old histories remain relative.
	require.NoError(t, request.ValidateWindow())
	raw, err = json.Marshal(request)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "window")
}
