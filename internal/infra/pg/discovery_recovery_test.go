// discovery_recovery_test.go — integration coverage for EventWorkflow's
// durable search checkpoint and pending-candidate recovery queries.
package pg_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
)

func TestDiscoveryActivities_RecoveryStateRoundTrip(t *testing.T) {
	ctx, pool, fixtureRepo := setupRepo(t)
	fixture := makeStaging(8120, time.Date(2026, 8, 17, 19, 0, 0, 0, time.UTC))
	if err := fixture.Activate(time.Date(2026, 8, 17, 18, 55, 0, 0, time.UTC)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := fixtureRepo.Upsert(ctx, fixture); err != nil {
		t.Fatalf("upsert fixture: %v", err)
	}

	eventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, player_name, minute, downstream_triggered
		) VALUES ($1, $2, '40_7_goal_1', 'goal', 'normal goal', 40, 'Team', 'Player', 77, true)
	`, eventID, fixture.ID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	const workflowID = "event-recovery-test"
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_downstream_workflows (event_id, workflow_type, workflow_id)
		VALUES ($1, 'discovery', $2)
	`, eventID, workflowID); err != nil {
		t.Fatalf("seed checklist: %v", err)
	}

	activities := &discoveryactivity.Activities{Pool: pool}
	urls := []string{"https://x.com/u/status/1", "https://x.com/u/status/2"}
	for _, url := range urls {
		out, err := activities.StoreCandidate(ctx, discoveryactivity.StoreCandidateInput{
			EventID: eventID, FixtureID: fixture.ID, SearchAttempt: 1,
			Query: "query", TweetURL: url, VideoPageURL: "video",
		})
		if err != nil || !out.Inserted {
			t.Fatalf("StoreCandidate(%s) = %+v, %v", url, out, err)
		}
	}
	if err := activities.RecordCandidateOutcome(ctx, discoveryactivity.RecordCandidateOutcomeInput{
		EventID: eventID, TweetURL: urls[1], Outcome: discoveryactivity.OutcomeRejected,
		RejectReason: "test",
	}); err != nil {
		t.Fatalf("RecordCandidateOutcome: %v", err)
	}
	if err := activities.RecordDiscoveryProgress(ctx, discoveryactivity.RecordDiscoveryProgressInput{
		EventID: eventID, WorkflowType: "discovery", WorkflowID: workflowID, Attempt: 7,
	}); err != nil {
		t.Fatalf("RecordDiscoveryProgress(7): %v", err)
	}
	// Monotonic: a replayed older checkpoint cannot move recovery backward.
	if err := activities.RecordDiscoveryProgress(ctx, discoveryactivity.RecordDiscoveryProgressInput{
		EventID: eventID, WorkflowType: "discovery", WorkflowID: workflowID, Attempt: 3,
	}); err != nil {
		t.Fatalf("RecordDiscoveryProgress(3): %v", err)
	}

	state, err := activities.LoadEventRecoveryState(ctx, discoveryactivity.LoadEventRecoveryStateInput{
		EventID: eventID, WorkflowType: "discovery", WorkflowID: workflowID,
	})
	if err != nil {
		t.Fatalf("LoadEventRecoveryState: %v", err)
	}
	if state.AttemptsCompleted != 7 {
		t.Errorf("attempts completed = %d, want 7", state.AttemptsCompleted)
	}
	if len(state.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(state.Candidates))
	}
	if state.Candidates[0].TweetURL != urls[0] || !state.Candidates[0].Pending {
		t.Errorf("candidate[0] = %+v, want pending %s", state.Candidates[0], urls[0])
	}
	if state.Candidates[1].TweetURL != urls[1] || state.Candidates[1].Pending {
		t.Errorf("candidate[1] = %+v, want terminal %s", state.Candidates[1], urls[1])
	}
}
