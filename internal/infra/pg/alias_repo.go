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

// Column list for team_aliases. Same discipline as fixture — one const
// keeps read + write column order aligned.
const aliasColumns = `
	team_id, team_name, is_national,
	country, city,
	wikidata_qid, wikidata_aliases, twitter_aliases, llm_model,
	created_at, updated_at
`

// scanAlias reads one team_aliases row into a domain TeamAlias.
// Returns alias.ErrNotFound on ErrNoRows so callers gate on the
// domain sentinel.
func scanAlias(row rowScanner) (*alias.TeamAlias, error) {
	var ta alias.TeamAlias
	if err := row.Scan(
		&ta.TeamID, &ta.TeamName, &ta.IsNational,
		&ta.Country, &ta.City,
		&ta.WikidataQID, &ta.WikidataAliases, &ta.TwitterAliases, &ta.LLMModel,
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
// The IngestWorkflow's alias-pre-caching step calls this with the
// flattened team-ID set from the day's fixtures — one SQL round-trip
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

// Upsert inserts or updates by team_id primary key. Written columns
// exclude created_at + updated_at: created_at preserved on UPDATE by
// omission; updated_at maintained by the trg_team_aliases_updated_at
// trigger.
//
// nil-vs-empty-slice discipline: the schema has NOT NULL DEFAULT '{}'
// on both alias arrays, so passing []string(nil) would attempt a NULL
// insert and fail the constraint. Normalize to empty slice here.
func (r *AliasRepo) Upsert(ctx context.Context, ta *alias.TeamAlias) error {
	wikidataAliases := ta.WikidataAliases
	if wikidataAliases == nil {
		wikidataAliases = []string{}
	}
	twitterAliases := ta.TwitterAliases
	if twitterAliases == nil {
		twitterAliases = []string{}
	}

	const query = `
		INSERT INTO team_aliases (
			team_id, team_name, is_national,
			country, city,
			wikidata_qid, wikidata_aliases, twitter_aliases, llm_model
		) VALUES (
			$1, $2, $3,
			$4, $5,
			$6, $7, $8, $9
		)
		ON CONFLICT (team_id) DO UPDATE SET
			team_name = EXCLUDED.team_name,
			is_national = EXCLUDED.is_national,
			country = EXCLUDED.country,
			city = EXCLUDED.city,
			wikidata_qid = EXCLUDED.wikidata_qid,
			wikidata_aliases = EXCLUDED.wikidata_aliases,
			twitter_aliases = EXCLUDED.twitter_aliases,
			llm_model = EXCLUDED.llm_model
	`
	_, err := r.pool.Exec(ctx, query,
		ta.TeamID, ta.TeamName, ta.IsNational,
		ta.Country, ta.City,
		ta.WikidataQID, wikidataAliases, twitterAliases, ta.LLMModel,
	)
	if err != nil {
		return fmt.Errorf("pg.AliasRepo.Upsert: %w", err)
	}
	return nil
}
