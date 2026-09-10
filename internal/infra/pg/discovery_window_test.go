// Real-Postgres checks for immutable search floors and monotonic scan evidence.
package pg_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
)

// TestDiscoverySearchWindowPersistence covers initialization before any usable
// search, concurrent retries, stale checkpoints, and replacement-run safety.
func TestDiscoverySearchWindowPersistence(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	firstSeen := time.Date(2026, 9, 9, 20, 0, 0, 123456000, time.UTC)
	fixture := makeStaging(8191, firstSeen.Add(-time.Hour))
	require.NoError(t, fixtures.Insert(ctx, fixture))
	eventID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO events
		(id, fixture_id, natural_key, event_type, detail, team_id, team_name, player_name, minute, first_seen_at)
		VALUES ($1, $2, '40_7_goal_1', 'goal', 'normal goal', 40, 'Team', 'Player', 50, $3)`,
		eventID, fixture.ID, firstSeen)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO event_downstream_workflows
		(event_id, workflow_type, workflow_id, metadata) VALUES ($1, 'discovery', 'window-test', '{"unrelated":"retain"}')`, eventID)
	require.NoError(t, err)
	activities := &discoveryactivity.Activities{Pool: pool}
	in := discoveryactivity.LoadEventRecoveryStateInput{EventID: eventID, WorkflowType: "discovery", WorkflowID: "window-test"}
	legacy, err := activities.LoadEventRecoveryState(ctx, in)
	require.NoError(t, err)
	require.Nil(t, legacy.Window)
	in.SearchLookbackMinutes = 3

	var wg sync.WaitGroup
	results := make(chan discoveryactivity.LoadEventRecoveryStateOutput, 4)
	errors := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := activities.LoadEventRecoveryState(ctx, in)
			results <- out
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	floor := firstSeen.Add(-3 * time.Minute)
	for out := range results {
		require.NotNil(t, out.Window)
		require.True(t, floor.Equal(out.Window.EarliestTweetAt))
		require.False(t, out.Window.AllowSeenStop)
	}
	// Initialization is durable before the first attempt, including 429 at slot 1.
	window := twittercontract.SearchWindow{EarliestTweetAt: floor, AllowSeenStop: true}
	progress := discoveryactivity.RecordDiscoveryProgressInput{
		EventID: eventID, WorkflowType: "discovery", WorkflowID: in.WorkflowID,
		Attempt: 1, Window: &window, LastSearchState: twittercontract.ResultRendered,
	}
	require.NoError(t, activities.RecordDiscoveryProgress(ctx, progress))
	require.NoError(t, activities.RecordDiscoveryProgress(ctx, progress)) // Same retry is harmless.
	window.AllowSeenStop = false
	progress.UnavailableAttempts = 1
	progress.LastSearchState = twittercontract.ResultUpstreamError
	require.NoError(t, activities.RecordDiscoveryProgress(ctx, progress))
	window.AllowSeenStop = true
	progress.UnavailableAttempts = 0 // Stale checkpoint cannot re-enable the shortcut.
	require.NoError(t, activities.RecordDiscoveryProgress(ctx, progress))
	var raw []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT metadata FROM event_downstream_workflows WHERE event_id=$1`, eventID).Scan(&raw))
	var stored struct {
		Window    twittercontract.SearchWindow `json:"search_window"`
		Unrelated string                       `json:"unrelated"`
	}
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.False(t, stored.Window.AllowSeenStop)
	require.Equal(t, "retain", stored.Unrelated)

	// A newer completed scan may enable it; a replacement execution still must
	// reach the floor again because later uncheckpointed candidates may exist.
	progress.Attempt, progress.UnavailableAttempts = 2, 1
	require.NoError(t, activities.RecordDiscoveryProgress(ctx, progress))
	in.SearchLookbackMinutes = 9 // Config change must not move the original boundary.
	recovered, err := activities.LoadEventRecoveryState(ctx, in)
	require.NoError(t, err)
	require.True(t, floor.Equal(recovered.Window.EarliestTweetAt))
	require.False(t, recovered.Window.AllowSeenStop)
	require.Equal(t, 2, recovered.AttemptsCompleted)
	require.Equal(t, 1, recovered.UnavailableAttempts)

	window.EarliestTweetAt = floor.Add(-time.Minute)
	require.ErrorContains(t, activities.RecordDiscoveryProgress(ctx, progress), "boundary changed")
	in.WorkflowID = "missing"
	_, err = activities.LoadEventRecoveryState(ctx, in)
	require.Error(t, err)
	in.WorkflowID = "window-test"
	_, err = pool.Exec(ctx, `UPDATE event_downstream_workflows SET metadata=jsonb_set(metadata, '{search_window}', 'null') WHERE event_id=$1`, eventID)
	require.NoError(t, err)
	_, err = activities.LoadEventRecoveryState(ctx, in)
	require.Error(t, err, "malformed existing state must not silently reinitialize its timestamp")
}
