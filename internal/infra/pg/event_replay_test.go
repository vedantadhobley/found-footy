// event_replay_test.go proves operator recovery preparation against real Postgres.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	pginfra "github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// TestEventReplayStore_PrepareRecoverIsBoundedAndIdempotent pins combined recovery.
func TestEventReplayStore_PrepareRecoverIsBoundedAndIdempotent(t *testing.T) {
	ctx, pool, _, fixtures := setupEventRepo(t)
	const fixtureID int64 = 919001
	seedCompletedFixture(t, ctx, fixtures, fixtureID)
	eventID := seedReplayEvent(t, ctx, pool, fixtureID)

	eligibleVision := seedReplayFailure(t, ctx, pool, eventID, fixtureID,
		"https://x.com/a/status/1", "vision_error", "model_admission", "timeout")
	eligibleDownload := seedReplayFailure(t, ctx, pool, eventID, fixtureID,
		"https://x.com/b/status/2", "download_error", "cdn_download", "forbidden")
	eligibleActivity := seedReplayFailureWithTimeout(t, ctx, pool, eventID, fixtureID,
		"https://x.com/d/status/4", "vision_error", "activity", "timeout", "start_to_close")
	ineligible := seedReplayFailure(t, ctx, pool, eventID, fixtureID,
		"https://x.com/c/status/3", "vision_error", "response_parse", "invalid_json")

	workflowID, err := pginfra.StableEventReplayWorkflowID(eventID, "kane-54-20260918")
	if err != nil {
		t.Fatalf("workflow ID: %v", err)
	}
	request := pginfra.EventReplayRequest{
		EventID: eventID, WorkflowID: workflowID, Operation: "kane-54-20260918",
		Mode: pginfra.EventReplayRecover,
	}
	store := pginfra.NewEventReplayStore(pool)
	plan, err := store.PlanEventReplay(ctx, request)
	if err != nil {
		t.Fatalf("PlanEventReplay: %v", err)
	}
	if plan.AlreadyPrepared || plan.Completed || plan.SelectedCandidates != 3 || len(plan.RetryCandidates) != 3 ||
		plan.Input.EventID != eventID || plan.FixtureState != "completed" {
		t.Fatalf("plan = %+v", plan)
	}

	prepared, err := store.PrepareEventReplay(ctx, request)
	if err != nil {
		t.Fatalf("PrepareEventReplay: %v", err)
	}
	if !prepared.AlreadyPrepared || prepared.Completed {
		t.Fatalf("prepared = %+v", prepared)
	}
	assertCandidateOutcome(t, ctx, pool, eligibleVision, "pending")
	assertCandidateOutcome(t, ctx, pool, eligibleDownload, "pending")
	assertCandidateOutcome(t, ctx, pool, eligibleActivity, "pending")
	assertCandidateOutcome(t, ctx, pool, ineligible, "failed")

	var attempts int
	var mode, operation string
	if err := pool.QueryRow(ctx, `
		SELECT (metadata->>'attempts_completed')::int,
		       metadata->>'replay_mode', metadata->>'replay_operation'
		FROM event_downstream_workflows
		WHERE event_id=$1 AND workflow_type='discovery' AND workflow_id=$2
	`, eventID, workflowID).Scan(&attempts, &mode, &operation); err != nil {
		t.Fatalf("read replay metadata: %v", err)
	}
	if attempts != 0 || mode != "recover" || operation != "kane-54-20260918" {
		t.Fatalf("metadata attempts=%d mode=%q operation=%q", attempts, mode, operation)
	}

	// Work that finished after preparation must not be reopened by a command retry.
	if _, err := pool.Exec(ctx, `
		UPDATE event_search_candidates
		SET outcome_class='rejected', reject_reason='not a football broadcast', outcome_at=NOW()
		WHERE id=$1
	`, eligibleVision); err != nil {
		t.Fatalf("finish replay candidate: %v", err)
	}
	retried, err := store.PrepareEventReplay(ctx, request)
	if err != nil {
		t.Fatalf("retry PrepareEventReplay: %v", err)
	}
	if !retried.AlreadyPrepared || retried.SelectedCandidates != 3 || len(retried.RetryCandidates) != 0 {
		t.Fatalf("retried = %+v", retried)
	}
	assertCandidateOutcome(t, ctx, pool, eligibleVision, "rejected")
	if _, err := pool.Exec(ctx, `
		UPDATE event_search_candidates
		SET outcome_class='failed', reject_reason='vision_error', outcome_at=NOW()
		WHERE id = ANY($1::uuid[])
	`, []uuid.UUID{eligibleDownload, eligibleActivity}); err != nil {
		t.Fatalf("finish replay candidates: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE event_downstream_workflows
		SET completed_at=NOW(), outcome_class='no_candidates'
		WHERE event_id=$1 AND workflow_type='discovery' AND workflow_id=$2
	`, eventID, workflowID); err != nil {
		t.Fatalf("finish replay checklist: %v", err)
	}
	result, err := store.ReadEventReplayResult(ctx, eventID, workflowID)
	if err != nil {
		t.Fatalf("ReadEventReplayResult: %v", err)
	}
	if !result.ChecklistCompleted || result.PendingCandidates != 0 || result.ReplayCandidates != 3 {
		t.Fatalf("result = %+v", result)
	}
}

// TestEventReplayStore_FreshSearchDoesNotResetFailures separates the two repair axes.
func TestEventReplayStore_FreshSearchDoesNotResetFailures(t *testing.T) {
	ctx, pool, _, fixtures := setupEventRepo(t)
	const fixtureID int64 = 919004
	seedCompletedFixture(t, ctx, fixtures, fixtureID)
	eventID := seedReplayEvent(t, ctx, pool, fixtureID)
	candidateID := seedReplayFailure(t, ctx, pool, eventID, fixtureID,
		"https://x.com/a/status/5", "vision_error", "model_admission", "timeout")
	workflowID, err := pginfra.StableEventReplayWorkflowID(eventID, "fresh-only-20260918")
	if err != nil {
		t.Fatalf("workflow ID: %v", err)
	}
	request := pginfra.EventReplayRequest{
		EventID: eventID, WorkflowID: workflowID, Operation: "fresh-only-20260918",
		Mode: pginfra.EventReplayFreshSearch,
	}
	store := pginfra.NewEventReplayStore(pool)
	plan, err := store.PlanEventReplay(ctx, request)
	if err != nil {
		t.Fatalf("PlanEventReplay: %v", err)
	}
	if plan.SelectedCandidates != 0 || len(plan.RetryCandidates) != 0 {
		t.Fatalf("fresh plan selected transient candidates: %+v", plan)
	}
	if _, err := store.PrepareEventReplay(ctx, request); err != nil {
		t.Fatalf("PrepareEventReplay: %v", err)
	}
	assertCandidateOutcome(t, ctx, pool, candidateID, "failed")
}

// TestEventReplayStore_RetryOnlyConsumesNoSearchBudget survives config increases.
func TestEventReplayStore_RetryOnlyConsumesNoSearchBudget(t *testing.T) {
	ctx, pool, _, fixtures := setupEventRepo(t)
	const fixtureID int64 = 919002
	seedCompletedFixture(t, ctx, fixtures, fixtureID)
	eventID := seedReplayEvent(t, ctx, pool, fixtureID)
	seedReplayFailure(t, ctx, pool, eventID, fixtureID,
		"https://x.com/a/status/4", "vision_error", "model_request", "transport")
	workflowID, err := pginfra.StableEventReplayWorkflowID(eventID, "transient-only-20260918")
	if err != nil {
		t.Fatalf("workflow ID: %v", err)
	}
	request := pginfra.EventReplayRequest{
		EventID: eventID, WorkflowID: workflowID, Operation: "transient-only-20260918",
		Mode: pginfra.EventReplayRetryTransient,
	}
	if _, err := pginfra.NewEventReplayStore(pool).PrepareEventReplay(ctx, request); err != nil {
		t.Fatalf("PrepareEventReplay: %v", err)
	}
	var attempts int
	if err := pool.QueryRow(ctx, `
		SELECT (metadata->>'attempts_completed')::int
		FROM event_downstream_workflows
		WHERE event_id=$1 AND workflow_type='discovery' AND workflow_id=$2
	`, eventID, workflowID).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 20 {
		t.Fatalf("attempts_completed=%d, want 20", attempts)
	}
}

// TestEventReplayStore_RejectsUnsettledOwnership prevents concurrent discovery.
func TestEventReplayStore_RejectsUnsettledOwnership(t *testing.T) {
	ctx, pool, _, fixtures := setupEventRepo(t)
	const fixtureID int64 = 919003
	seedCompletedFixture(t, ctx, fixtures, fixtureID)
	eventID := seedReplayEvent(t, ctx, pool, fixtureID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_downstream_workflows(event_id,workflow_type,workflow_id)
		VALUES ($1,'discovery','event-replay-other')
	`, eventID); err != nil {
		t.Fatalf("seed conflicting replay: %v", err)
	}
	workflowID, err := pginfra.StableEventReplayWorkflowID(eventID, "blocked-replay-20260918")
	if err != nil {
		t.Fatalf("workflow ID: %v", err)
	}
	_, err = pginfra.NewEventReplayStore(pool).PlanEventReplay(ctx, pginfra.EventReplayRequest{
		EventID: eventID, WorkflowID: workflowID, Operation: "blocked-replay-20260918",
		Mode: pginfra.EventReplayFreshSearch,
	})
	if err == nil {
		t.Fatal("replay accepted another pending discovery owner")
	}
}

// seedReplayEvent creates one completed natural discovery.
func seedReplayEvent(
	t *testing.T,
	ctx context.Context,
	pool *pginfra.Pool,
	fixtureID int64,
) uuid.UUID {
	t.Helper()
	eventID := uuid.New()
	firstSeen := time.Date(2026, 9, 18, 18, 54, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, detail, team_id, team_name,
			player_name, minute, debounce_count, downstream_triggered, first_seen_at
		) VALUES ($1,$2,'157_521_goal_1','goal','normal goal',157,'Bayern München',
		          'Harry Kane',54,3,true,$3)
	`, eventID, fixtureID, firstSeen); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_downstream_workflows(
			event_id,workflow_type,workflow_id,completed_at,outcome_class,metadata
		) VALUES ($1,'discovery',$2,NOW(),'no_candidates',
		          '{"attempts_completed":15}'::jsonb)
	`, eventID, "event-"+eventID.String()); err != nil {
		t.Fatalf("seed original discovery: %v", err)
	}
	return eventID
}

// seedReplayFailure adds one typed terminal candidate.
func seedReplayFailure(
	t *testing.T,
	ctx context.Context,
	pool *pginfra.Pool,
	eventID uuid.UUID,
	fixtureID int64,
	tweetURL, reason, stage, class string,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_search_candidates (
			id,event_id,fixture_id,search_attempt,query,tweet_url,video_page_url,
			outcome_class,reject_reason,outcome_detail,outcome_at
		) VALUES ($1,$2,$3,1,'query',$4,'video','failed',$5,
		          jsonb_build_object('failure',jsonb_build_object('stage',$6::text,'class',$7::text)),NOW())
	`, id, eventID, fixtureID, tweetURL, reason, stage, class); err != nil {
		t.Fatalf("seed failure: %v", err)
	}
	return id
}

// seedReplayFailureWithTimeout adds one typed Temporal activity timeout.
func seedReplayFailureWithTimeout(
	t *testing.T,
	ctx context.Context,
	pool *pginfra.Pool,
	eventID uuid.UUID,
	fixtureID int64,
	tweetURL, reason, stage, class, timeoutType string,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_search_candidates (
			id,event_id,fixture_id,search_attempt,query,tweet_url,video_page_url,
			outcome_class,reject_reason,outcome_detail,outcome_at
		) VALUES ($1,$2,$3,1,'query',$4,'video','failed',$5,
		          jsonb_build_object('failure',jsonb_build_object(
		              'stage',$6::text,'class',$7::text,'timeout_type',$8::text)),NOW())
	`, id, eventID, fixtureID, tweetURL, reason, stage, class, timeoutType); err != nil {
		t.Fatalf("seed timeout failure: %v", err)
	}
	return id
}

// assertCandidateOutcome reads one candidate's durable lifecycle state.
func assertCandidateOutcome(
	t *testing.T,
	ctx context.Context,
	pool *pginfra.Pool,
	id uuid.UUID,
	want string,
) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT outcome_class FROM event_search_candidates WHERE id=$1`, id).Scan(&got); err != nil {
		t.Fatalf("read candidate %s: %v", id, err)
	}
	if got != want {
		t.Fatalf("candidate %s outcome=%q, want %q", id, got, want)
	}
}
