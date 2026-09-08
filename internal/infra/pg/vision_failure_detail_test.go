// Real-Postgres regression coverage for retry-safe vision failure evidence persistence.
package pg_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
)

// TestVisionFailureDetailRoundTrip covers an observed row and terminal-first insertion, both retried.
func TestVisionFailureDetailRoundTrip(t *testing.T) {
	ctx, pool, fixtureRepo := setupRepo(t)
	f := makeStaging(8129, time.Date(2026, 9, 4, 19, 0, 0, 0, time.UTC))
	require.NoError(t, fixtureRepo.Insert(ctx, f))
	eventID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO events (
		id,fixture_id,natural_key,event_type,detail,team_id,team_name,player_name,minute
	) VALUES ($1,$2,'40_7_goal_1','goal','normal goal',40,'Team','Player',30)`, eventID, f.ID)
	require.NoError(t, err)
	acts := &discoveryactivity.Activities{Pool: pool}
	for _, tt := range []struct {
		name     string
		observed bool
		failure  visionactivity.FailureDetail
	}{
		{"request", true, visionactivity.FailureDetail{Stage: visionactivity.FailureRequest, Class: visionactivity.FailureTimeout}},
		{"heartbeat", false, visionactivity.FailureDetail{Stage: visionactivity.FailureActivity, Class: visionactivity.FailureTimeout, TimeoutType: visionactivity.TimeoutHeartbeat}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evidence := discoverycontract.CandidateEvidence{EventID: eventID, FixtureID: f.ID, SearchAttempt: 1,
				Query: "query", TweetURL: "https://x.com/reporter/status/" + tt.name, VideoPageURL: "video", Username: "reporter"}
			if tt.observed {
				_, err := acts.StoreCandidate(ctx, evidence)
				require.NoError(t, err)
			}
			detail, err := json.Marshal(map[string]any{"failure": tt.failure})
			require.NoError(t, err)
			in := discoveryactivity.UpsertCandidateOutcomeInput{Evidence: evidence, Outcome: discoveryactivity.OutcomeFailed, RejectReason: "vision_error", Detail: detail}
			require.NoError(t, acts.UpsertCandidateOutcome(ctx, in))
			require.NoError(t, acts.UpsertCandidateOutcome(ctx, in))
			var outcome, reason string
			var stored []byte
			var stamped bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT outcome_class,reject_reason,outcome_detail,outcome_at IS NOT NULL
				FROM event_search_candidates WHERE event_id=$1 AND tweet_url=$2`, eventID, evidence.TweetURL).
				Scan(&outcome, &reason, &stored, &stamped))
			require.Equal(t, "failed", outcome)
			require.Equal(t, "vision_error", reason)
			require.True(t, stamped)
			require.JSONEq(t, string(detail), string(stored))
		})
	}
}
