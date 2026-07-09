// team_repo.go — Postgres implementation of the team.Repo domain
// interface. Same pattern as alias_repo.go / fixture_repo.go.
//
// The tracked_teams_cache table is small (≤ ~250 rows for top-5 leagues
// + WC roster), read once per Ingest cycle, written once per 24h. No
// need for index-heavy schema; the primary key + two indices on the
// SQL side cover every read pattern.
package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/domain/team"
)

// TeamRepo backs team.Repo with the pg pool.
type TeamRepo struct {
	pool *Pool
}

// NewTeamRepo constructs a TeamRepo bound to pool.
func NewTeamRepo(pool *Pool) *TeamRepo {
	return &TeamRepo{pool: pool}
}

// List returns every tracked team currently in the cache.
func (r *TeamRepo) List(ctx context.Context) ([]team.TrackedTeam, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT team_id, team_name, league_id, league_name, season, refreshed_at
		FROM tracked_teams_cache
	`)
	if err != nil {
		return nil, fmt.Errorf("pg.TeamRepo.List: %w", err)
	}
	defer rows.Close()

	var out []team.TrackedTeam
	for rows.Next() {
		var t team.TrackedTeam
		if err := rows.Scan(&t.ID, &t.Name, &t.LeagueID, &t.LeagueName, &t.Season, &t.RefreshedAt); err != nil {
			return nil, fmt.Errorf("pg.TeamRepo.List: scan: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.TeamRepo.List: rows: %w", err)
	}
	return out, nil
}

// OldestRefreshedAt returns the earliest refreshed_at across the cache,
// or (zero time, false, nil) if the cache is empty.
func (r *TeamRepo) OldestRefreshedAt(ctx context.Context) (time.Time, bool, error) {
	var oldest *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT MIN(refreshed_at) FROM tracked_teams_cache
	`).Scan(&oldest)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("pg.TeamRepo.OldestRefreshedAt: %w", err)
	}
	if oldest == nil {
		return time.Time{}, false, nil
	}
	return *oldest, true, nil
}

// Replace atomically wipes the cache and writes the given rows, all
// stamped with the same refreshed_at. Uses a single transaction so
// concurrent Ingest cycles never see a partially-refreshed cache.
//
// Empty teams slice is legal — clears the cache. Callers that don't
// want that should guard on len(teams) == 0.
func (r *TeamRepo) Replace(ctx context.Context, teams []team.TrackedTeam, refreshedAt time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg.TeamRepo.Replace: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op if commit succeeded

	if _, err := tx.Exec(ctx, `TRUNCATE tracked_teams_cache`); err != nil {
		return fmt.Errorf("pg.TeamRepo.Replace: truncate: %w", err)
	}

	if len(teams) == 0 {
		return tx.Commit(ctx)
	}

	rows := make([][]any, 0, len(teams))
	for _, t := range teams {
		rows = append(rows, []any{
			t.ID, t.Name, t.LeagueID, t.LeagueName, t.Season, refreshedAt.UTC(),
		})
	}

	_, err = tx.CopyFrom(
		ctx,
		pgx.Identifier{"tracked_teams_cache"},
		[]string{"team_id", "team_name", "league_id", "league_name", "season", "refreshed_at"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("pg.TeamRepo.Replace: copy: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg.TeamRepo.Replace: commit: %w", err)
	}
	return nil
}
