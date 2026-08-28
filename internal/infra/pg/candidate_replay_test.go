// candidate_replay_test.go covers the transactional, idempotent repair path
// that re-drives exact historical candidate verdicts through EventWorkflow.
package pg_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	pginfra "github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// TestCandidateReplayStore_PrepareIsAuditableAndIdempotent proves that a retry
// cannot reset completed replay work and that the prior verdict survives.
func TestCandidateReplayStore_PrepareIsAuditableAndIdempotent(t *testing.T) {
	ctx, pool, fixtureRepo := setupRepo(t)
	fixture := makeStaging(8130, time.Date(2026, 8, 19, 18, 0, 0, 0, time.UTC))
	if err := fixture.Activate(time.Date(2026, 8, 19, 17, 55, 0, 0, time.UTC)); err != nil {
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
		) VALUES ($1, $2, '42_9_penalty_1', 'missed penalty', 'missed penalty',
		          42, 'Team', 'Player', 90, true)
	`, eventID, fixture.ID); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	evidence := []discoverycontract.CandidateEvidence{
		{
			EventID: eventID, FixtureID: fixture.ID, SearchAttempt: 1, Query: "query",
			TweetURL: "https://x.com/a/status/1", TweetText: "clip one",
			VideoPageURL: "video-one", DurationSeconds: 12, Username: "a",
		},
		{
			EventID: eventID, FixtureID: fixture.ID, SearchAttempt: 2, Query: "query",
			TweetURL: "https://x.com/b/status/2", TweetText: "clip two",
			VideoPageURL: "video-two", DurationSeconds: 13, Username: "b",
		},
	}
	for _, candidate := range evidence {
		if _, err := pool.Exec(ctx, `
			INSERT INTO event_search_candidates (
				event_id, fixture_id, search_attempt, query, tweet_url,
				tweet_text, video_page_url, duration_seconds, username,
				outcome_class, reject_reason, outcome_detail, outcome_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
			          'rejected', $10, '{"detected_minute":45}'::jsonb, NOW())
		`, candidate.EventID, candidate.FixtureID, candidate.SearchAttempt,
			candidate.Query, candidate.TweetURL, candidate.TweetText,
			candidate.VideoPageURL, candidate.DurationSeconds, candidate.Username,
			pginfra.ClockMismatchRejectReason); err != nil {
			t.Fatalf("seed candidate: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_search_candidates (
			event_id, fixture_id, search_attempt, query, tweet_url, video_page_url,
			outcome_class, reject_reason, outcome_at
		) VALUES ($1, $2, 3, 'query', 'https://x.com/c/status/3', 'video-three',
		          'rejected', 'not a football broadcast', NOW())
	`, eventID, fixture.ID); err != nil {
		t.Fatalf("seed non-target candidate: %v", err)
	}

	store := pginfra.NewCandidateReplayStore(pool)
	events, err := store.ListCandidateReplayEvents(
		ctx, fixture.ID, pginfra.ClockMismatchRejectReason, "event-replay-ff057-boundary-",
	)
	if err != nil {
		t.Fatalf("ListCandidateReplayEvents: %v", err)
	}
	if len(events) != 1 || events[0].EligibleCandidates != 2 || events[0].Input.EventID != eventID {
		t.Fatalf("events = %+v, want one event with two candidates", events)
	}

	workflowID := "event-replay-ff057-boundary-" + eventID.String()
	prepareInput := pginfra.PrepareCandidateReplayInput{
		EventID: eventID, WorkflowID: workflowID,
		ReplayKind:   pginfra.ClockBoundaryReplayKind,
		RejectReason: pginfra.ClockMismatchRejectReason,
		MaxAttempts:  15,
	}
	prepared, err := store.PrepareCandidateReplay(ctx, prepareInput)
	if err != nil {
		t.Fatalf("PrepareCandidateReplay: %v", err)
	}
	if prepared.SelectedCandidates != 2 || prepared.AlreadyPrepared || prepared.Completed {
		t.Fatalf("prepared = %+v", prepared)
	}
	replanned, err := store.ListCandidateReplayEvents(
		ctx, fixture.ID, pginfra.ClockMismatchRejectReason, "event-replay-ff057-boundary-",
	)
	if err != nil {
		t.Fatalf("replan ListCandidateReplayEvents: %v", err)
	}
	if len(replanned) != 1 || replanned[0].EligibleCandidates != 2 ||
		!replanned[0].AlreadyPrepared || replanned[0].Completed {
		t.Fatalf("replanned = %+v", replanned)
	}

	activities := &discoveryactivity.Activities{Pool: pool}
	if err := activities.UpsertCandidateOutcome(ctx, discoveryactivity.UpsertCandidateOutcomeInput{
		Evidence: evidence[0], Outcome: discoveryactivity.OutcomePromoted,
		Detail: json.RawMessage(`{"asset_id":"asset-1","verified":true}`),
	}); err != nil {
		t.Fatalf("UpsertCandidateOutcome: %v", err)
	}

	// Retrying preparation must not reset a candidate already finished by the
	// replay execution.
	retried, err := store.PrepareCandidateReplay(ctx, prepareInput)
	if err != nil {
		t.Fatalf("retry PrepareCandidateReplay: %v", err)
	}
	if !retried.AlreadyPrepared || retried.SelectedCandidates != 2 || retried.Completed {
		t.Fatalf("retried = %+v", retried)
	}

	var (
		outcome string
		detail  []byte
	)
	if err := pool.QueryRow(ctx, `
		SELECT outcome_class, outcome_detail
		FROM event_search_candidates
		WHERE event_id = $1 AND tweet_url = $2
	`, eventID, evidence[0].TweetURL).Scan(&outcome, &detail); err != nil {
		t.Fatalf("read replayed candidate: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(detail, &decoded); err != nil {
		t.Fatalf("decode outcome detail: %v", err)
	}
	replay, ok := decoded["replay"].(map[string]any)
	if outcome != string(discoveryactivity.OutcomePromoted) || !ok ||
		replay["run_id"] != workflowID || decoded["asset_id"] != "asset-1" {
		t.Fatalf("outcome=%q detail=%s", outcome, detail)
	}
	previous, ok := replay["previous_outcome_detail"].(map[string]any)
	if !ok || previous["detected_minute"] != float64(45) {
		t.Fatalf("previous outcome evidence missing: %s", detail)
	}

	if err := activities.UpsertCandidateOutcome(ctx, discoveryactivity.UpsertCandidateOutcomeInput{
		Evidence: evidence[1], Outcome: discoveryactivity.OutcomeDuplicate,
	}); err != nil {
		t.Fatalf("finish second candidate: %v", err)
	}
	var (
		detailType string
		replayRun  string
	)
	if err := pool.QueryRow(ctx, `
		SELECT jsonb_typeof(outcome_detail), outcome_detail#>>'{replay,run_id}'
		FROM event_search_candidates
		WHERE event_id = $1 AND tweet_url = $2
	`, eventID, evidence[1].TweetURL).Scan(&detailType, &replayRun); err != nil {
		t.Fatalf("read nil-detail replay outcome: %v", err)
	}
	if detailType != "object" || replayRun != workflowID {
		t.Fatalf("nil-detail replay outcome type=%q run=%q", detailType, replayRun)
	}

	// Simulate the malformed shape produced before JSON null was distinguished
	// from SQL NULL. Retrying the same repair identity normalizes only its own
	// two-element [null, replay-object] envelope.
	if _, err := pool.Exec(ctx, `
		UPDATE event_search_candidates
		SET outcome_detail = jsonb_build_array('null'::jsonb, outcome_detail)
		WHERE event_id = $1 AND tweet_url = $2
	`, eventID, evidence[1].TweetURL); err != nil {
		t.Fatalf("seed legacy replay detail: %v", err)
	}
	normalized, err := store.PrepareCandidateReplay(ctx, prepareInput)
	if err != nil {
		t.Fatalf("normalize PrepareCandidateReplay: %v", err)
	}
	if !normalized.AlreadyPrepared || normalized.NormalizedCandidates != 1 {
		t.Fatalf("normalized = %+v", normalized)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE event_downstream_workflows
		SET completed_at = NOW(), outcome_class = 'assets_surfaced'
		WHERE event_id = $1 AND workflow_type = 'discovery' AND workflow_id = $2
	`, eventID, workflowID); err != nil {
		t.Fatalf("complete checklist: %v", err)
	}

	result, err := store.ReadCandidateReplayResult(ctx, eventID, workflowID)
	if err != nil {
		t.Fatalf("ReadCandidateReplayResult: %v", err)
	}
	if !result.ChecklistCompleted || result.OutcomeClass != "assets_surfaced" ||
		result.ReplayedCandidates != 2 || result.PendingCandidates != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// TestCandidateReplayStore_PrepareRejectsIdentityDrift prevents one workflow
// identity from being silently reused for a different repair predicate.
func TestCandidateReplayStore_PrepareRejectsIdentityDrift(t *testing.T) {
	ctx, pool, fixtureRepo := setupRepo(t)
	fixture := makeStaging(8131, time.Date(2026, 8, 19, 19, 0, 0, 0, time.UTC))
	if err := fixture.Activate(time.Date(2026, 8, 19, 18, 55, 0, 0, time.UTC)); err != nil {
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
		) VALUES ($1, $2, '43_10_goal_1', 'goal', 'normal goal',
		          43, 'Team', 'Player', 45, true)
	`, eventID, fixture.ID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_downstream_workflows (event_id, workflow_type, workflow_id, metadata)
		VALUES ($1, 'discovery', 'event-replay-conflict',
		        '{"attempts_completed":14,"replay_kind":"another-repair","replay_selector":{"reject_reason":"other"},"selected_candidates":1}')
	`, eventID); err != nil {
		t.Fatalf("seed conflicting checklist: %v", err)
	}

	store := pginfra.NewCandidateReplayStore(pool)
	_, err := store.PrepareCandidateReplay(context.Background(), pginfra.PrepareCandidateReplayInput{
		EventID: eventID, WorkflowID: "event-replay-conflict",
		ReplayKind:   pginfra.ClockBoundaryReplayKind,
		RejectReason: pginfra.ClockMismatchRejectReason,
		MaxAttempts:  15,
	})
	if err == nil {
		t.Fatal("expected identity drift error")
	}
}
