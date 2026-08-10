// event_repo.go — Postgres implementation of the event.Repo domain
// interface. Same template as fixture_repo.go / alias_repo.go:
// shared column list, rowScanner-based scan, ErrNoRows → domain
// sentinel translation.
//
// Implements the symmetric-counter debounce model per decisions.md
// 2026-07-07 entry:
//   - Insert(ctx, e, workflowID) — atomic new-event + first vote seed
//   - RegisterEventPresence — idempotent increment, may flip
//     downstream_triggered on the first crossing of count=3
//   - RegisterEventAbsence — idempotent decrement, atomic soft-delete
//     with removed_reason='var' on hit-zero
//   - RegisterVideoValidationWorkflow — monotonic attempt counter
//     (unchanged by the debounce redesign)
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

// EventRepo backs event.Repo with the pg pool.
type EventRepo struct {
	pool *Pool
}

// NewEventRepo constructs an EventRepo bound to pool.
func NewEventRepo(pool *Pool) *EventRepo {
	return &EventRepo{pool: pool}
}

// Column list for events. Same shared-const discipline as the other
// repos — read + write column order stay aligned in one place.
const eventColumns = `
	id, fixture_id, natural_key,
	event_type, detail,
	team_id, team_name,
	player_id, player_name,
	minute, extra,
	first_seen_at,
	debounce_count, downstream_triggered,
	monitor_complete, download_complete,
	removed, removed_reason, removed_at,
	telemetry,
	created_at, updated_at
`

// scanEvent reads one events row into a domain Event. Translates
// pgx.ErrNoRows → event.ErrNotFound so callers gate on the domain
// sentinel rather than the pgx-specific error.
//
// telemetry (JSONB) comes back as []byte from pgx; we unmarshal into
// the map[string]any that the domain type carries. Nil JSONB → nil map.
func scanEvent(row rowScanner) (*event.Event, error) {
	var e event.Event
	var telemetryBytes []byte
	var eventType string
	var removedReason *string

	if err := row.Scan(
		&e.ID, &e.FixtureID, &e.NaturalKey,
		&eventType, &e.Detail,
		&e.Team.ID, &e.Team.Name,
		&e.Player.ID, &e.Player.Name,
		&e.Minute, &e.Extra,
		&e.FirstSeenAt,
		&e.DebounceCount, &e.DownstreamTriggered,
		&e.MonitorComplete, &e.DownloadComplete,
		&e.Removed, &removedReason, &e.RemovedAt,
		&telemetryBytes,
		&e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, event.ErrNotFound
		}
		return nil, fmt.Errorf("pg.EventRepo.scanEvent: %w", err)
	}
	e.Type = event.Type(eventType)
	if removedReason != nil {
		r := event.RemovalReason(*removedReason)
		e.RemovedReason = &r
	}
	if len(telemetryBytes) > 0 {
		if err := json.Unmarshal(telemetryBytes, &e.Telemetry); err != nil {
			return nil, fmt.Errorf("pg.EventRepo.scanEvent: telemetry unmarshal: %w", err)
		}
	}
	return &e, nil
}

// Get returns the event by UUID or event.ErrNotFound.
func (r *EventRepo) Get(ctx context.Context, id uuid.UUID) (*event.Event, error) {
	row := r.pool.QueryRow(ctx,
		"SELECT "+eventColumns+" FROM events WHERE id = $1", id)
	return scanEvent(row)
}

// GetByNaturalKey returns the event for (fixture_id, natural_key) or
// event.ErrNotFound. Called by MonitorWorkflow when it sees an API
// event and wants to know if we already track it.
func (r *EventRepo) GetByNaturalKey(ctx context.Context, fixtureID int64, naturalKey string) (*event.Event, error) {
	row := r.pool.QueryRow(ctx,
		"SELECT "+eventColumns+" FROM events WHERE fixture_id = $1 AND natural_key = $2",
		fixtureID, naturalKey)
	return scanEvent(row)
}

// Insert creates a new event row. Fails with a wrapped pgconn.PgError
// (23505 unique_violation) if (fixture_id, natural_key) collides. That
// error IS the concurrent-detection-race signal — the caller catches,
// re-Gets by natural key, and proceeds with the winner's UUID.
//
// telemetry (JSONB) uses json.Marshal for consistency with scanEvent's
// unmarshal. Nil map serializes as `null` — schema allows JSONB NULL.
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

// RegisterEventPresence records a presence vote by workflowID. Atomic:
// idempotent vote insert + counter increment (capped at 3) + first-time
// threshold flip. Returns the post-vote count and whether THIS call was
// the one that flipped downstream_triggered.
//
// Idempotency guarantee: the workflow_id is the primary key part in
// event_monitor_workflows, so ON CONFLICT DO NOTHING catches retries.
// If the vote was already recorded, we skip the counter increment
// entirely — no double-count on activity retries.
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
// is the caller's responsibility on hitZero=true.
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

// RegisterDownstreamWorkflow inserts a pending row into
// event_downstream_workflows so FixtureReadyToComplete sees the
// workflow as in-flight. Idempotent on
// (event_id, workflow_type, workflow_id). See event.Repo
// docstring for the load-bearing rationale.
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

// ListPending returns events in the fixture that need more work
// (NOT removed AND (NOT monitor_complete OR NOT download_complete)).
// Uses the partial index events_pending_work for cheap lookup.
// Ordered by first_seen_at so callers see earliest-detected events
// first.
func (r *EventRepo) ListPending(ctx context.Context, fixtureID int64) ([]*event.Event, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT "+eventColumns+` FROM events
		 WHERE fixture_id = $1
		   AND NOT removed
		   AND (NOT monitor_complete OR NOT download_complete)
		 ORDER BY first_seen_at`,
		fixtureID)
	if err != nil {
		return nil, fmt.Errorf("pg.EventRepo.ListPending: %w", err)
	}
	defer rows.Close()

	var events []*event.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.EventRepo.ListPending: rows: %w", err)
	}
	return events, nil
}

// ListByFixture returns all NON-removed events for a fixture, ordered by
// minute then first-seen — the display read backing the read API's fixture +
// event endpoints (#167). Distinct from ListPending, which filters to
// pipeline-work-remaining; this returns every event the frontend should show
// (removed/VAR events are excluded). Served by the events_fixture index.
func (r *EventRepo) ListByFixture(ctx context.Context, fixtureID int64) ([]*event.Event, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT "+eventColumns+` FROM events
		 WHERE fixture_id = $1 AND NOT removed
		 ORDER BY minute, first_seen_at`,
		fixtureID)
	if err != nil {
		return nil, fmt.Errorf("pg.EventRepo.ListByFixture: %w", err)
	}
	defer rows.Close()

	var events []*event.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.EventRepo.ListByFixture: rows: %w", err)
	}
	return events, nil
}

// EventsAwaitingDiscovery returns confirmed, not-removed events whose
// discovery workflow hasn't completed yet (spawn failed, or still in
// flight). Drives ReconcileFixture's spawn-recovery pass. See event.Repo.
func (r *EventRepo) EventsAwaitingDiscovery(ctx context.Context, fixtureID int64) ([]*event.Event, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT "+eventColumns+` FROM events
		 WHERE fixture_id = $1
		   AND downstream_triggered
		   AND NOT removed
		   AND NOT EXISTS (
		       SELECT 1 FROM event_downstream_workflows edw
		       WHERE edw.event_id = events.id
		         AND edw.workflow_type = 'discovery'
		         AND edw.completed_at IS NOT NULL
		   )
		 ORDER BY first_seen_at`,
		fixtureID)
	if err != nil {
		return nil, fmt.Errorf("pg.EventRepo.EventsAwaitingDiscovery: %w", err)
	}
	defer rows.Close()

	var events []*event.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.EventRepo.EventsAwaitingDiscovery: rows: %w", err)
	}
	return events, nil
}

// marshalTelemetry serializes the domain map to JSON bytes for JSONB
// storage. nil map → nil bytes (stored as NULL). Empty map → "{}".
func marshalTelemetry(t map[string]any) ([]byte, error) {
	if t == nil {
		return nil, nil
	}
	return json.Marshal(t)
}
