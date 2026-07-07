// event_repo.go — Postgres implementation of the event.Repo domain
// interface. Same template as fixture_repo.go / alias_repo.go:
// shared column list, rowScanner-based scan, ErrNoRows → domain
// sentinel translation.
//
// Fix 3a of the O2 sequence — basic CRUD only (Get, GetByNaturalKey,
// Insert, Upsert, ListPending). Debounce methods
// (RegisterMonitorWorkflow, RegisterDropWorkflow, etc.) land in fix
// 3b. Interface additions for delete-drops-on-presence + soft-delete
// helpers land in fix 3c.
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
func (r *EventRepo) Insert(ctx context.Context, e *event.Event) error {
	telemetryBytes, err := marshalTelemetry(e.Telemetry)
	if err != nil {
		return fmt.Errorf("pg.EventRepo.Insert: telemetry: %w", err)
	}

	var removedReason *string
	if e.RemovedReason != nil {
		s := string(*e.RemovedReason)
		removedReason = &s
	}

	const query = `
		INSERT INTO events (
			id, fixture_id, natural_key,
			event_type, detail,
			team_id, team_name,
			player_id, player_name,
			minute, extra,
			first_seen_at,
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
			$13, $14,
			$15, $16, $17,
			$18
		)
	`
	if _, err := r.pool.Exec(ctx, query,
		e.ID, e.FixtureID, e.NaturalKey,
		string(e.Type), e.Detail,
		e.Team.ID, e.Team.Name,
		e.Player.ID, e.Player.Name,
		e.Minute, e.Extra,
		e.FirstSeenAt.UTC(),
		e.MonitorComplete, e.DownloadComplete,
		e.Removed, removedReason, e.RemovedAt,
		telemetryBytes,
	); err != nil {
		return fmt.Errorf("pg.EventRepo.Insert: %w", err)
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

// marshalTelemetry serializes the domain map to JSON bytes for JSONB
// storage. nil map → nil bytes (stored as NULL). Empty map → "{}".
func marshalTelemetry(t map[string]any) ([]byte, error) {
	if t == nil {
		return nil, nil
	}
	return json.Marshal(t)
}
