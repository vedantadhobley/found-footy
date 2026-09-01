// Idempotent event-presence and absence voting transactions.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

// RegisterEventPresence atomically inserts an idempotent vote, increments the
// counter up to three, and reports the first downstream-trigger transition.
func (r *EventRepo) RegisterEventPresence(ctx context.Context, eventID uuid.UUID, workflowID string) (int, bool, error) {
	return r.registerEventPresence(ctx, eventID, workflowID, nil)
}

// RegisterEventPresenceWithAudit atomically appends event.stable only when
// this vote performs the downstream-trigger transition.
func (r *EventRepo) RegisterEventPresenceWithAudit(
	ctx context.Context,
	eventID uuid.UUID,
	workflowID string,
	record auditlog.Record,
) (int, bool, error) {
	if !record.Valid() || record.Kind() != auditlog.KindEventStable || record.EventID() != eventID {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventPresenceWithAudit: audit identity does not match event")
	}
	return r.registerEventPresence(ctx, eventID, workflowID, &record)
}

func (r *EventRepo) registerEventPresence(
	ctx context.Context,
	eventID uuid.UUID,
	workflowID string,
	record *auditlog.Record,
) (int, bool, error) {
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
	var fixtureID int64
	if voteWasNew {
		// Increment (capped at 3). Read current downstream_triggered
		// so we know whether the increment is what flipped it.
		err = tx.QueryRow(ctx, `
			UPDATE events
			SET debounce_count = LEAST(debounce_count + 1, 3)
			WHERE id = $1
			RETURNING debounce_count, downstream_triggered, fixture_id
		`, eventID).Scan(&newCount, &wasTriggered, &fixtureID)
	} else {
		// No increment; just report current state.
		err = tx.QueryRow(ctx, `
			SELECT debounce_count, downstream_triggered, fixture_id
			FROM events
			WHERE id = $1
		`, eventID).Scan(&newCount, &wasTriggered, &fixtureID)
	}
	if err != nil {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventPresence: count: %w", err)
	}
	if record != nil && record.FixtureID() != fixtureID {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventPresenceWithAudit: audit fixture does not match event")
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
	if justTriggered && record != nil {
		if err := insertAuditLog(ctx, tx, *record); err != nil {
			return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventPresenceWithAudit: audit: %w", err)
		}
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
	return r.registerEventAbsence(ctx, eventID, workflowID, nil)
}

// RegisterEventAbsenceWithAudit atomically appends event.removed only when
// this vote performs the debounce-zero soft removal.
func (r *EventRepo) RegisterEventAbsenceWithAudit(
	ctx context.Context,
	eventID uuid.UUID,
	workflowID string,
	record auditlog.Record,
) (int, bool, error) {
	if !record.Valid() || record.Kind() != auditlog.KindEventRemoved || record.EventID() != eventID {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsenceWithAudit: audit identity does not match event")
	}
	return r.registerEventAbsence(ctx, eventID, workflowID, &record)
}

func (r *EventRepo) registerEventAbsence(
	ctx context.Context,
	eventID uuid.UUID,
	workflowID string,
	record *auditlog.Record,
) (int, bool, error) {
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
	var fixtureID int64
	if voteWasNew {
		err = tx.QueryRow(ctx, `
			UPDATE events
			SET debounce_count = GREATEST(debounce_count - 1, 0)
			WHERE id = $1
			RETURNING debounce_count, removed, fixture_id
		`, eventID).Scan(&newCount, &alreadyRemoved, &fixtureID)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT debounce_count, removed, fixture_id FROM events WHERE id = $1
		`, eventID).Scan(&newCount, &alreadyRemoved, &fixtureID)
	}
	if err != nil {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsence: count: %w", err)
	}
	if record != nil && record.FixtureID() != fixtureID {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsenceWithAudit: audit fixture does not match event")
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

	// When the event just flipped to removed (VAR overturn), close every
	// candidate before its owning downstream row in the SAME transaction.
	// Candidate writers share this event lock, so work that loses the race
	// observes removed=true and cannot recreate pending afterward.
	if hitZero {
		if _, err := terminalizePendingCandidatesForRemovedEvent(
			ctx, tx, eventID, "", time.Now().UTC(),
		); err != nil {
			return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsence: close candidates on removal: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE event_downstream_workflows
			SET completed_at = NOW(), outcome_class = $2
			WHERE event_id = $1 AND completed_at IS NULL
		`, eventID, string(event.OutcomeEventRemoved)); err != nil {
			return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsence: close downstream on removal: %w", err)
		}
		if record != nil {
			if err := insertAuditLog(ctx, tx, *record); err != nil {
				return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsenceWithAudit: audit: %w", err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("pg.EventRepo.RegisterEventAbsence: commit: %w", err)
	}
	return newCount, hitZero, nil
}
