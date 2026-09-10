// Durable fixed-window initialization reuses the existing downstream metadata.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
)

// loadSearchWindow initializes only an absent window, atomically with respect
// to concurrent retries. A later config change cannot move the chosen floor.
// The stored event timestamp also covers old workflow inputs without FirstSeenAt.
func (a *Activities) loadSearchWindow(ctx context.Context, in LoadEventRecoveryStateInput) (twittercontract.SearchWindow, error) {
	var window twittercontract.SearchWindow
	if in.SearchLookbackMinutes < 0 {
		return window, fmt.Errorf("discovery.LoadEventRecoveryState: negative search lookback")
	}
	_, err := a.Pool.Exec(ctx, `
		UPDATE event_downstream_workflows d
		SET metadata = jsonb_set(COALESCE(d.metadata, '{}'::jsonb), '{search_window}',
		    jsonb_build_object('earliest_tweet_at', e.first_seen_at - make_interval(mins => $4),
		                      'allow_seen_stop', false), true)
		FROM events e
		WHERE d.event_id = $1 AND d.workflow_type = $2 AND d.workflow_id = $3
		  AND e.id = d.event_id
		  AND NOT (COALESCE(d.metadata, '{}'::jsonb) ? 'search_window')
	`, in.EventID, in.WorkflowType, in.WorkflowID, in.SearchLookbackMinutes)
	if err != nil {
		return window, fmt.Errorf("discovery.LoadEventRecoveryState: initialize search window: %w", err)
	}
	var raw []byte
	if err := a.Pool.QueryRow(ctx, `
		SELECT metadata->'search_window' FROM event_downstream_workflows
		WHERE event_id = $1 AND workflow_type = $2 AND workflow_id = $3
	`, in.EventID, in.WorkflowType, in.WorkflowID).Scan(&raw); err != nil {
		return window, fmt.Errorf("discovery.LoadEventRecoveryState: read search window: %w", err)
	}
	if err := json.Unmarshal(raw, &window); err != nil {
		return window, fmt.Errorf("discovery.LoadEventRecoveryState: decode search window: %w", err)
	}
	if err := window.Validate(); err != nil {
		return window, fmt.Errorf("discovery.LoadEventRecoveryState: %w", err)
	}
	window.EarliestTweetAt = window.EarliestTweetAt.UTC()
	// A failed run may have stored candidates from a later, uncheckpointed
	// partial scan. Keep the timestamp, but do not trust its last completed
	// scan's permission in a replacement run. Replay retains this activity result.
	window.AllowSeenStop = false
	return window, nil
}
