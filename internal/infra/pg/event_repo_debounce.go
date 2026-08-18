// Idempotent event-presence and absence voting transactions.
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

// RegisterEventPresence atomically inserts an idempotent vote, increments the
// counter up to three, and reports the first downstream-trigger transition.
func (r *EventRepo) RegisterEventPresence(ctx context.Context, eventID uuid.UUID, workflowID string) (int, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventPresence: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Attempt to record the vote. RETURNING true fires only if a row
	// was actually inserted; ON CONFLICT is silent.
	var voteInserted bool
	err = tx.QueryRow(ctx, `
		INSERT INTO event_monitor_workflows (event_id, workflow_id)
		VALUES ($1, $2)
		ON CONFLICT (event_id, workflow_id) DO NOTHING
		RETURNING true
	`, eventID, workflowID).Scan(&voteInserted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventPresence: vote: %w", err)
	}
	voteWasNew := err == nil // pgx.ErrNoRows means the ON CONFLICT skipped

	var newCount int
	var wasTriggered bool
	if voteWasNew {
		// Increment (capped at 3). Read current downstream_triggered
		// so we know whether the increment is what flipped it.
		err = tx.QueryRow(ctx, `
			UPDATE events
			SET debounce_count = LEAST(debounce_count + 1, 3)
			WHERE id = $1
			RETURNING debounce_count, downstream_triggered
		`, eventID).Scan(&newCount, &wasTriggered)
	} else {
		// No increment; just report current state.
		err = tx.QueryRow(ctx, `
			SELECT debounce_count, downstream_triggered
			FROM events
			WHERE id = $1
		`, eventID).Scan(&newCount, &wasTriggered)
	}
	if err != nil {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventPresence: count: %w", err)
	}

	// If we're now at 3 AND downstream isn't triggered yet, flip it.
	// The UPDATE ... WHERE NOT downstream_triggered ensures only ONE
	// concurrent presence call ever gets justTriggered=true.
	justTriggered := false
	if newCount == 3 && !wasTriggered {
		var flipped bool
		err = tx.QueryRow(ctx, `
			UPDATE events
			SET downstream_triggered = TRUE
			WHERE id = $1 AND NOT downstream_triggered
			RETURNING true
		`, eventID).Scan(&flipped)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventPresence: flip: %w", err)
		}
		justTriggered = flipped && err == nil
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventPresence: commit: %w", err)
	}
	return newCount, justTriggered, nil
}

// RegisterEventAbsence records an absence vote by workflowID. Atomic:
// idempotent vote insert + counter decrement (floor 0) + soft-delete if
// count hits 0.
//
// Soft-delete details when hitZero: sets removed=TRUE,
// removed_reason='var', removed_at=NOW(). The row is preserved for
// audit; downstream cleanup (Temporal cancel + video_shares soft-delete)
// is the caller's responsibility on hitZero=true. The monitor caller guards
// goal votes with same-response score consistency before invoking this method.
func (r *EventRepo) RegisterEventAbsence(ctx context.Context, eventID uuid.UUID, workflowID string) (int, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsence: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Record the vote (idempotent).
	var voteInserted bool
	err = tx.QueryRow(ctx, `
		INSERT INTO event_drop_workflows (event_id, workflow_id)
		VALUES ($1, $2)
		ON CONFLICT (event_id, workflow_id) DO NOTHING
		RETURNING true
	`, eventID, workflowID).Scan(&voteInserted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsence: vote: %w", err)
	}
	voteWasNew := err == nil

	var newCount int
	var alreadyRemoved bool
	if voteWasNew {
		err = tx.QueryRow(ctx, `
			UPDATE events
			SET debounce_count = GREATEST(debounce_count - 1, 0)
			WHERE id = $1
			RETURNING debounce_count, removed
		`, eventID).Scan(&newCount, &alreadyRemoved)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT debounce_count, removed FROM events WHERE id = $1
		`, eventID).Scan(&newCount, &alreadyRemoved)
	}
	if err != nil {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsence: count: %w", err)
	}

	hitZero := false
	if newCount == 0 && !alreadyRemoved {
		var flipped bool
		err = tx.QueryRow(ctx, `
			UPDATE events
			SET removed = TRUE,
			    removed_reason = 'var',
			    removed_at = NOW()
			WHERE id = $1 AND NOT removed
			RETURNING true
		`, eventID).Scan(&flipped)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsence: soft-delete: %w", err)
		}
		hitZero = flipped && err == nil
	}

	// When the event just flipped to removed (VAR overturn), close out any
	// still-pending downstream workflow rows in the SAME transaction, so
	// fixture completion isn't blocked forever waiting on a discovery for
	// an event that no longer exists. See audit-2026-07-26 P1 #1.
	if hitZero {
		if _, err := tx.Exec(ctx, `
			UPDATE event_downstream_workflows
			SET completed_at = NOW(), outcome_class = $2
			WHERE event_id = $1 AND completed_at IS NULL
		`, eventID, string(event.OutcomeEventRemoved)); err != nil {
			return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsence: close downstream on removal: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsence: commit: %w", err)
	}
	return newCount, hitZero, nil
}
