// fixture_repo.go — Postgres implementation of the fixture.Repo
// domain interface. Uses the embedded *pgxpool.Pool from pg.Pool.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
)

// FixtureRepo backs fixture.Repo with the pg pool. Kept as a
// dedicated type rather than methods on *Pool so callers depend on
// the domain interface via constructor injection.
type FixtureRepo struct {
	pool *Pool
}

// NewFixtureRepo constructs a FixtureRepo bound to pool.
func NewFixtureRepo(pool *Pool) *FixtureRepo {
	return &FixtureRepo{pool: pool}
}

// Column list for SELECTs. Kept as a single constant so scan order in
// scanFixture and INSERT column order in Upsert can't drift apart —
// change one, change both.
const fixtureColumns = `
	id, state,
	api_status_short, api_status_long, api_elapsed, api_extra,
	kickoff,
	home_team_id, home_team_name, away_team_id, away_team_name,
	league_id, league_name, league_season,
	home_score, away_score, home_winner, away_winner,
	activated_at, completed_at, last_activity_at, last_polled_at,
	completion_counter,
	league_country, league_round, home_penalty, away_penalty,
	created_at, updated_at
`

// rowScanner is implemented by both pgx.Row (single-row QueryRow
// results) and pgx.Rows (iterator over multi-row Query results).
// Lets scanFixture serve both call paths.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanFixture reads one fixtures-table row into a domain Fixture.
// Returns fixture.ErrNotFound when the row scan yields ErrNoRows so
// callers can errors.Is-check for the miss case.
func scanFixture(row rowScanner) (*fixture.Fixture, error) {
	var f fixture.Fixture
	var stateStr string
	if err := row.Scan(
		&f.ID, &stateStr,
		&f.APIStatus.Short, &f.APIStatus.Long, &f.APIElapsed, &f.APIExtra,
		&f.Kickoff,
		&f.Home.ID, &f.Home.Name, &f.Away.ID, &f.Away.Name,
		&f.League.ID, &f.League.Name, &f.League.Season,
		&f.HomeScore, &f.AwayScore, &f.HomeWinner, &f.AwayWinner,
		&f.ActivatedAt, &f.CompletedAt, &f.LastActivityAt, &f.LastPolledAt,
		&f.CompletionCounter,
		&f.League.Country, &f.League.Round, &f.HomePenalty, &f.AwayPenalty,
		&f.CreatedAt, &f.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fixture.ErrNotFound
		}
		return nil, fmt.Errorf("pg.FixtureRepo.scanFixture: %w", err)
	}
	f.State = fixture.State(stateStr)
	return &f, nil
}

// Get returns the fixture by ID, or fixture.ErrNotFound.
func (r *FixtureRepo) Get(ctx context.Context, id int64) (*fixture.Fixture, error) {
	row := r.pool.QueryRow(ctx,
		"SELECT "+fixtureColumns+" FROM fixtures WHERE id = $1", id)
	return scanFixture(row)
}

// Upsert inserts a new fixture or updates the existing row by id
// primary key. Written columns exclude created_at + updated_at:
// created_at is preserved on UPDATE via ON CONFLICT DO UPDATE not
// listing it; updated_at is maintained by the trg_fixtures_updated_at
// trigger. On INSERT the two default to NOW() per schema.
func (r *FixtureRepo) Upsert(ctx context.Context, f *fixture.Fixture) error {
	if err := f.ValidateInvariants(); err != nil {
		return fmt.Errorf("pg.FixtureRepo.Upsert: %w", err)
	}
	const query = `
		INSERT INTO fixtures (
			id, state,
			api_status_short, api_status_long, api_elapsed, api_extra,
			kickoff,
			home_team_id, home_team_name, away_team_id, away_team_name,
			league_id, league_name, league_season,
			home_score, away_score, home_winner, away_winner,
			activated_at, completed_at, last_activity_at, last_polled_at,
			completion_counter,
			league_country, league_round, home_penalty, away_penalty
		) VALUES (
			$1, $2,
			$3, $4, $5, $6,
			$7,
			$8, $9, $10, $11,
			$12, $13, $14,
			$15, $16, $17, $18,
			$19, $20, $21, $22,
			$23,
			$24, $25, $26, $27
		)
		ON CONFLICT (id) DO UPDATE SET
			state = EXCLUDED.state,
			api_status_short = EXCLUDED.api_status_short,
			api_status_long = EXCLUDED.api_status_long,
			api_elapsed = EXCLUDED.api_elapsed,
			api_extra = EXCLUDED.api_extra,
			kickoff = EXCLUDED.kickoff,
			home_team_id = EXCLUDED.home_team_id,
			home_team_name = EXCLUDED.home_team_name,
			away_team_id = EXCLUDED.away_team_id,
			away_team_name = EXCLUDED.away_team_name,
			league_id = EXCLUDED.league_id,
			league_name = EXCLUDED.league_name,
			league_season = EXCLUDED.league_season,
			home_score = EXCLUDED.home_score,
			away_score = EXCLUDED.away_score,
			home_winner = EXCLUDED.home_winner,
			away_winner = EXCLUDED.away_winner,
			activated_at = EXCLUDED.activated_at,
			completed_at = EXCLUDED.completed_at,
			last_activity_at = EXCLUDED.last_activity_at,
			last_polled_at = EXCLUDED.last_polled_at,
			completion_counter = EXCLUDED.completion_counter,
			league_country = EXCLUDED.league_country,
			league_round = EXCLUDED.league_round,
			home_penalty = EXCLUDED.home_penalty,
			away_penalty = EXCLUDED.away_penalty
	`
	_, err := r.pool.Exec(ctx, query,
		f.ID, string(f.State),
		f.APIStatus.Short, f.APIStatus.Long, f.APIElapsed, f.APIExtra,
		f.Kickoff,
		f.Home.ID, f.Home.Name, f.Away.ID, f.Away.Name,
		f.League.ID, f.League.Name, f.League.Season,
		f.HomeScore, f.AwayScore, f.HomeWinner, f.AwayWinner,
		f.ActivatedAt, f.CompletedAt, f.LastActivityAt, f.LastPolledAt,
		f.CompletionCounter,
		f.League.Country, f.League.Round, f.HomePenalty, f.AwayPenalty,
	)
	if err != nil {
		return fmt.Errorf("pg.FixtureRepo.Upsert: %w", err)
	}
	return nil
}

// ListByState returns all fixtures in the given state, most recently
// updated first. Uses the partial indexes on state (see schema.sql
// fixtures_active_by_polled / fixtures_completed_recent / etc.) so
// even a full-table sweep stays cheap.
func (r *FixtureRepo) ListByState(ctx context.Context, state fixture.State) ([]*fixture.Fixture, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT "+fixtureColumns+" FROM fixtures WHERE state = $1 ORDER BY updated_at DESC",
		string(state))
	if err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.ListByState: %w", err)
	}
	defer rows.Close()
	return collectFixtures(rows)
}

// ListActiveIDs returns only the fixture IDs currently in state=active.
// The MonitorWorkflow calls this every 30s to build its batched
// /fixtures?ids= API call, and needs just the ID column — hitting the
// full row set (ListByState) would waste pgx unmarshaling on ~2KB
// of fields we throw away. The index on (state) makes this a cheap
// index-only scan.
func (r *FixtureRepo) ListActiveIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT id FROM fixtures WHERE state = 'active' ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.ListActiveIDs: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("pg.FixtureRepo.ListActiveIDs: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.ListActiveIDs: rows: %w", err)
	}
	return ids, nil
}

// ListStagingBeforeKickoff returns staging fixtures whose kickoff is
// at or before threshold. The MonitorWorkflow's PreActivateUpcoming
// step calls this every 30s with threshold = now + activation window
// (default 30 min) — see decisions.md 2026-07-07 activation triggers.
func (r *FixtureRepo) ListStagingBeforeKickoff(ctx context.Context, threshold time.Time) ([]*fixture.Fixture, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT "+fixtureColumns+" FROM fixtures WHERE state = 'staging' AND kickoff <= $1 ORDER BY kickoff",
		threshold.UTC())
	if err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.ListStagingBeforeKickoff: %w", err)
	}
	defer rows.Close()
	return collectFixtures(rows)
}

// FixtureReadyToComplete evaluates the full completion contract per
// docs/design/proposals/completion-contract.md as a single SQL
// query. Returns fixture.ErrNotFound if id doesn't exist.
//
// Cheap-by-design: the partial index event_downstream_workflows_pending
// makes the "any workflow in flight" check O(1) when the answer is no,
// and the events NOT EXISTS clause short-circuits on the events partial
// index. Runs once per fixture per 30s ActivePoll cycle.
func (r *FixtureRepo) FixtureReadyToComplete(ctx context.Context, id int64) (bool, error) {
	const query = `
		SELECT
		    f.api_status_short IN ('ft','aet','pen','canc','abd','wo','awd')
		    AND (f.completion_counter >= 3
		         OR f.home_winner IS NOT NULL
		         OR f.away_winner IS NOT NULL)
		    AND NOT EXISTS (
		        SELECT 1 FROM events e
		        WHERE e.fixture_id = f.id
		          AND e.removed = false
		          AND e.downstream_triggered = false
		          -- Exclude unknown-scorer placeholders (debounce_count=0). They
		          -- never trigger downstream, so without this a placeholder that
		          -- survives to full-time (scorer never attributed) blocks
		          -- completion forever and the fixture never prunes. Python's
		          -- complete_fixture_if_ready filtered "None" event_ids out of the
		          -- gate for the same reason. Known-scorer events always seed
		          -- count>=1, so this only excludes placeholders, never a real goal
		          -- mid-debounce. See docs/design/audit-2026-08-05.md Tier-1 #2.
		          AND e.debounce_count > 0
		    )
		    AND NOT EXISTS (
		        SELECT 1 FROM event_downstream_workflows edw
		        JOIN events e ON edw.event_id = e.id
		        WHERE e.fixture_id = f.id
		          AND edw.completed_at IS NULL
		    ) AS ready
		FROM fixtures f
		WHERE f.id = $1
	`
	var ready bool
	err := r.pool.QueryRow(ctx, query, id).Scan(&ready)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, fixture.ErrNotFound
		}
		return false, fmt.Errorf("pg.FixtureRepo.FixtureReadyToComplete: %w", err)
	}
	return ready, nil
}

// PruneCompleted deletes completed fixtures whose completed_at is
// older than threshold AND which have NO surviving video_shares.
// This honors the URL-stability invariant (§3): fixtures with any
// public share stay indefinitely; only truly unreferenced fixtures
// prune. Returns the number of rows deleted.
//
// The RESTRICT chain (video_shares → events → fixtures) at the FK
// layer would block a DELETE even without this WHERE clause, but
// checking here surfaces the "nothing to prune" outcome cleanly
// (rowsAffected = 0) rather than the DB rejecting individual rows.
func (r *FixtureRepo) PruneCompleted(ctx context.Context, threshold time.Time) (int, error) {
	const query = `
		DELETE FROM fixtures f
		WHERE f.state = 'completed'
		  AND f.completed_at < $1
		  AND NOT EXISTS (
			  SELECT 1 FROM events e
			  JOIN video_shares s ON s.event_id = e.id
			  WHERE e.fixture_id = f.id
		  )
	`
	tag, err := r.pool.Exec(ctx, query, threshold.UTC())
	if err != nil {
		return 0, fmt.Errorf("pg.FixtureRepo.PruneCompleted: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ListReclaimableEventIDs returns event IDs of completed fixtures older
// than threshold that still carry a non-removed share — the byte-reclaim
// worklist for retention's clip-bearing half (#176). DISTINCT because an
// event can hold several shares (the superseded chain). See the
// fixture.Repo interface doc for the URL-stability rationale: the caller
// DestroyEvents each (Garage bytes reclaimed, rows kept as 410 tombstones).
func (r *FixtureRepo) ListReclaimableEventIDs(ctx context.Context, threshold time.Time) ([]uuid.UUID, error) {
	const query = `
		SELECT DISTINCT e.id
		FROM fixtures f
		JOIN events e ON e.fixture_id = f.id
		JOIN video_shares s ON s.event_id = e.id
		WHERE f.state = 'completed'
		  AND f.completed_at < $1
		  AND s.state <> 'removed'
	`
	rows, err := r.pool.Query(ctx, query, threshold.UTC())
	if err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.ListReclaimableEventIDs: %w", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("pg.FixtureRepo.ListReclaimableEventIDs: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.ListReclaimableEventIDs: rows: %w", err)
	}
	return ids, nil
}

// collectFixtures walks a pgx.Rows iterator and scans each into a
// domain Fixture. Closes rows on error to keep the connection healthy.
func collectFixtures(rows pgx.Rows) ([]*fixture.Fixture, error) {
	var out []*fixture.Fixture
	for rows.Next() {
		f, err := scanFixture(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.collectFixtures: %w", err)
	}
	return out, nil
}
