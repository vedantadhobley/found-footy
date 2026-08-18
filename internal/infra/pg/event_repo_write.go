// Event creation, mutable-field updates, placeholder deletion, and upsert operations.
package pg

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

// Insert creates a new event row. A natural-key collision is the concurrent
// detection-race signal; callers re-read the winning row and continue.
func (r *EventRepo) Insert(ctx context.Context, e *event.Event, workflowID string) error {
	telemetryBytes, err := marshalTelemetry(e.Telemetry)
	if err != nil {
		return fmt.Errorf("pg.EventRepo.Insert: telemetry: %w", err)
	}
	var removedReason *string
	if e.RemovedReason != nil {
		s := string(*e.RemovedReason)
		removedReason = &s
	}

	// Unknown-scorer events land as placeholders: debounce_count=0 and NO
	// presence vote — "not a full event yet" (Python parity, monitor.py
	// initial_count=0). They stay pinned at 0 until the vendor attributes a
	// scorer (a new player-keyed natural_key supersedes this row) or the
	// placeholder vanishes and is hard-deleted (DeleteUnknownEvent). Known
	// scorers seed 1 + the first vote and debounce normally. See
	// decisions.md unknown-scorer debounce entry.
	initialCount := 1
	if !e.Player.Known() {
		initialCount = 0
	}

	// Atomic: INSERT the event (debounce_count=initialCount) AND, for a known
	// scorer, INSERT the first presence vote. If the caller retries after a
	// mid-transaction crash, the outer INSERT hits the natural_key UNIQUE and
	// the caller falls through to RegisterEventPresence — which finds the
	// workflow_id already recorded here (known) and no-ops cleanly.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg.EventRepo.Insert: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insertEvent = `
		INSERT INTO events (
			id, fixture_id, natural_key,
			event_type, detail,
			team_id, team_name,
			player_id, player_name,
			assist_id, assist_name,
			minute, extra,
			first_seen_at,
			debounce_count, downstream_triggered,
			monitor_complete, download_complete,
			removed, removed_reason, removed_at,
			telemetry
		) VALUES (
			$1, $2, $3,
			$4, $5,
			$6, $7,
			$8, $9,
			$20, $21,
			$10, $11,
			$12,
			$19, FALSE,
			$13, $14,
			$15, $16, $17,
			$18
		)
	`
	if _, err := tx.Exec(ctx, insertEvent,
		e.ID, e.FixtureID, e.NaturalKey,
		string(e.Type), e.Detail,
		e.Team.ID, e.Team.Name,
		e.Player.ID, e.Player.Name,
		e.Minute, e.Extra,
		e.FirstSeenAt.UTC(),
		e.MonitorComplete, e.DownloadComplete,
		e.Removed, removedReason, e.RemovedAt,
		telemetryBytes,
		initialCount,
		e.Assist.ID, e.Assist.Name,
	); err != nil {
		return fmt.Errorf("pg.EventRepo.Insert: event: %w", err)
	}

	// Seed the first presence vote only for a known scorer. An unknown
	// placeholder holds 0 votes (mirrors Python's empty monitor_workflows)
	// so it never counts toward the 3-vote downstream trigger.
	if initialCount > 0 {
		const insertVote = `
			INSERT INTO event_monitor_workflows (event_id, workflow_id)
			VALUES ($1, $2)
		`
		if _, err := tx.Exec(ctx, insertVote, e.ID, workflowID); err != nil {
			return fmt.Errorf("pg.EventRepo.Insert: seed vote: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg.EventRepo.Insert: commit: %w", err)
	}
	// Reflect the seeded value on the caller's local struct so they
	// don't need to re-read to know what state we ended in.
	e.DebounceCount = initialCount
	e.DownstreamTriggered = false
	return nil
}

// UpdateMutableFields re-persists the provider-mutable, non-identity fields of
// an existing event onto row id — assist (arrives late), minute + extra
// (VAR-corrected), detail (reclassified). Identity (team/player/type/
// natural_key) and lifecycle (debounce, downstream, removal) are untouched.
// ReconcileFixture calls this only when the values actually changed, so it is
// not a per-cycle write. See #199.
func (r *EventRepo) UpdateMutableFields(ctx context.Context, id uuid.UUID, fresh *event.Event) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE events SET
			assist_id   = $2,
			assist_name = $3,
			minute      = $4,
			extra       = $5,
			detail      = $6,
			updated_at  = now()
		WHERE id = $1
	`, id, fresh.Assist.ID, fresh.Assist.Name, fresh.Minute, fresh.Extra, fresh.Detail); err != nil {
		return fmt.Errorf("pg.EventRepo.UpdateMutableFields: %w", err)
	}
	return nil
}

// DeleteUnknownEvent hard-deletes an unknown-scorer placeholder by UUID. It
// is called only from the reconcile absence loop when a placeholder
// (debounce_count 0, no scorer) disappears from the API — usually because
// the vendor attributed the scorer and a new player-keyed natural_key
// superseded it.
//
// Hard delete, NOT the soft-delete/VAR path (RegisterEventAbsence), because a
// placeholder was never a confirmed event: it carries no audit weight, and
// routing it through the VAR path would mis-stamp removed_reason='var', emit a
// misleading event.removed, and overload the count-0 state. The
// debounce_count=0 guard makes this a no-op for any confirmed event (a present
// confirmed event is always ≥1), so a caller bug can't hard-delete a real
// event. Child vote rows CASCADE; the ON DELETE RESTRICT on
// video_shares.event_id can't fire (placeholders never mint shares) and stands
// as a fail-loud guard if one ever did. See decisions.md unknown-scorer entry.
func (r *EventRepo) DeleteUnknownEvent(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM events WHERE id = $1 AND debounce_count = 0`, id)
	if err != nil {
		return fmt.Errorf("pg.EventRepo.DeleteUnknownEvent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return event.ErrNotFound
	}
	return nil
}

// Upsert updates an EXISTING event row's mutable state fields
// (monitor_complete, download_complete, removed*, telemetry). Meant
// for state transitions after Insert has already placed the row. The
// updated_at column is maintained by the trg_events_updated_at
// trigger; caller doesn't set it.
//
// Note on shape: this is UPDATE-not-INSERT-not-conflict — if the row
// doesn't exist, no error is returned but 0 rows affected. Callers
// that need "did I actually update anything?" should either Get first
// or pipeline this with a RETURNING clause on a future change.
func (r *EventRepo) Upsert(ctx context.Context, e *event.Event) error {
	telemetryBytes, err := marshalTelemetry(e.Telemetry)
	if err != nil {
		return fmt.Errorf("pg.EventRepo.Upsert: telemetry: %w", err)
	}
	var removedReason *string
	if e.RemovedReason != nil {
		s := string(*e.RemovedReason)
		removedReason = &s
	}

	const query = `
		UPDATE events SET
			monitor_complete = $2,
			download_complete = $3,
			removed = $4,
			removed_reason = $5,
			removed_at = $6,
			telemetry = $7
		WHERE id = $1
	`
	if _, err := r.pool.Exec(ctx, query,
		e.ID,
		e.MonitorComplete, e.DownloadComplete,
		e.Removed, removedReason, e.RemovedAt,
		telemetryBytes,
	); err != nil {
		return fmt.Errorf("pg.EventRepo.Upsert: %w", err)
	}
	return nil
}
