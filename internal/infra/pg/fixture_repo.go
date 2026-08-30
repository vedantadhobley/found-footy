// fixture_repo.go — Postgres implementation of the fixture.Repo
// domain interface. Uses the embedded *pgxpool.Pool from pg.Pool.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
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

// Column list for SELECTs. Keep scanFixture in the same order.
const fixtureColumns = `
	id, state,
	api_status_short, api_status_long, api_elapsed, api_extra,
	kickoff,
	home_team_id, home_team_name, away_team_id, away_team_name,
	league_id, league_name, league_season,
	home_score, away_score, home_winner, away_winner,
	activated_at, completed_at, terminal_observed_at, last_activity_at, last_polled_at,
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
		&f.ActivatedAt, &f.CompletedAt, &f.TerminalObservedAt, &f.LastActivityAt, &f.LastPolledAt,
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

// Insert creates one fixture without conflict handling. It is intentionally
// outside fixture.Repo: production ingestion uses StoreFromIngest, while
// integration setup and the repository smoke test use this strict primitive.
func (r *FixtureRepo) Insert(ctx context.Context, f *fixture.Fixture) error {
	if err := insertFixture(ctx, r.pool, f); err != nil {
		return fmt.Errorf("pg.FixtureRepo.Insert: %w", err)
	}
	return nil
}

// StoreFromIngest inserts a new fixture with its initial lifecycle state. A
// conflict never changes state or lifecycle timestamps, and applies provider
// fields only when the incoming observation is newer than last_polled_at.
// Equality is an idempotent no-op and gives a simultaneous monitor poll
// precedence over ingestion for an already-known fixture.
func (r *FixtureRepo) StoreFromIngest(ctx context.Context, f *fixture.Fixture) (fixture.State, error) {
	if err := f.ValidateInvariants(); err != nil {
		return "", fmt.Errorf("pg.FixtureRepo.StoreFromIngest: %w", err)
	}
	const query = `
		WITH stored AS (
			INSERT INTO fixtures (
			id, state,
			api_status_short, api_status_long, api_elapsed, api_extra,
			kickoff,
			home_team_id, home_team_name, away_team_id, away_team_name,
			league_id, league_name, league_season,
			home_score, away_score, home_winner, away_winner,
			activated_at, completed_at, terminal_observed_at, last_activity_at, last_polled_at,
			league_country, league_round, home_penalty, away_penalty
			) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19, $20, $21, $22, $23,
			$24, $25, $26, $27
			)
			ON CONFLICT (id) DO UPDATE SET
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
			last_polled_at = EXCLUDED.last_polled_at,
			league_country = EXCLUDED.league_country,
			league_round = EXCLUDED.league_round,
			home_penalty = EXCLUDED.home_penalty,
			away_penalty = EXCLUDED.away_penalty
			WHERE EXCLUDED.last_polled_at IS NOT NULL
			  AND (fixtures.last_polled_at IS NULL OR EXCLUDED.last_polled_at > fixtures.last_polled_at)
			RETURNING state
		)
		SELECT state FROM stored
		UNION ALL
		SELECT state FROM fixtures
		WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM stored)
		LIMIT 1
	`
	var state string
	if err := r.pool.QueryRow(ctx, query, fixtureArgs(f)...).Scan(&state); err != nil {
		return "", fmt.Errorf("pg.FixtureRepo.StoreFromIngest: %w", err)
	}
	return fixture.State(state), nil
}

// RefreshActivePoll writes only the active monitor's provider snapshot. The
// state guard makes a stale poll a no-op after a concurrent completion.
func (r *FixtureRepo) RefreshActivePoll(ctx context.Context, f *fixture.Fixture) (bool, error) {
	if f.State != fixture.StateActive {
		return false, fmt.Errorf("pg.FixtureRepo.RefreshActivePoll: fixture %d is %s", f.ID, f.State)
	}
	if err := f.ValidateInvariants(); err != nil {
		return false, fmt.Errorf("pg.FixtureRepo.RefreshActivePoll: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE fixtures SET
			api_status_short = $2, api_status_long = $3,
			api_elapsed = $4, api_extra = $5,
			kickoff = $6,
			home_team_id = $7, home_team_name = $8,
			away_team_id = $9, away_team_name = $10,
			league_id = $11, league_name = $12, league_season = $13,
			league_country = $14, league_round = $15,
			home_score = $16, away_score = $17,
			home_winner = $18, away_winner = $19,
			terminal_observed_at = $20, last_polled_at = $21,
			home_penalty = $22, away_penalty = $23
		WHERE id = $1 AND state = 'active'
		  AND (last_polled_at IS NULL OR $21 >= last_polled_at)
	`, f.ID, f.APIStatus.Short, f.APIStatus.Long, f.APIElapsed, f.APIExtra,
		f.Kickoff, f.Home.ID, f.Home.Name, f.Away.ID, f.Away.Name,
		f.League.ID, f.League.Name, f.League.Season, f.League.Country, f.League.Round,
		f.HomeScore, f.AwayScore, f.HomeWinner, f.AwayWinner,
		f.TerminalObservedAt, f.LastPolledAt, f.HomePenalty, f.AwayPenalty)
	if err != nil {
		return false, fmt.Errorf("pg.FixtureRepo.RefreshActivePoll: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RefreshStagingPoll writes only the passive staging monitor's fields. The
// state guard prevents a delayed staging response from mutating an active row.
func (r *FixtureRepo) RefreshStagingPoll(ctx context.Context, f *fixture.Fixture) (bool, error) {
	if f.State != fixture.StateStaging {
		return false, fmt.Errorf("pg.FixtureRepo.RefreshStagingPoll: fixture %d is %s", f.ID, f.State)
	}
	if err := f.ValidateInvariants(); err != nil {
		return false, fmt.Errorf("pg.FixtureRepo.RefreshStagingPoll: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE fixtures SET
			api_status_short = $2, api_status_long = $3,
			kickoff = $4,
			home_team_id = $5, home_team_name = $6,
			away_team_id = $7, away_team_name = $8,
			league_id = $9, league_name = $10, league_season = $11,
			league_country = $12, league_round = $13,
			last_polled_at = $14
		WHERE id = $1 AND state = 'staging'
		  AND (last_polled_at IS NULL OR $14 >= last_polled_at)
	`, f.ID, f.APIStatus.Short, f.APIStatus.Long, f.Kickoff,
		f.Home.ID, f.Home.Name, f.Away.ID, f.Away.Name,
		f.League.ID, f.League.Name, f.League.Season, f.League.Country, f.League.Round,
		f.LastPolledAt)
	if err != nil {
		return false, fmt.Errorf("pg.FixtureRepo.RefreshStagingPoll: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// TransitionWithAudit commits a fixture lifecycle transition and its required
// semantic audit row in one transaction. Each transition updates only the
// lifecycle and provider fields that transition owns.
func (r *FixtureRepo) TransitionWithAudit(ctx context.Context, f *fixture.Fixture, record auditlog.Record) (bool, error) {
	if !record.Valid() {
		return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: invalid audit record")
	}
	if record.FixtureID() != f.ID {
		return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: audit fixture %d does not match fixture %d", record.FixtureID(), f.ID)
	}
	if err := f.ValidateInvariants(); err != nil {
		return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: %w", err)
	}
	var expected fixture.State
	switch record.Kind() {
	case auditlog.KindFixtureActivated:
		if f.State != fixture.StateActive {
			return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: activation audit requires active fixture")
		}
		expected = fixture.StateStaging
	case auditlog.KindFixtureCompleted:
		if f.State != fixture.StateCompleted {
			return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: completion audit requires completed fixture")
		}
		expected = fixture.StateActive
	default:
		return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: invalid fixture audit kind %q", record.Kind())
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		current          string
		storedLastPolled *time.Time
	)
	if err := tx.QueryRow(ctx, `SELECT state, last_polled_at FROM fixtures WHERE id = $1 FOR UPDATE`, f.ID).Scan(&current, &storedLastPolled); err != nil {
		return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: lock fixture: %w", err)
	}
	if fixture.State(current) == f.State {
		return false, nil
	}
	if fixture.State(current) != expected {
		return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: transition %s to %s is not allowed", current, f.State)
	}
	if storedLastPolled != nil && (f.LastPolledAt == nil || f.LastPolledAt.Before(*storedLastPolled)) {
		return false, nil
	}

	switch record.Kind() {
	case auditlog.KindFixtureActivated:
		_, err = tx.Exec(ctx, `
			UPDATE fixtures SET
				state = 'active', activated_at = $2,
				api_status_short = $3, api_status_long = $4,
				kickoff = $5,
				home_team_id = $6, home_team_name = $7,
				away_team_id = $8, away_team_name = $9,
				league_id = $10, league_name = $11, league_season = $12,
				league_country = $13, league_round = $14,
				last_polled_at = $15
			WHERE id = $1 AND state = 'staging'
		`, f.ID, f.ActivatedAt, f.APIStatus.Short, f.APIStatus.Long, f.Kickoff,
			f.Home.ID, f.Home.Name, f.Away.ID, f.Away.Name,
			f.League.ID, f.League.Name, f.League.Season, f.League.Country, f.League.Round,
			f.LastPolledAt)
	case auditlog.KindFixtureCompleted:
		_, err = tx.Exec(ctx, `
			UPDATE fixtures SET state = 'completed', completed_at = $2
			WHERE id = $1 AND state = 'active'
		`, f.ID, f.CompletedAt)
	}
	if err != nil {
		return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: fixture: %w", err)
	}
	if err := insertAuditLog(ctx, tx, record); err != nil {
		return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("pg.FixtureRepo.TransitionWithAudit: commit: %w", err)
	}
	return true, nil
}

type fixtureExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func insertFixture(ctx context.Context, exec fixtureExecer, f *fixture.Fixture) error {
	if err := f.ValidateInvariants(); err != nil {
		return err
	}
	const query = `
		INSERT INTO fixtures (
			id, state,
			api_status_short, api_status_long, api_elapsed, api_extra,
			kickoff,
			home_team_id, home_team_name, away_team_id, away_team_name,
			league_id, league_name, league_season,
			home_score, away_score, home_winner, away_winner,
			activated_at, completed_at, terminal_observed_at, last_activity_at, last_polled_at,
			league_country, league_round, home_penalty, away_penalty
		) VALUES (
			$1, $2,
			$3, $4, $5, $6,
			$7,
			$8, $9, $10, $11,
			$12, $13, $14,
			$15, $16, $17, $18,
			$19, $20, $21, $22, $23,
			$24, $25, $26, $27
		)
	`
	_, err := exec.Exec(ctx, query, fixtureArgs(f)...)
	return err
}

func fixtureArgs(f *fixture.Fixture) []any {
	return []any{
		f.ID, string(f.State),
		f.APIStatus.Short, f.APIStatus.Long, f.APIElapsed, f.APIExtra,
		f.Kickoff,
		f.Home.ID, f.Home.Name, f.Away.ID, f.Away.Name,
		f.League.ID, f.League.Name, f.League.Season,
		f.HomeScore, f.AwayScore, f.HomeWinner, f.AwayWinner,
		f.ActivatedAt, f.CompletedAt, f.TerminalObservedAt, f.LastActivityAt, f.LastPolledAt,
		f.League.Country, f.League.Round, f.HomePenalty, f.AwayPenalty,
	}
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

// PublicCompletedCutoff returns midnight UTC for the oldest of the newest
// completedFixtureDates distinct UTC kickoff dates. Nil means no completed
// fixture exists. The media planner consumes this projection; API window
// statements apply the same date rule inside their own read snapshot.
func (r *FixtureRepo) PublicCompletedCutoff(ctx context.Context, completedFixtureDates int) (*time.Time, error) {
	if completedFixtureDates <= 0 {
		return nil, fmt.Errorf("pg.FixtureRepo.PublicCompletedCutoff: completed fixture dates must be > 0")
	}
	var cutoff *time.Time
	if err := r.pool.QueryRow(ctx, `
		SELECT min(fixture_date)::timestamp AT TIME ZONE 'UTC'
		FROM (
			SELECT DISTINCT (kickoff AT TIME ZONE 'UTC')::date AS fixture_date
			FROM fixtures
			WHERE state = 'completed'
			ORDER BY fixture_date DESC
			LIMIT $1
		) recent_completed_dates
	`, completedFixtureDates).Scan(&cutoff); err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.PublicCompletedCutoff: %w", err)
	}
	if cutoff != nil {
		utc := cutoff.UTC()
		cutoff = &utc
	}
	return cutoff, nil
}

// ListPublicWindow returns all staging and active fixtures plus fixtures on the
// newest completedFixtureDates distinct UTC kickoff dates. Cutoff selection and
// fixture selection share one statement snapshot.
func (r *FixtureRepo) ListPublicWindow(ctx context.Context, completedFixtureDates int) ([]*fixture.Fixture, error) {
	if completedFixtureDates <= 0 {
		return nil, fmt.Errorf("pg.FixtureRepo.ListPublicWindow: completed fixture dates must be > 0")
	}
	rows, err := r.pool.Query(ctx,
		`WITH recent_completed_dates AS (
			SELECT DISTINCT (kickoff AT TIME ZONE 'UTC')::date AS fixture_date
			FROM fixtures
			WHERE state = 'completed'
			ORDER BY fixture_date DESC
			LIMIT $1
		), public_cutoff AS (
			SELECT min(fixture_date)::timestamp AT TIME ZONE 'UTC' AS cutoff
			FROM recent_completed_dates
		)
		 SELECT `+fixtureColumns+` FROM fixtures CROSS JOIN public_cutoff
		 WHERE state <> 'completed'
		    OR (public_cutoff.cutoff IS NOT NULL AND kickoff >= public_cutoff.cutoff)
		 ORDER BY CASE state WHEN 'staging' THEN 0 WHEN 'active' THEN 1 ELSE 2 END,
		          updated_at DESC`, completedFixtureDates)
	if err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.ListPublicWindow: %w", err)
	}
	defer rows.Close()
	return collectFixtures(rows)
}

// GetByIDs returns known fixtures in caller order. Unknown IDs are omitted.
func (r *FixtureRepo) GetByIDs(ctx context.Context, ids []int64) ([]*fixture.Fixture, error) {
	if len(ids) == 0 {
		return []*fixture.Fixture{}, nil
	}
	rows, err := r.pool.Query(ctx,
		"SELECT "+fixtureColumns+` FROM fixtures
		 WHERE id = ANY($1::bigint[])
		 ORDER BY array_position($1::bigint[], id)`, ids)
	if err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.GetByIDs: %w", err)
	}
	defer rows.Close()
	return collectFixtures(rows)
}

// SearchPublicFixtures returns publicly-windowed fixtures matching q across
// competition (league) name, either team name, or any of the fixture's event
// scorer/assist names — the free-text search backing GET /api/v1/search. Any
// state (staging/active/completed), kickoff-newest first, capped at limit.
//
// q's ILIKE metacharacters are escaped so a literal "%"/"_" in the query is
// matched verbatim, not as a wildcard. The scorer/assist arm is an EXISTS
// subquery over the fixture's non-removed events (indexed by fixture_id); across
// the bounded retained window a seq scan of the ~hundreds of fixtures is cheap.
func (r *FixtureRepo) SearchPublicFixtures(ctx context.Context, q string, limit, completedFixtureDates int) ([]*fixture.Fixture, error) {
	if completedFixtureDates <= 0 {
		return nil, fmt.Errorf("pg.FixtureRepo.SearchPublicFixtures: completed fixture dates must be > 0")
	}
	pattern := "%" + escapeLike(q) + "%"
	rows, err := r.pool.Query(ctx,
		`WITH recent_completed_dates AS (
			SELECT DISTINCT (kickoff AT TIME ZONE 'UTC')::date AS fixture_date
			FROM fixtures
			WHERE state = 'completed'
			ORDER BY fixture_date DESC
			LIMIT $3
		), public_cutoff AS (
			SELECT min(fixture_date)::timestamp AT TIME ZONE 'UTC' AS cutoff
			FROM recent_completed_dates
		)
		 SELECT `+fixtureColumns+` FROM fixtures CROSS JOIN public_cutoff
		 WHERE (state <> 'completed' OR (public_cutoff.cutoff IS NOT NULL AND kickoff >= public_cutoff.cutoff))
		   AND (league_name ILIKE $1
		    OR home_team_name ILIKE $1
		    OR away_team_name ILIKE $1
		    OR EXISTS (
		        SELECT 1 FROM events e
		        WHERE e.fixture_id = fixtures.id AND NOT e.removed
		          AND (e.player_name ILIKE $1 OR e.assist_name ILIKE $1)
		    ))
		 ORDER BY kickoff DESC
		 LIMIT $2`,
		pattern, limit, completedFixtureDates)
	if err != nil {
		return nil, fmt.Errorf("pg.FixtureRepo.SearchPublicFixtures: %w", err)
	}
	defer rows.Close()
	return collectFixtures(rows)
}

// escapeLike escapes LIKE/ILIKE metacharacters (\ % _) so a user query matches
// literally inside the %…% wrapper — searching "100%" or "a_b" hits those exact
// strings rather than acting as wildcards. Backslash is Postgres's default LIKE
// ESCAPE, so it must be escaped first.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListActiveIDs returns only the fixture IDs currently in state=active.
// ActivePollWorkflow calls this at active cadence to build its batched
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
// at or before threshold. ActivePollWorkflow's ActivateUpcoming step calls
// this with threshold = now + activation window (5 minutes by default).
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

// AssessCompletion evaluates the terminal-grace completion contract per
// docs/decisions/2026-08-25-terminal-observation-grace-bounds-completion.md as a single SQL
// query. Returns fixture.ErrNotFound if id doesn't exist.
//
// Cheap-by-design: the partial index event_downstream_workflows_pending
// makes the "any workflow in flight" check O(1) when the answer is no,
// and the events NOT EXISTS clause short-circuits on the events partial
// index. Runs once per fixture per 30s ActivePoll cycle.
func (r *FixtureRepo) AssessCompletion(
	ctx context.Context,
	id int64,
	terminalBefore time.Time,
) (fixture.CompletionAssessment, error) {
	const query = `
		SELECT
		    f.state = 'active'
		    AND f.api_status_short IN ('ft','aet','pen','canc','abd','wo','awd')
		    AND f.terminal_observed_at IS NOT NULL
		    AND f.terminal_observed_at <= $2
		    AND NOT EXISTS (
		        SELECT 1 FROM events e
		        WHERE e.fixture_id = f.id
		          AND e.removed = false
		          AND e.downstream_triggered = false
		          -- Exclude unknown-scorer placeholders (debounce_count=0). They
		          -- never trigger downstream, so without this a placeholder that
		          -- survives to full-time (scorer never attributed) blocks
		          -- fixture completion forever. Python's
		          -- complete_fixture_if_ready filtered "None" event_ids out of the
		          -- gate for the same reason. Known-scorer events always seed
		          -- count>=1, so this only excludes placeholders, never a real goal
		          -- mid-debounce. See docs/design/audits/audit-2026-08-05.md Tier-1 #2.
		          AND e.debounce_count > 0
		    )
		    AND NOT EXISTS (
		        SELECT 1 FROM event_downstream_workflows edw
		        JOIN events e ON edw.event_id = e.id
		        WHERE e.fixture_id = f.id
		          AND edw.completed_at IS NULL
		    ) AS ready,
		    CASE
		        WHEN f.api_status_short IN ('canc','abd','wo','awd') THEN NULL
		        ELSE f.home_score IS NOT NULL
		          AND f.away_score IS NOT NULL
		          AND f.home_score = (
		              SELECT COUNT(*) FROM events score_home
		              WHERE score_home.fixture_id = f.id
		                AND score_home.event_type = 'goal'
		                AND score_home.team_id = f.home_team_id
		                AND score_home.removed = false
		          )
		          AND f.away_score = (
		              SELECT COUNT(*) FROM events score_away
		              WHERE score_away.fixture_id = f.id
		                AND score_away.event_type = 'goal'
		                AND score_away.team_id = f.away_team_id
		                AND score_away.removed = false
		          )
		    END AS durable_score_event_parity
		FROM fixtures f
		WHERE f.id = $1
	`
	var assessment fixture.CompletionAssessment
	err := r.pool.QueryRow(ctx, query, id, terminalBefore.UTC()).Scan(
		&assessment.Ready,
		&assessment.DurableScoreEventParity,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fixture.CompletionAssessment{}, fixture.ErrNotFound
		}
		return fixture.CompletionAssessment{}, fmt.Errorf("pg.FixtureRepo.AssessCompletion: %w", err)
	}
	return assessment, nil
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
