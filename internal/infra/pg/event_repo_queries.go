// Fixture-scoped event lists used by reconciliation, recovery, and the read API.
package pg

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

// GetByIDs returns known events in caller order. Unknown IDs are omitted.
func (r *EventRepo) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]*event.Event, error) {
	if len(ids) == 0 {
		return []*event.Event{}, nil
	}
	rows, err := r.pool.Query(ctx,
		"SELECT "+eventColumns+` FROM events
		 WHERE id = ANY($1::uuid[])
		 ORDER BY array_position($1::uuid[], id)`, ids)
	if err != nil {
		return nil, fmt.Errorf("pg.EventRepo.GetByIDs: %w", err)
	}
	defer rows.Close()
	return collectEvents(rows, "GetByIDs")
}

// ListByFixtures returns every non-removed event for the requested fixtures in
// stable fixture/minute order. The API groups this one batch in memory.
func (r *EventRepo) ListByFixtures(ctx context.Context, fixtureIDs []int64) ([]*event.Event, error) {
	if len(fixtureIDs) == 0 {
		return []*event.Event{}, nil
	}
	rows, err := r.pool.Query(ctx,
		"SELECT "+eventColumns+` FROM events
		 WHERE fixture_id = ANY($1::bigint[]) AND NOT removed
		 ORDER BY array_position($1::bigint[], fixture_id), minute, first_seen_at, id`, fixtureIDs)
	if err != nil {
		return nil, fmt.Errorf("pg.EventRepo.ListByFixtures: %w", err)
	}
	defer rows.Close()
	return collectEvents(rows, "ListByFixtures")
}

// ListPending returns non-removed events whose monitor or download work is
// incomplete, ordered by first observation.
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

func collectEvents(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}, operation string) ([]*event.Event, error) {
	var events []*event.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.EventRepo.%s: rows: %w", operation, err)
	}
	return events, nil
}

// ListAllByFixture returns active and soft-removed events in detection order.
// It is an identity-history query for monitor reconciliation, not a display
// query; callers decide which rows are eligible for presence/absence votes.
func (r *EventRepo) ListAllByFixture(ctx context.Context, fixtureID int64) ([]*event.Event, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT "+eventColumns+` FROM events
		 WHERE fixture_id = $1
		 ORDER BY first_seen_at, id`,
		fixtureID)
	if err != nil {
		return nil, fmt.Errorf("pg.EventRepo.ListAllByFixture: %w", err)
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
		return nil, fmt.Errorf("pg.EventRepo.ListAllByFixture: rows: %w", err)
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
