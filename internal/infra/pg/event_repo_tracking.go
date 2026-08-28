// Downstream-discovery and video-validation workflow registration operations.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

// RegisterDownstreamWorkflow inserts the idempotent pending checklist row that
// AssessCompletion treats as in flight.
func (r *EventRepo) RegisterDownstreamWorkflow(ctx context.Context, eventID uuid.UUID, workflowType, workflowID string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO event_downstream_workflows (event_id, workflow_type, workflow_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id, workflow_type, workflow_id) DO NOTHING
	`, eventID, workflowType, workflowID)
	if err != nil {
		return fmt.Errorf("pg.EventRepo.RegisterDownstreamWorkflow: %w", err)
	}
	return nil
}

// CompleteDownstreamWorkflow closes one exact pending checklist row or reports
// its prior terminal result. Absence is a typed invariant failure.
func (r *EventRepo) CompleteDownstreamWorkflow(
	ctx context.Context,
	eventID uuid.UUID,
	workflowType, workflowID, outcomeClass string,
) (event.DownstreamCompletion, error) {
	var out event.DownstreamCompletion
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("pg.EventRepo.CompleteDownstreamWorkflow: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var completedAt *time.Time
	var storedOutcome *string
	if err := tx.QueryRow(ctx, `
		SELECT completed_at, outcome_class
		FROM event_downstream_workflows
		WHERE event_id = $1 AND workflow_type = $2 AND workflow_id = $3
		FOR UPDATE
	`, eventID, workflowType, workflowID).Scan(&completedAt, &storedOutcome); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, fmt.Errorf("pg.EventRepo.CompleteDownstreamWorkflow: %w", event.ErrDownstreamWorkflowNotFound)
		}
		return out, fmt.Errorf("pg.EventRepo.CompleteDownstreamWorkflow: lock: %w", err)
	}
	if completedAt != nil {
		if err := tx.Commit(ctx); err != nil {
			return out, fmt.Errorf("pg.EventRepo.CompleteDownstreamWorkflow: commit read: %w", err)
		}
		out.State = event.DownstreamAlreadyCompleted
		out.CompletedAt = completedAt.UTC()
		if storedOutcome != nil {
			out.OutcomeClass = *storedOutcome
		}
		return out, nil
	}

	if err := tx.QueryRow(ctx, `
		UPDATE event_downstream_workflows
		SET completed_at = NOW(), outcome_class = $4
		WHERE event_id = $1 AND workflow_type = $2 AND workflow_id = $3
		RETURNING completed_at, outcome_class
	`, eventID, workflowType, workflowID, outcomeClass).Scan(&out.CompletedAt, &out.OutcomeClass); err != nil {
		return out, fmt.Errorf("pg.EventRepo.CompleteDownstreamWorkflow: update: %w", err)
	}
	out.CompletedAt = out.CompletedAt.UTC()
	out.State = event.DownstreamCompletedNow
	if err := tx.Commit(ctx); err != nil {
		return event.DownstreamCompletion{}, fmt.Errorf("pg.EventRepo.CompleteDownstreamWorkflow: commit: %w", err)
	}
	return out, nil
}

// RegisterVideoValidationWorkflow records a download attempt. Unchanged
// by the O2 debounce redesign — tracks download attempts, not
// presence/absence stability.
func (r *EventRepo) RegisterVideoValidationWorkflow(ctx context.Context, eventID uuid.UUID, workflowID string, outcomeClass string) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("pg.EventRepo.RegisterVideoValidationWorkflow: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO event_download_workflows (event_id, workflow_id, outcome_class)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id, workflow_id) DO NOTHING
	`, eventID, workflowID, outcomeClass); err != nil {
		return 0, fmt.Errorf("pg.EventRepo.RegisterVideoValidationWorkflow: insert: %w", err)
	}

	var count int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM event_download_workflows WHERE event_id = $1
	`, eventID).Scan(&count); err != nil {
		return 0, fmt.Errorf("pg.EventRepo.RegisterVideoValidationWorkflow: count: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("pg.EventRepo.RegisterVideoValidationWorkflow: commit: %w", err)
	}
	return count, nil
}
