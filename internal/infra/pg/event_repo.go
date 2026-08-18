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
	assist_id, assist_name,
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
		&e.Assist.ID, &e.Assist.Name,
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

// ListLiveFleetEventIDs returns the events that should currently hold a
// per-event Firefox instance — the reaper's KEEP set (#160 fleet reaper,
// audit P0-5). An instance is legitimately alive from provision (count=1,
// during the active window) until the EventWorkflow releases it, so an event
// is "live" when it is not removed AND either:
//
//   - its fixture is still active (covers the pre-trigger debounce window,
//     where no downstream row exists yet), OR
//   - a downstream workflow is still in flight (completed_at IS NULL) — this
//     is what keeps a LATE-MATCH goal's instance safe: the fixture has
//     already flipped active→completed but discovery is still searching.
//
// Only the fixture-active branch would keep it otherwise, and that branch is
// false post-whistle → the OR is load-bearing, not redundant. A labeled
// container whose event is NOT in this set is an orphan to reap. Bias broad:
// no player_id / trigger filters, since excluding a still-live instance loses
// a goal's clips whereas an extra keep-id is harmless.
func (r *EventRepo) ListLiveFleetEventIDs(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT e.id
		FROM events e
		JOIN fixtures f ON f.id = e.fixture_id
		WHERE e.removed = false
		  AND (
		      f.state = 'active'
		      OR EXISTS (
		          SELECT 1 FROM event_downstream_workflows edw
		          WHERE edw.event_id = e.id AND edw.completed_at IS NULL
		      )
		  )`)
	if err != nil {
		return nil, fmt.Errorf("pg.EventRepo.ListLiveFleetEventIDs: %w", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("pg.EventRepo.ListLiveFleetEventIDs: scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DiscoveryComplete returns the subset of eventIDs whose 'discovery' downstream
// workflow has finished (edw.completed_at set) — the read-side signal the API
// derives event.Phase from (separating `searching` from `complete`). Batched
// to avoid an N+1 across a fixture's events; absent IDs (no discovery row, or
// still in flight) are simply not in the returned set. IDs are passed as text
// with an ::uuid[] cast so the array encoding never depends on a uuid codec.
func (r *EventRepo) DiscoveryComplete(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(eventIDs))
	if len(eventIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(eventIDs))
	for i, id := range eventIDs {
		ids[i] = id.String()
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT event_id
		FROM event_downstream_workflows
		WHERE workflow_type = 'discovery'
		  AND completed_at IS NOT NULL
		  AND event_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("pg.EventRepo.DiscoveryComplete: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("pg.EventRepo.DiscoveryComplete: scan: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// GetByNaturalKey returns the event for (fixture_id, natural_key) or
// event.ErrNotFound. Called during active-fixture reconciliation when it sees an API
// event and wants to know if we already track it.
func (r *EventRepo) GetByNaturalKey(ctx context.Context, fixtureID int64, naturalKey string) (*event.Event, error) {
	row := r.pool.QueryRow(ctx,
		"SELECT "+eventColumns+" FROM events WHERE fixture_id = $1 AND natural_key = $2",
		fixtureID, naturalKey)
	return scanEvent(row)
}

// marshalTelemetry serializes the domain map to JSON bytes for JSONB storage.
// A nil map stays SQL NULL; an empty map becomes an empty JSON object.
func marshalTelemetry(t map[string]any) ([]byte, error) {
	if t == nil {
		return nil, nil
	}
	return json.Marshal(t)
}
