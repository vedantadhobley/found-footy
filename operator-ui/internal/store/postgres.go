// Package store is the operator UI's independent, read-only PostgreSQL adapter.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vedantadhobley/found-footy-operator-ui/internal/model"
)

// ErrNotFound means the requested event or media asset does not exist.
var ErrNotFound = errors.New("operator store: not found")

// Store owns a small read-only pool. It contains no mutating SQL methods.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a pool whose sessions default every transaction to read-only.
func New(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PG_DSN: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = 0
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = make(map[string]string)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.ConnConfig.RuntimeParams["application_name"] = "found-footy-operator-ui"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the operator pool.
func (s *Store) Close() { s.pool.Close() }

// ListEvents returns recent trackable events with retained-media counts.
func (s *Store) ListEvents(ctx context.Context, opts model.ListOptions) (model.EventList, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id::text,e.fixture_id,e.event_type::text,e.detail,
			COALESCE(e.player_name,''),e.team_name,e.minute,COALESCE(e.extra,-1),
			e.first_seen_at,e.removed,f.state::text,f.kickoff,
			f.home_team_name,f.away_team_name,f.league_name,
			(SELECT count(*) FROM video_assets a WHERE a.event_id=e.id),
			(SELECT count(*) FROM video_assets a WHERE a.event_id=e.id AND a.object_reclaimed_at IS NULL),
			(SELECT count(*) FROM video_assets a WHERE a.event_id=e.id AND a.superseded_by IS NULL),
			(SELECT count(*) FROM event_search_candidates c WHERE c.event_id=e.id),
			count(*) OVER()
		FROM events e
		JOIN fixtures f ON f.id=e.fixture_id
		WHERE ($1='' OR lower(concat_ws(' ',e.player_name,e.team_name,f.home_team_name,f.away_team_name,f.league_name,e.detail)) LIKE '%'||lower($1)||'%')
		  AND ($2='' OR e.event_type::text=$2)
		  AND (NOT $3 OR EXISTS(SELECT 1 FROM video_assets a WHERE a.event_id=e.id))
		ORDER BY e.first_seen_at DESC,e.id DESC
		LIMIT $4 OFFSET $5`, opts.Query, opts.EventType, opts.OnlyWithAssets, opts.Limit, opts.Offset)
	if err != nil {
		return model.EventList{}, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	out := model.EventList{Events: make([]model.EventSummary, 0), Limit: opts.Limit, Offset: opts.Offset}
	for rows.Next() {
		var event model.EventSummary
		if err := rows.Scan(
			&event.ID, &event.FixtureID, &event.EventType, &event.Detail,
			&event.PlayerName, &event.TeamName, &event.Minute, &event.Extra,
			&event.FirstSeenAt, &event.Removed, &event.FixtureState, &event.Kickoff,
			&event.HomeTeam, &event.AwayTeam, &event.LeagueName,
			&event.AssetCount, &event.PlayableAssets, &event.RootCount, &event.CandidateCount, &out.Total,
		); err != nil {
			return model.EventList{}, fmt.Errorf("scan event list: %w", err)
		}
		out.Events = append(out.Events, event)
	}
	if err := rows.Err(); err != nil {
		return model.EventList{}, fmt.Errorf("iterate event list: %w", err)
	}
	return out, nil
}

// EventDetail loads one event, every accepted MD5, candidate provenance, and recent graph receipts.
func (s *Store) EventDetail(ctx context.Context, eventID string) (model.EventDetail, error) {
	var out model.EventDetail
	var completedAt, lastPolledAt *time.Time
	var homeScore, awayScore, elapsed, fixtureExtra int
	err := s.pool.QueryRow(ctx, `
		SELECT e.id::text,e.fixture_id,e.event_type::text,e.detail,COALESCE(e.player_name,''),e.team_name,
			e.minute,COALESCE(e.extra,-1),e.first_seen_at,e.removed,
			f.state::text,f.kickoff,f.home_team_name,f.away_team_name,f.league_name,
			(SELECT count(*) FROM video_assets a WHERE a.event_id=e.id),
			(SELECT count(*) FROM video_assets a WHERE a.event_id=e.id AND a.object_reclaimed_at IS NULL),
			(SELECT count(*) FROM video_assets a WHERE a.event_id=e.id AND a.superseded_by IS NULL),
			(SELECT count(*) FROM event_search_candidates c WHERE c.event_id=e.id),
			f.api_status_short,f.api_status_long,COALESCE(f.api_elapsed,-1),COALESCE(f.api_extra,-1),
			COALESCE(f.home_score,-1),COALESCE(f.away_score,-1),f.league_country,f.league_round,
			f.completed_at,f.last_polled_at
		FROM events e JOIN fixtures f ON f.id=e.fixture_id WHERE e.id=$1`, eventID).Scan(
		&out.Event.ID, &out.Event.FixtureID, &out.Event.EventType, &out.Event.Detail,
		&out.Event.PlayerName, &out.Event.TeamName, &out.Event.Minute, &out.Event.Extra,
		&out.Event.FirstSeenAt, &out.Event.Removed, &out.Event.FixtureState, &out.Event.Kickoff,
		&out.Event.HomeTeam, &out.Event.AwayTeam, &out.Event.LeagueName,
		&out.Event.AssetCount, &out.Event.PlayableAssets, &out.Event.RootCount, &out.Event.CandidateCount,
		&out.Fixture.StatusShort, &out.Fixture.StatusLong, &elapsed, &fixtureExtra,
		&homeScore, &awayScore, &out.Fixture.LeagueCountry, &out.Fixture.LeagueRound,
		&completedAt, &lastPolledAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.EventDetail{}, ErrNotFound
	}
	if err != nil {
		return model.EventDetail{}, fmt.Errorf("load event: %w", err)
	}
	out.Fixture.ID = out.Event.FixtureID
	out.Fixture.State = out.Event.FixtureState
	out.Fixture.Kickoff = out.Event.Kickoff
	out.Fixture.HomeTeam = out.Event.HomeTeam
	out.Fixture.AwayTeam = out.Event.AwayTeam
	out.Fixture.LeagueName = out.Event.LeagueName
	out.Fixture.Elapsed, out.Fixture.Extra = elapsed, fixtureExtra
	out.Fixture.HomeScore, out.Fixture.AwayScore = homeScore, awayScore
	out.Fixture.CompletedAt, out.Fixture.LastPolledAt = completedAt, lastPolledAt

	if out.Assets, err = s.loadAssets(ctx, eventID); err != nil {
		return model.EventDetail{}, err
	}
	if out.Candidates, err = s.loadCandidates(ctx, eventID); err != nil {
		return model.EventDetail{}, err
	}
	if out.SelectionCommits, err = s.loadSelectionCommits(ctx, eventID); err != nil {
		return model.EventDetail{}, err
	}
	return out, nil
}

// loadAssets reads every accepted variant, including shareless and superseded assets.
func (s *Store) loadAssets(ctx context.Context, eventID string) ([]model.Asset, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id::text,encode(a.md5,'hex'),a.hash_version,octet_length(a.frame_hashes)/8,
			a.width,a.height,a.duration_ms,a.file_size_bytes,COALESCE(a.bitrate,0),COALESCE(a.frame_rate,0),
			a.popularity,
			(SELECT count(*) FROM event_search_candidates c WHERE c.observed_asset_id=a.id),
			(SELECT count(*) FROM event_search_candidates c WHERE c.credited_asset_id=a.id),
			COALESCE(a.superseded_by::text,''),a.first_seen_at,a.object_reclaimed_at,
			COALESCE(sh.id,''),COALESCE(sh.state::text,''),COALESCE(sh.timestamp_verified,false),
			COALESCE(sh.extracted_minute,-1),
			COALESCE((SELECT v.evidence FROM video_asset_validations v WHERE v.asset_id=a.id ORDER BY v.recorded_at,v.id LIMIT 1),'{}'::jsonb)
		FROM video_assets a
		LEFT JOIN video_shares sh ON sh.asset_id=a.id AND sh.event_id=a.event_id
		WHERE a.event_id=$1
		ORDER BY (a.superseded_by IS NULL) DESC,a.popularity DESC,a.width*a.height DESC,a.duration_ms DESC,a.first_seen_at`, eventID)
	if err != nil {
		return nil, fmt.Errorf("load assets: %w", err)
	}
	defer rows.Close()
	assets := make([]model.Asset, 0)
	for rows.Next() {
		var asset model.Asset
		var validation []byte
		if err := rows.Scan(
			&asset.ID, &asset.MD5, &asset.HashVersion, &asset.FrameCount,
			&asset.Width, &asset.Height, &asset.DurationMS, &asset.FileSizeBytes, &asset.Bitrate, &asset.FrameRate,
			&asset.Popularity, &asset.ExactObservations, &asset.RoutedObservations,
			&asset.SupersededBy, &asset.FirstSeenAt, &asset.ReclaimedAt,
			&asset.ShareID, &asset.ShareState, &asset.TimestampVerified, &asset.ExtractedMinute, &validation,
		); err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		if string(validation) != "{}" {
			asset.Validation = json.RawMessage(validation)
		}
		asset.Playable = asset.ReclaimedAt == nil
		asset.PublicRoot = asset.SupersededBy == "" && asset.ShareState == "active"
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assets: %w", err)
	}
	return assets, nil
}

// loadCandidates retains rejected and failed search evidence alongside accepted variants.
func (s *Store) loadCandidates(ctx context.Context, eventID string) ([]model.Candidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text,search_attempt,tweet_url,tweet_text,username,discovered_at,outcome_class,
			COALESCE(reject_reason,''),outcome_at,COALESCE(observed_asset_id::text,''),COALESCE(credited_asset_id::text,'')
		FROM event_search_candidates WHERE event_id=$1 ORDER BY discovered_at,id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("load candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]model.Candidate, 0)
	for rows.Next() {
		var candidate model.Candidate
		if err := rows.Scan(&candidate.ID, &candidate.SearchAttempt, &candidate.TweetURL, &candidate.TweetText,
			&candidate.Username, &candidate.DiscoveredAt, &candidate.OutcomeClass, &candidate.RejectReason,
			&candidate.OutcomeAt, &candidate.ObservedAssetID, &candidate.CreditedAssetID); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate candidates: %w", err)
	}
	return candidates, nil
}

// loadSelectionCommits exposes only the latest receipt because its result is a
// complete current plan and can approach the schema's one-megabyte bound.
func (s *Store) loadSelectionCommits(ctx context.Context, eventID string) ([]model.SelectionCommit, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,committed_at,policy,result
		FROM video_selection_commits WHERE event_id=$1 ORDER BY committed_at DESC,id DESC LIMIT 1`, eventID)
	if err != nil {
		return nil, fmt.Errorf("load selection commits: %w", err)
	}
	defer rows.Close()
	commits := make([]model.SelectionCommit, 0)
	for rows.Next() {
		var commit model.SelectionCommit
		var policy, result []byte
		if err := rows.Scan(&commit.ID, &commit.CommittedAt, &policy, &result); err != nil {
			return nil, fmt.Errorf("scan selection commit: %w", err)
		}
		commit.Policy, commit.Result = json.RawMessage(policy), json.RawMessage(result)
		commits = append(commits, commit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate selection commits: %w", err)
	}
	return commits, nil
}

// MediaObject resolves only retained event assets; it never accepts a raw object key.
func (s *Store) MediaObject(ctx context.Context, assetID string) (model.MediaObject, error) {
	var object model.MediaObject
	err := s.pool.QueryRow(ctx, `SELECT s3_bucket,s3_key,object_reclaimed_at FROM video_assets WHERE id=$1`, assetID).
		Scan(&object.Bucket, &object.Key, &object.ReclaimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.MediaObject{}, ErrNotFound
	}
	if err != nil {
		return model.MediaObject{}, fmt.Errorf("resolve media: %w", err)
	}
	return object, nil
}

// ChangeToken reads PostgreSQL's cumulative mutation counters for the seven
// operator-visible tables. This avoids repeatedly scanning application data.
// Statistics may flush late or reset at server restart; either changes the
// token, while the watcher's forced authoritative resync covers delayed stats.
func (s *Store) ChangeToken(ctx context.Context) (string, error) {
	var token string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(string_agg(
		relname||':'||n_tup_ins||':'||n_tup_upd||':'||n_tup_del,'|' ORDER BY relname),'')
		FROM pg_stat_user_tables
		WHERE relname=ANY(ARRAY[
			'fixtures','events','video_assets','video_asset_validations',
			'video_selection_commits','video_shares','event_search_candidates'
		])`).Scan(&token)
	if err != nil {
		return "", fmt.Errorf("load change token: %w", err)
	}
	return token, nil
}
