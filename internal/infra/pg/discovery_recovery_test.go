// discovery_recovery_test.go — integration coverage for EventWorkflow's
// durable search checkpoint and pending-candidate recovery queries.
package pg_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
)

func TestDiscoveryActivities_RecoveryStateRoundTrip(t *testing.T) {
	ctx, pool, fixtureRepo := setupRepo(t)
	fixture := makeStaging(8120, time.Date(2026, 8, 17, 19, 0, 0, 0, time.UTC))
	if err := fixture.Activate(time.Date(2026, 8, 17, 18, 55, 0, 0, time.UTC)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := fixtureRepo.Insert(ctx, fixture); err != nil {
		t.Fatalf("insert fixture: %v", err)
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
	evidence := make([]discoverycontract.CandidateEvidence, 0, len(urls))
	for _, url := range urls {
		candidate := discoverycontract.CandidateEvidence{
			EventID: eventID, FixtureID: fixture.ID, SearchAttempt: 1,
			Query: "query", TweetURL: url, TweetText: "goal clip",
			VideoPageURL: "video", Username: "reporter",
		}
		evidence = append(evidence, candidate)
		out, err := activities.StoreCandidate(ctx, candidate)
		if err != nil || !out.Inserted {
			t.Fatalf("StoreCandidate(%s) = %+v, %v", url, out, err)
		}
	}
	if err := activities.UpsertCandidateOutcome(ctx, discoveryactivity.UpsertCandidateOutcomeInput{
		Evidence: evidence[1], Outcome: discoveryactivity.OutcomeRejected,
		RejectReason: "test",
	}); err != nil {
		t.Fatalf("UpsertCandidateOutcome: %v", err)
	}
	searchEvidence := twittercontract.SearchEvidence{
		FinalURL: "https://x.com/search", TimelineSeen: true, TimelineStatus: 429,
		RateLimitRemain: "0",
	}
	if err := activities.RecordDiscoveryProgress(ctx, discoveryactivity.RecordDiscoveryProgressInput{
		EventID: eventID, WorkflowType: "discovery", WorkflowID: workflowID, Attempt: 7,
		UnavailableAttempts: 4, LastSearchState: twittercontract.ResultUpstreamError,
		LastSearchEvidence: &searchEvidence,
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
	if state.UnavailableAttempts != 4 ||
		state.LastSearchState != twittercontract.ResultUpstreamError ||
		state.LastSearchEvidence != searchEvidence {
		t.Errorf("search recovery = unavailable %d/state %q/evidence %+v",
			state.UnavailableAttempts, state.LastSearchState, state.LastSearchEvidence)
	}
	if len(state.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(state.Candidates))
	}
	if state.Candidates[0].Evidence != evidence[0] ||
		state.Candidates[0].State != ddiscovery.CandidateObserved ||
		!state.Candidates[0].Pending {
		t.Errorf("candidate[0] = %+v, want observed %+v", state.Candidates[0], evidence[0])
	}
	if state.Candidates[1].Evidence != evidence[1] ||
		state.Candidates[1].State != ddiscovery.CandidateTerminal ||
		state.Candidates[1].Pending {
		t.Errorf("candidate[1] = %+v, want terminal %+v", state.Candidates[1], evidence[1])
	}
}

// TestDiscoveryActivities_TerminalUpsertCreatesMissingEvidence covers the
// StoreCandidate failure window. The terminal write must create the complete
// audit row by itself and remain one row when Temporal retries it.
func TestDiscoveryActivities_TerminalUpsertCreatesMissingEvidence(t *testing.T) {
	ctx, pool, fixtureRepo := setupRepo(t)
	fixture := makeStaging(8121, time.Date(2026, 8, 17, 20, 0, 0, 0, time.UTC))
	if err := fixture.Activate(time.Date(2026, 8, 17, 19, 55, 0, 0, time.UTC)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := fixtureRepo.Insert(ctx, fixture); err != nil {
		t.Fatalf("insert fixture: %v", err)
	}

	eventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, player_name, minute, downstream_triggered
		) VALUES ($1, $2, '41_8_goal_1', 'goal', 'normal goal', 41, 'Team', 'Player', 88, true)
	`, eventID, fixture.ID); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	evidence := discoverycontract.CandidateEvidence{
		EventID: eventID, FixtureID: fixture.ID, SearchAttempt: 6,
		Query:     "(player OR Team) filter:videos",
		TweetURL:  "https://x.com/reporter/status/3333333333333333333",
		TweetText: "late winner", VideoPageURL: "https://x.com/i/status/3333333333333333333",
		DurationSeconds: 14.25, Username: "reporter", AgeMinutesAtDiscovery: 0.75,
	}
	input := discoveryactivity.UpsertCandidateOutcomeInput{
		Evidence: evidence, Outcome: discoveryactivity.OutcomePromoted,
	}
	activities := &discoveryactivity.Activities{Pool: pool}
	if err := activities.UpsertCandidateOutcome(ctx, input); err != nil {
		t.Fatalf("first UpsertCandidateOutcome: %v", err)
	}
	if err := activities.UpsertCandidateOutcome(ctx, input); err != nil {
		t.Fatalf("retry UpsertCandidateOutcome: %v", err)
	}

	var (
		count                   int
		fixtureID               int64
		attempt                 int
		query, text, page, user string
		duration, age           float64
		outcome                 string
		outcomeAt               time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)::int, MIN(fixture_id), MIN(search_attempt), MIN(query),
		       MIN(tweet_text), MIN(video_page_url), MIN(duration_seconds),
		       MIN(username), MIN(age_minutes_at_discovery), MIN(outcome_class),
		       MIN(outcome_at)
		FROM event_search_candidates
		WHERE event_id = $1 AND tweet_url = $2
	`, eventID, evidence.TweetURL).Scan(
		&count, &fixtureID, &attempt, &query, &text, &page, &duration,
		&user, &age, &outcome, &outcomeAt,
	); err != nil {
		t.Fatalf("read terminal candidate: %v", err)
	}
	if count != 1 || fixtureID != evidence.FixtureID || attempt != evidence.SearchAttempt ||
		query != evidence.Query || text != evidence.TweetText || page != evidence.VideoPageURL ||
		duration != evidence.DurationSeconds || user != evidence.Username ||
		age != evidence.AgeMinutesAtDiscovery || outcome != string(discoveryactivity.OutcomePromoted) ||
		outcomeAt.IsZero() {
		t.Fatalf("terminal candidate mismatch: count=%d fixture=%d attempt=%d query=%q text=%q page=%q duration=%v user=%q age=%v outcome=%q at=%v",
			count, fixtureID, attempt, query, text, page, duration, user, age, outcome, outcomeAt)
	}
}
