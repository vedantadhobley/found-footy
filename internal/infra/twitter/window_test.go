// HTTP contract tests prevent old browsers from silently applying relative bounds.
package twitter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/config"
	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
)

// TestSearchRequiresAppliedWindow checks serialization, old-service mismatch,
// fixed-floor/permission mismatch, explicit empty, and preserved 429 evidence.
func TestSearchRequiresAppliedWindow(t *testing.T) {
	window := twittercontract.SearchWindow{EarliestTweetAt: time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)}
	wrongFloor, wrongPermission := window, window
	wrongFloor.EarliestTweetAt = wrongFloor.EarliestTweetAt.Add(time.Minute)
	wrongPermission.AllowSeenStop = true
	for _, tc := range []struct {
		name      string
		state     twittercontract.ResultState
		echo      *twittercontract.SearchWindow
		wantError bool
	}{
		{"applied", twittercontract.ResultRendered, &window, false},
		{"explicit_empty", twittercontract.ResultExplicitEmpty, &window, false},
		{"old_rendered", twittercontract.ResultRendered, nil, true},
		{"old_untyped", "", nil, true},
		{"wrong_floor", twittercontract.ResultRendered, &wrongFloor, true},
		{"wrong_permission", twittercontract.ResultRendered, &wrongPermission, true},
		{"old_unavailable_preserves_evidence", twittercontract.ResultUpstreamError, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req twitter.SearchRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				require.NotNil(t, req.Window)
				require.True(t, window.Equal(*req.Window))
				require.Zero(t, req.MaxAgeMinutes)
				require.Equal(t, "player team filter:videos", req.Query)
				require.Equal(t, []string{"https://x.com/u/status/111111111111111111"}, req.ExcludeURLs)
				_ = json.NewEncoder(w).Encode(twitter.SearchResponse{ResultState: tc.state,
					StopReason: "age", Window: tc.echo, Evidence: twittercontract.SearchEvidence{TimelineStatus: 429}})
			}))
			defer srv.Close()
			ins, _ := newFixture()
			client, err := twitter.NewClient(config.TwitterConfig{BaseURL: srv.URL, SearchTimeout: time.Second}, ins)
			require.NoError(t, err)
			out, err := client.Search(context.Background(), "", twitter.SearchRequest{
				Query: "player team filter:videos", Window: &window,
				ExcludeURLs: []string{"https://x.com/u/status/111111111111111111"},
			})
			if tc.wantError {
				require.ErrorContains(t, err, "did not apply")
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.state, out.ResultState)
				require.Equal(t, 429, out.Evidence.TimelineStatus)
			}
		})
	}
}
