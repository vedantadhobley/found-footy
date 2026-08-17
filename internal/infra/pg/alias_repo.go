// alias_repo.go — Postgres implementation of the alias.Repo domain
// interface. Same pattern as fixture_repo.go: shared column list,
// rowScanner-based scan, ErrNoRows → domain sentinel translation.
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
)

// AliasRepo backs alias.Repo with the pg pool.
type AliasRepo struct {
	pool *Pool
}

// NewAliasRepo constructs an AliasRepo bound to pool.
func NewAliasRepo(pool *Pool) *AliasRepo {
	return &AliasRepo{pool: pool}
}

// Column list for team_aliases. One const keeps read + write column
// order aligned across scanAlias / UpsertVendorFields / UpsertResolution.
const aliasColumns = `
	team_id, canonical_name, team_code,
	country, city, is_national,
	wikidata_qid, aliases, resolved_at,
	created_at, updated_at
`

// scanAlias reads one team_aliases row into a domain TeamAlias.
// Returns alias.ErrNotFound on ErrNoRows so callers gate on the
// domain sentinel.
func scanAlias(row rowScanner) (*alias.TeamAlias, error) {
	var ta alias.TeamAlias
	if err := row.Scan(
		&ta.TeamID, &ta.CanonicalName, &ta.TeamCode,
		&ta.Country, &ta.City, &ta.IsNational,
		&ta.WikidataQID, &ta.Aliases, &ta.ResolvedAt,
		&ta.CreatedAt, &ta.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, alias.ErrNotFound
		}
		return nil, fmt.Errorf("pg.AliasRepo.scanAlias: %w", err)
	}
	return &ta, nil
}

// Get returns the alias by team ID, or alias.ErrNotFound.
func (r *AliasRepo) Get(ctx context.Context, teamID int) (*alias.TeamAlias, error) {
	row := r.pool.QueryRow(ctx,
		"SELECT "+aliasColumns+" FROM team_aliases WHERE team_id = $1", teamID)
	return scanAlias(row)
}

// BulkGet returns a map of team_id → *TeamAlias for every id in ids
// that has a cached row. Missing IDs are simply absent from the map
// (no error). Empty ids input returns an empty map.
//
// Ingest's daily team-cache refresh uses this: one SQL round-trip
// instead of len(ids) Gets.
func (r *AliasRepo) BulkGet(ctx context.Context, ids []int) (map[int]*alias.TeamAlias, error) {
	if len(ids) == 0 {
		return map[int]*alias.TeamAlias{}, nil
	}
	rows, err := r.pool.Query(ctx,
		"SELECT "+aliasColumns+" FROM team_aliases WHERE team_id = ANY($1)", ids)
	if err != nil {
		return nil, fmt.Errorf("pg.AliasRepo.BulkGet: %w", err)
	}
	defer rows.Close()

	out := make(map[int]*alias.TeamAlias, len(ids))
	for rows.Next() {
		ta, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		out[ta.TeamID] = ta
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.AliasRepo.BulkGet: %w", err)
	}
	return out, nil
}

// UpsertVendorFields writes active vendor fields only. If the row exists,
// dormant resolver fields are preserved.
//
// created_at is set on INSERT via table default; updated_at is
// refreshed by the trg_team_aliases_updated_at trigger.
func (r *AliasRepo) UpsertVendorFields(ctx context.Context, ta *alias.TeamAlias) error {
	const query = `
		INSERT INTO team_aliases (
			team_id, canonical_name, team_code,
			country, city, is_national
		) VALUES (
			$1, $2, $3,
			$4, $5, $6
		)
		ON CONFLICT (team_id) DO UPDATE SET
			canonical_name = EXCLUDED.canonical_name,
			team_code = EXCLUDED.team_code,
			country = EXCLUDED.country,
			city = EXCLUDED.city,
			is_national = EXCLUDED.is_national
	`
	_, err := r.pool.Exec(ctx, query,
		ta.TeamID, ta.CanonicalName, ta.TeamCode,
		ta.Country, ta.City, ta.IsNational,
	)
	if err != nil {
		return fmt.Errorf("pg.AliasRepo.UpsertVendorFields: %w", err)
	}
	return nil
}

// UpsertResolution writes a full row including dormant resolver fields.
// Retained for compatibility; production has no caller.
//
// nil-slice discipline: schema has NOT NULL DEFAULT '{}' on aliases,
// so a nil []string would fail the constraint on INSERT. Normalize
// to empty slice.
func (r *AliasRepo) UpsertResolution(ctx context.Context, ta *alias.TeamAlias) error {
	aliases := ta.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	const query = `
		INSERT INTO team_aliases (
			team_id, canonical_name, team_code,
			country, city, is_national,
			wikidata_qid, aliases, resolved_at
		) VALUES (
			$1, $2, $3,
			$4, $5, $6,
			$7, $8, $9
		)
		ON CONFLICT (team_id) DO UPDATE SET
			canonical_name = EXCLUDED.canonical_name,
			team_code = EXCLUDED.team_code,
			country = EXCLUDED.country,
			city = EXCLUDED.city,
			is_national = EXCLUDED.is_national,
			wikidata_qid = EXCLUDED.wikidata_qid,
			aliases = EXCLUDED.aliases,
			resolved_at = EXCLUDED.resolved_at
	`
	_, err := r.pool.Exec(ctx, query,
		ta.TeamID, ta.CanonicalName, ta.TeamCode,
		ta.Country, ta.City, ta.IsNational,
		ta.WikidataQID, aliases, ta.ResolvedAt,
	)
	if err != nil {
		return fmt.Errorf("pg.AliasRepo.UpsertResolution: %w", err)
	}
	return nil
}
