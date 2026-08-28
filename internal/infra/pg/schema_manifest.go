// Required object manifest for adoption and post-migration schema verification.
package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var requiredTables = []string{
	"fixtures", "events", "event_monitor_workflows", "event_download_workflows",
	"event_drop_workflows", "event_downstream_workflows", "video_assets",
	"video_shares", "event_search_candidates", "team_aliases",
	"tracked_teams_cache", "twitter_sessions", "event_log",
	"webhook_subscriptions", "webhook_deliveries", "outbox_cursor",
}

var requiredIndexes = []string{
	"fixtures_staging_by_kickoff", "fixtures_active_by_polled", "fixtures_completed_recent",
	"events_fixture", "events_pending_work", "events_by_first_seen",
	"event_downstream_workflows_pending", "video_assets_event_popularity",
	"video_shares_event_rank_active", "video_shares_event_asset", "video_shares_event",
	"video_shares_asset", "event_search_candidates_event", "event_search_candidates_fixture",
	"event_search_candidates_discovered_at", "event_search_candidates_credited_asset",
	"team_aliases_needs_refresh", "tracked_teams_cache_league_season",
	"tracked_teams_cache_refreshed_at", "event_log_created", "event_log_event",
	"webhook_deliveries_created",
}

var requiredTypes = []string{"fixture_state", "event_type", "share_state", "removal_reason"}

var requiredTriggers = []string{
	"trg_fixtures_updated_at", "trg_events_updated_at", "trg_team_aliases_updated_at",
	"trg_twitter_sessions_updated_at", "trg_webhook_subs_updated_at",
}

func verifyBaselineSchema(ctx context.Context, tx pgx.Tx) error {
	if err := requireExtension(ctx, tx, "pgcrypto"); err != nil {
		return err
	}
	for _, name := range requiredTables {
		if err := requireRelation(ctx, tx, name); err != nil {
			return err
		}
	}
	for _, name := range requiredTypes {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_type t
				JOIN pg_namespace n ON n.oid = t.typnamespace
				WHERE n.nspname = 'public' AND t.typname = $1
			)
		`, name).Scan(&exists); err != nil {
			return fmt.Errorf("check type %s: %w", name, err)
		}
		if !exists {
			return fmt.Errorf("required type %s is missing", name)
		}
	}
	var functionExists bool
	if err := tx.QueryRow(ctx, `SELECT to_regprocedure('public.set_updated_at()') IS NOT NULL`).Scan(&functionExists); err != nil {
		return fmt.Errorf("check set_updated_at function: %w", err)
	}
	if !functionExists {
		return fmt.Errorf("required function set_updated_at() is missing")
	}
	for _, name := range requiredTriggers {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = $1 AND NOT tgisinternal)`, name).Scan(&exists); err != nil {
			return fmt.Errorf("check trigger %s: %w", name, err)
		}
		if !exists {
			return fmt.Errorf("required trigger %s is missing", name)
		}
	}
	return nil
}

func verifyCurrentSchema(ctx context.Context, tx pgx.Tx) error {
	if err := verifyBaselineSchema(ctx, tx); err != nil {
		return err
	}
	for _, name := range requiredIndexes {
		if err := requireRelation(ctx, tx, name); err != nil {
			return err
		}
	}
	for _, pair := range [][2]string{
		{"video_assets", "hash_version"},
		{"fixtures", "terminal_observed_at"},
		{"event_search_candidates", "credited_asset_id"},
	} {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
			)
		`, pair[0], pair[1]).Scan(&exists); err != nil {
			return fmt.Errorf("check column %s.%s: %w", pair[0], pair[1], err)
		}
		if !exists {
			return fmt.Errorf("required column %s.%s is missing", pair[0], pair[1])
		}
	}
	for _, name := range []string{"video_assets_hash_version_check", "fixtures_terminal_observation_state"} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = $1)`, name).Scan(&exists); err != nil {
			return fmt.Errorf("check constraint %s: %w", name, err)
		}
		if !exists {
			return fmt.Errorf("required constraint %s is missing", name)
		}
	}
	return nil
}

func requireRelation(ctx context.Context, tx pgx.Tx, name string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, name).Scan(&exists); err != nil {
		return fmt.Errorf("check relation %s: %w", name, err)
	}
	if !exists {
		return fmt.Errorf("required relation %s is missing", name)
	}
	return nil
}

func requireExtension(ctx context.Context, tx pgx.Tx, name string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)`, name).Scan(&exists); err != nil {
		return fmt.Errorf("check extension %s: %w", name, err)
	}
	if !exists {
		return fmt.Errorf("required extension %s is missing", name)
	}
	return nil
}
