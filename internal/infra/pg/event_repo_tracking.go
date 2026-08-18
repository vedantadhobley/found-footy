// Downstream-discovery and video-validation workflow registration operations.
package pg

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// RegisterDownstreamWorkflow inserts the idempotent pending checklist row that
// FixtureReadyToComplete treats as in flight.
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
