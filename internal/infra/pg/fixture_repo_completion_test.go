// FixtureRepo terminal-grace completion-contract integration tests.
package pg_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
)

func TestFixtureRepo_AssessCompletion_TruthTable(t *testing.T) {
	ctx, pool, repo := setupRepo(t)
	observedAt := time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC)
	f := makeStaging(9101, observedAt.Add(-2*time.Hour))
	f.APIStatus = fixture.APIStatus{Short: "ns", Long: "Not Started"}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert staging: %v", err)
	}

	assessment, err := repo.AssessCompletion(ctx, f.ID, observedAt)
	if err != nil || assessment.Ready {
		t.Fatalf("staging assessment = %+v (err=%v), want not ready", assessment, err)
	}

	if err := f.Activate(observedAt.Add(-2 * time.Hour)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	zero := 0
	f.UpdateFromPoll(
		fixture.APIStatus{Short: "ft", Long: "Match Finished"},
		nil, nil, &zero, &zero, observedAt,
	)
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert terminal: %v", err)
	}

	assessment, err = repo.AssessCompletion(ctx, f.ID, observedAt.Add(-time.Nanosecond))
	if err != nil || assessment.Ready {
		t.Fatalf("before grace boundary = %+v (err=%v), want not ready", assessment, err)
	}
	assessment, err = repo.AssessCompletion(ctx, f.ID, observedAt)
	if err != nil || !assessment.Ready {
		t.Fatalf("at grace boundary = %+v (err=%v), want ready", assessment, err)
	}
	if assessment.DurableScoreEventParity == nil || !*assessment.DurableScoreEventParity {
		t.Fatalf("0-0 durable parity = %v, want true", assessment.DurableScoreEventParity)
	}

	one := 1
	f.HomeScore = &one
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert 1-0: %v", err)
	}
	eventID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, team_id, team_name,
			player_id, player_name, detail, minute,
			debounce_count, downstream_triggered, removed, first_seen_at
		) VALUES (
			$1, $2, '40_111_goal_1', 'goal', 40, 'Liverpool',
			111, 'M.Salah', 'normal goal', 42,
			2, false, false, $3
		)
	`, eventID, f.ID, observedAt.Add(-time.Hour))
	if err != nil {
		t.Fatalf("insert mid-debounce event: %v", err)
	}
	assessment, err = repo.AssessCompletion(ctx, f.ID, observedAt)
	if err != nil || assessment.Ready {
		t.Fatalf("mid-debounce assessment = %+v (err=%v), want not ready", assessment, err)
	}

	if _, err = pool.Exec(ctx, `
		UPDATE events SET debounce_count = 3, downstream_triggered = true WHERE id = $1
	`, eventID); err != nil {
		t.Fatalf("settle event: %v", err)
	}
	assessment, err = repo.AssessCompletion(ctx, f.ID, observedAt)
	if err != nil || !assessment.Ready {
		t.Fatalf("settled assessment = %+v (err=%v), want ready", assessment, err)
	}

	if _, err = pool.Exec(ctx, `
		INSERT INTO event_downstream_workflows (event_id, workflow_type, workflow_id)
		VALUES ($1, 'discovery', 'discovery-9101-1')
	`, eventID); err != nil {
		t.Fatalf("register downstream: %v", err)
	}
	assessment, err = repo.AssessCompletion(ctx, f.ID, observedAt)
	if err != nil || assessment.Ready {
		t.Fatalf("pending downstream assessment = %+v (err=%v), want not ready", assessment, err)
	}
	if _, err = pool.Exec(ctx, `
		UPDATE event_downstream_workflows
		SET completed_at = NOW(), outcome_class = 'success'
		WHERE event_id = $1
	`, eventID); err != nil {
		t.Fatalf("complete downstream: %v", err)
	}

	placeholderID := uuid.New()
	if _, err = pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, team_id, team_name,
			player_id, player_name, detail, minute,
			debounce_count, downstream_triggered, removed, first_seen_at
		) VALUES (
			$1, $2, '40_0_goal_1', 'goal', 40, 'Liverpool',
			NULL, NULL, 'normal goal', 88,
			0, false, false, $3
		)
	`, placeholderID, f.ID, observedAt); err != nil {
		t.Fatalf("insert placeholder: %v", err)
	}
	assessment, err = repo.AssessCompletion(ctx, f.ID, observedAt)
	if err != nil || !assessment.Ready {
		t.Fatalf("unknown placeholder assessment = %+v (err=%v), want ready", assessment, err)
	}
	if assessment.DurableScoreEventParity == nil || *assessment.DurableScoreEventParity {
		t.Fatalf("mismatched durable parity = %v, want false audit evidence", assessment.DurableScoreEventParity)
	}

	f.APIStatus = fixture.APIStatus{Short: "canc", Long: "Match Cancelled"}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert exceptional terminal: %v", err)
	}
	assessment, err = repo.AssessCompletion(ctx, f.ID, observedAt)
	if err != nil || !assessment.Ready || assessment.DurableScoreEventParity != nil {
		t.Fatalf("exceptional assessment = %+v (err=%v), want ready with nil parity", assessment, err)
	}
}

func TestFixtureRepo_AssessCompletion_PenaltyDecisionIsEvidenceNotGate(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	observedAt := time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC)
	f := makeStaging(9102, observedAt.Add(-2*time.Hour))
	if err := f.Activate(observedAt.Add(-2 * time.Hour)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	zero := 0
	f.UpdateFromPoll(
		fixture.APIStatus{Short: "pen", Long: "Match Finished After Penalties"},
		nil, nil, &zero, &zero, observedAt,
	)
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	assessment, err := repo.AssessCompletion(ctx, f.ID, observedAt)
	if err != nil || !assessment.Ready {
		t.Fatalf("missing shootout assessment = %+v (err=%v), want ready", assessment, err)
	}
}

func TestFixtureRepo_AssessCompletion_NotFound(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	_, err := repo.AssessCompletion(ctx, 999_999, time.Now())
	if !errors.Is(err, fixture.ErrNotFound) {
		t.Errorf("assessment for missing fixture returned %v, want ErrNotFound", err)
	}
}
