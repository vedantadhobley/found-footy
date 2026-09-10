// Offline, synthetic feed replays compare time boundaries without spending X quota.
package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
)

type windowScenario struct {
	Name     string
	Attempts []windowAttempt
	WantIDs  map[windowPolicy][]int
}

// windowAnchor keeps experiment time independent of the machine running tests.
func windowAnchor() time.Time { return time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC) }

// windowURL creates synthetic full-length IDs that pass the deployed ID guard.
func windowURL(id int) string { return fmt.Sprintf("https://x.com/example/status/%018d", id) }

// windowTweet supplies only fields used by the real scroll loop.
func windowTweet(id int, offset time.Duration) extractedTweet {
	return extractedTweet{TweetURL: windowURL(id), HasVideo: true,
		Datetime: windowAnchor().Add(offset).Format(time.RFC3339Nano)}
}

// windowFeed builds one observed DOM batch; callers can split it to model scrolls.
func windowFeed(at time.Duration, tweets ...extractedTweet) windowAttempt {
	return windowAttempt{State: twittercontract.ResultRendered, MaxScrolls: 10,
		Batches: []windowBatch{{At: windowAnchor().Add(at), Tweets: tweets}}}
}

// windowScenarios deliberately include counterexamples, not a recall benchmark.
// IDs encode synthetic candidates, not validated goal clips or tweet relevance.
func windowScenarios() []windowScenario {
	old := windowTweet(99, -4*time.Minute)
	first := windowFeed(time.Minute, windowTweet(1, 30*time.Second), old)
	recovery := windowFeed(7*time.Minute, windowTweet(4, 390*time.Second))
	recovery.Batches = append(recovery.Batches, windowBatch{
		At: windowAnchor().Add(7 * time.Minute),
		Tweets: []extractedTweet{windowTweet(3, 3*time.Minute), windowTweet(2, 2*time.Minute),
			windowTweet(1, 30*time.Second), old},
	})
	partial := windowFeed(time.Minute, windowTweet(4, 40*time.Second),
		windowTweet(3, 30*time.Second), windowTweet(2, 20*time.Second))
	partial.MaxScrolls = 1
	missingSeen := windowFeed(10*time.Minute, windowTweet(2, 9*time.Minute))
	missingSeen.MaxScrolls = 2
	missingSeen.Batches = append(missingSeen.Batches, windowBatch{
		At: windowAnchor().Add(10 * time.Minute), Tweets: []extractedTweet{windowTweet(3, 8*time.Minute)},
	})
	return []windowScenario{
		{
			Name: "normal_cadence_with_pre_observation_post",
			Attempts: []windowAttempt{
				windowFeed(time.Minute, windowTweet(3, 30*time.Second), windowTweet(2, 10*time.Second),
					windowTweet(1, -10*time.Second), old),
				windowFeed(2*time.Minute, windowTweet(4, 90*time.Second), windowTweet(3, 30*time.Second),
					windowTweet(2, 10*time.Second), windowTweet(1, -10*time.Second), old),
			},
			WantIDs: map[windowPolicy][]int{windowRolling: {3, 2, 1, 4}, windowFixed: {3, 2, 1, 4},
				windowExact: {3, 2, 4}, windowReference: {3, 2, 1, 4}},
		},
		{
			Name: "five_minute_outage",
			Attempts: []windowAttempt{first,
				{State: twittercontract.ResultUpstreamError}, recovery},
			WantIDs: map[windowPolicy][]int{windowRolling: {1, 4}, windowFixed: {1, 4, 3, 2},
				windowExact: {1, 4, 3, 2}, windowReference: {1, 4, 3, 2}},
		},
		{
			Name: "first_attempt_unavailable",
			Attempts: []windowAttempt{{State: twittercontract.ResultUnknownTimeout},
				windowFeed(8*time.Minute, windowTweet(2, 7*time.Minute), windowTweet(1, 30*time.Second), old)},
			WantIDs: map[windowPolicy][]int{windowRolling: {2}, windowFixed: {2, 1},
				windowExact: {2, 1}, windowReference: {2, 1}},
		},
		{
			Name: "delayed_start_with_pre_observation_post",
			Attempts: []windowAttempt{windowFeed(5*time.Minute,
				windowTweet(2, 4*time.Minute), windowTweet(1, -30*time.Second), old)},
			WantIDs: map[windowPolicy][]int{windowRolling: {2}, windowFixed: {2, 1},
				windowExact: {2}, windowReference: {2, 1}},
		},
		{
			Name: "seen_three_after_partial_scan",
			Attempts: []windowAttempt{partial, windowFeed(2*time.Minute,
				windowTweet(4, 40*time.Second), windowTweet(3, 30*time.Second),
				windowTweet(2, 20*time.Second), windowTweet(1, 10*time.Second), old)},
			WantIDs: map[windowPolicy][]int{windowRolling: {4, 3, 2}, windowFixed: {4, 3, 2},
				windowExact: {4, 3, 2}, windowReference: {4, 3, 2, 1}},
		},
		{
			Name:     "known_tweets_absent_still_bounded",
			Attempts: []windowAttempt{first, missingSeen},
			WantIDs: map[windowPolicy][]int{windowRolling: {1, 2, 3}, windowFixed: {1, 2, 3},
				windowExact: {1, 2, 3}, windowReference: {1, 2, 3}},
		},
	}
}

// TestSearchWindowPolicies gives each policy its own history on the same feed.
// JSON restore proves only experimental checkpoint stability, not SQL/Temporal wiring.
func TestSearchWindowPolicies(t *testing.T) {
	for _, scenario := range windowScenarios() {
		t.Run(scenario.Name, func(t *testing.T) {
			for _, policy := range []windowPolicy{windowRolling, windowFixed, windowExact, windowReference} {
				t.Run(string(policy), func(t *testing.T) {
					checkpoint, err := newWindowCheckpoint(policy, windowAnchor())
					require.NoError(t, err)
					cutoff := checkpoint.EarliestTweetAt
					for i, attempt := range scenario.Attempts {
						result, err := runWindowAttempt(context.Background(), &checkpoint, attempt)
						require.NoError(t, err)
						t.Logf("probe=%d candidates=%d stop=%s extracts=%d scrolls=%d parsed=%d",
							i+1, len(result.URLs), result.Stop, result.Extractions, result.Scrolls, result.Parsed)
						encoded, err := json.Marshal(checkpoint)
						require.NoError(t, err)
						checkpoint = windowCheckpoint{}
						require.NoError(t, json.Unmarshal(encoded, &checkpoint))
						require.True(t, cutoff.Equal(checkpoint.EarliestTweetAt), "restoration moved cutoff")
					}
					var want []string
					for _, id := range scenario.WantIDs[policy] {
						want = append(want, windowURL(id))
					}
					require.Equal(t, want, checkpoint.SeenURLs)
				})
			}
		})
	}
}

// TestSearchWindowBoundaries pins strict-before semantics and unknown timestamps.
func TestSearchWindowBoundaries(t *testing.T) {
	for _, policy := range []windowPolicy{windowRolling, windowFixed, windowExact, windowReference} {
		t.Run(string(policy), func(t *testing.T) {
			checkpoint, err := newWindowCheckpoint(policy, windowAnchor())
			require.NoError(t, err)
			for _, tc := range []struct {
				Name string
				Time string
				Stop bool
			}{
				{"at_boundary", checkpoint.EarliestTweetAt.Format(time.RFC3339Nano), false},
				{"before_boundary", checkpoint.EarliestTweetAt.Add(-time.Nanosecond).Format(time.RFC3339Nano), true},
				{"after_boundary", checkpoint.EarliestTweetAt.Add(time.Nanosecond).Format(time.RFC3339Nano), false},
				{"future", windowAnchor().Add(time.Hour).Format(time.RFC3339Nano), false},
				{"missing", "", false}, {"malformed", "not-a-time", false},
			} {
				t.Run(tc.Name, func(t *testing.T) {
					tweet := extractedTweet{Datetime: tc.Time}
					require.Equal(t, tc.Stop, windowStopsAtTime(&checkpoint, tweet, windowAnchor()))
					tweet.IsPromoted = true
					require.False(t, windowStopsAtTime(&checkpoint, tweet, windowAnchor()))
				})
			}
		})
	}
	_, err := newWindowCheckpoint(windowFixed, time.Time{})
	require.Error(t, err)
	_, err = newWindowCheckpoint("invalid", windowAnchor())
	require.Error(t, err)
}

// TestSearchWindowBoundsAndUnavailable avoids treating absent evidence as coverage.
func TestSearchWindowBoundsAndUnavailable(t *testing.T) {
	checkpoint, err := newWindowCheckpoint(windowFixed, windowAnchor())
	require.NoError(t, err)
	before := checkpoint
	for _, state := range []twittercontract.ResultState{twittercontract.ResultLogin,
		twittercontract.ResultUpstreamError, twittercontract.ResultUnknownTimeout, twittercontract.ResultExplicitEmpty} {
		result, err := runWindowAttempt(context.Background(), &checkpoint, windowAttempt{State: state})
		require.NoError(t, err)
		require.Zero(t, result.Extractions)
		require.Equal(t, before, checkpoint)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runWindowAttempt(ctx, &checkpoint, windowFeed(time.Minute))
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, before, checkpoint)

	result, err := runWindowAttempt(context.Background(), &checkpoint, windowFeed(time.Minute, windowTweet(1, 0)))
	require.NoError(t, err)
	require.Equal(t, windowCaptureEnd, result.Stop, "incomplete capture is not exhausted feed")
	require.Equal(t, 1, result.Scrolls)
}
