// FixtureRepo completion-contract integration tests.
package pg_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
)

// TestFixtureRepo_FixtureReadyToComplete_TruthTable exercises terminal state,
// score coherence, event debounce, and downstream completion against Postgres.
func TestFixtureRepo_FixtureReadyToComplete_TruthTable(t *testing.T) {
	ctx, pool, repo := setupRepo(t)
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)

	// Fresh fixture in staging — not eligible (not Terminal).
	f := makeStaging(9101, base)
	f.APIStatus = fixture.APIStatus{Short: "ns", Long: "Not Started"}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert staging: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || ready {
		t.Errorf("staging fixture ready = %v (err=%v), want false", ready, err)
	}

	// Move to active, set counter to 3 + Terminal status. No events, no downstream.
	if err := f.Activate(base); err != nil {
		t.Fatalf("activate: %v", err)
	}
	zero := 0
	f.HomeScore, f.AwayScore = &zero, &zero
	// Simulate 3 Terminal polls to prime the counter.
	for i := 0; i < 3; i++ {
		f.UpdateFromPoll(
			fixture.APIStatus{Short: "ft", Long: "Match Finished"},
			nil, nil, nil, nil, true, base.Add(time.Duration(i)*30*time.Second),
		)
	}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert active-terminal: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("terminal+counter=3+no-events ready = %v (err=%v), want true", ready, err)
	}

	// Winner data cannot bypass the coherent three-poll counter.
	f.CompletionCounter = 0
	trueBool := true
	f.HomeWinner = &trueBool
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert winner: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || ready {
		t.Errorf("terminal+winner+counter=0 ready = %v (err=%v), want false", ready, err)
	}
	f.CompletionCounter = 3

	// Add an event in mid-debounce (removed=false, downstream_triggered=false).
	// Directly INSERT bypassing repo since we need to control debounce_count.
	one := 1
	f.HomeScore = &one
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert 1-0 score: %v", err)
	}
	eventID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, team_id, team_name,
			player_id, player_name, detail, minute,
			debounce_count, downstream_triggered, removed, first_seen_at
		) VALUES (
			$1, $2, '40_111_goal_1', 'goal', 40, 'Liverpool',
			111, 'M.Salah', 'normal goal', 42,
			2, false, false, $3
		)
	`, eventID, 9101, base.Add(42*time.Minute))
	if err != nil {
		t.Fatalf("insert mid-debounce event: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || ready {
		t.Errorf("terminal+event-mid-debounce ready = %v (err=%v), want false", ready, err)
	}

	// Settle the event (downstream_triggered = true).
	_, err = pool.Exec(ctx, `
		UPDATE events SET debounce_count = 3, downstream_triggered = true
		WHERE id = $1
	`, eventID)
	if err != nil {
		t.Fatalf("settle event: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("terminal+event-settled+no-downstream ready = %v (err=%v), want true", ready, err)
	}

	// Register an in-flight downstream workflow — completion should
	// block until the row's completed_at fills in.
	_, err = pool.Exec(ctx, `
		INSERT INTO event_downstream_workflows (event_id, workflow_type, workflow_id)
		VALUES ($1, 'discovery', 'discovery-9101-1')
	`, eventID)
	if err != nil {
		t.Fatalf("register downstream workflow: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || ready {
		t.Errorf("terminal+downstream-in-flight ready = %v (err=%v), want false", ready, err)
	}

	// Mark the downstream workflow completed.
	_, err = pool.Exec(ctx, `
		UPDATE event_downstream_workflows SET completed_at = NOW(), outcome_class = 'success'
		WHERE event_id = $1 AND workflow_id = 'discovery-9101-1'
	`, eventID)
	if err != nil {
		t.Fatalf("complete downstream workflow: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("terminal+downstream-completed ready = %v (err=%v), want true", ready, err)
	}

	// Unknown-scorer placeholder that survived to full-time: debounce_count=0,
	// removed=false, downstream_triggered=false, no player attributed. It never
	// triggers downstream, so pre-G1 it matched the event-settled NOT EXISTS
	// clause and blocked completion forever. It still counts as a scoring event
	// for score parity, so make it the second goal in a 2-0 result. It must NOT
	// block once inventory and score agree. (G1 / audit-2026-08-05 Tier-1 #2)
	two := 2
	f.HomeScore = &two
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert 2-0 score: %v", err)
	}
	placeholderID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, team_id, team_name,
			player_id, player_name, detail, minute,
			debounce_count, downstream_triggered, removed, first_seen_at
		) VALUES (
			$1, $2, '40_0_goal_1', 'goal', 40, 'Liverpool',
			NULL, NULL, 'normal goal', 88,
			0, false, false, $3
		)
	`, placeholderID, 9101, base.Add(88*time.Minute))
	if err != nil {
		t.Fatalf("insert unknown-scorer placeholder: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("terminal+unknown-placeholder ready = %v (err=%v), want true", ready, err)
	}

	// Played terminal result with more goals in the score than in surviving
	// storage must remain active. Winner data cannot bypass this parity gate.
	three := 3
	f.HomeScore = &three
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert mismatched 3-0 score: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || ready {
		t.Errorf("terminal+score/event mismatch ready = %v (err=%v), want false", ready, err)
	}

	// Exceptional terminal statuses do not promise a played-match event/score
	// inventory (walkovers and abandoned fixtures are common), so they bypass
	// score parity while retaining every other completion predicate.
	f.APIStatus = fixture.APIStatus{Short: "canc", Long: "Match Cancelled"}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert cancelled status: %v", err)
	}
	if ready, err := repo.FixtureReadyToComplete(ctx, 9101); err != nil || !ready {
		t.Errorf("cancelled+score/event mismatch ready = %v (err=%v), want true", ready, err)
	}
}

// TestFixtureRepo_FixtureReadyToComplete_NotFound
func TestFixtureRepo_FixtureReadyToComplete_NotFound(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	_, err := repo.FixtureReadyToComplete(ctx, 999_999)
	if !errors.Is(err, fixture.ErrNotFound) {
		t.Errorf("Ready for non-existent fixture returned %v, want ErrNotFound", err)
	}
}
