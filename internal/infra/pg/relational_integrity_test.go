// Relational integrity tests prove correlated ownership and durable value bounds.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

func seedIntegrityEvent(t *testing.T, ctx context.Context, pool *pg.Pool, fixtureID int64, suffix string) uuid.UUID {
	t.Helper()
	eventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (
			id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, minute
		) VALUES ($1, $2, $3, 'goal', 'normal goal', 40, 'Team', 30)
	`, eventID, fixtureID, "integrity_goal_"+suffix); err != nil {
		t.Fatalf("seed event %s: %v", suffix, err)
	}
	return eventID
}

func seedIntegrityAsset(t *testing.T, ctx context.Context, pool *pg.Pool, eventID uuid.UUID, fixtureID int64, suffix string) uuid.UUID {
	t.Helper()
	assetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO video_assets (
			id, event_id, fixture_id, s3_bucket, s3_key,
			md5, frame_hashes, width, height, duration_ms, file_size_bytes
		) VALUES ($1, $2, $3, 'test', $4, $5, $6, 1280, 720, 7000, 1000000)
	`, assetID, eventID, fixtureID, suffix+".mp4", []byte("0123456789abcdef"), make([]byte, 8)); err != nil {
		t.Fatalf("seed asset %s: %v", suffix, err)
	}
	return assetID
}

func requireConstraintFailure(t *testing.T, boundary string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s write unexpectedly crossed its durable integrity boundary", boundary)
	}
}

func TestRelationalIdentityRejectsCrossOwnedRows(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	kickoff := time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC)
	first, second := makeStaging(9301, kickoff), makeStaging(9302, kickoff)
	first.Home = fixture.Team{ID: 1, Name: "First"}
	second.Home = fixture.Team{ID: 2, Name: "Second"}
	if err := fixtures.Upsert(ctx, first); err != nil {
		t.Fatalf("seed first fixture: %v", err)
	}
	if err := fixtures.Upsert(ctx, second); err != nil {
		t.Fatalf("seed second fixture: %v", err)
	}
	firstEvent := seedIntegrityEvent(t, ctx, pool, first.ID, "first")
	secondEvent := seedIntegrityEvent(t, ctx, pool, second.ID, "second")
	firstAsset := seedIntegrityAsset(t, ctx, pool, firstEvent, first.ID, "first")
	secondAsset := seedIntegrityAsset(t, ctx, pool, secondEvent, second.ID, "second")

	_, err := pool.Exec(ctx, `
		INSERT INTO video_assets (
			id, event_id, fixture_id, s3_bucket, s3_key,
			md5, frame_hashes, width, height, duration_ms, file_size_bytes
		) VALUES ($1, $2, $3, 'test', 'wrong-fixture.mp4', $4, $5, 1280, 720, 7000, 1000000)
	`, uuid.New(), firstEvent, second.ID, []byte("fedcba9876543210"), make([]byte, 8))
	requireConstraintFailure(t, "asset event/fixture", err)

	_, err = pool.Exec(ctx, `
		INSERT INTO video_shares (id, asset_id, event_id, timestamp_verified, rank)
		VALUES ('s_cross_event1', $1, $2, true, 1)
	`, firstAsset, secondEvent)
	requireConstraintFailure(t, "share asset/event", err)

	_, err = pool.Exec(ctx, `
		INSERT INTO event_search_candidates (
			event_id, fixture_id, search_attempt, query, tweet_url, video_page_url
		) VALUES ($1, $2, 1, 'q', 'https://x.com/cross/1', 'video')
	`, firstEvent, second.ID)
	requireConstraintFailure(t, "candidate event/fixture", err)

	if _, err := pool.Exec(ctx, `
		INSERT INTO event_search_candidates (
			event_id, fixture_id, search_attempt, query, tweet_url, video_page_url
		) VALUES ($1, $2, 1, 'q', 'https://x.com/cross/2', 'video')
	`, firstEvent, first.ID); err != nil {
		t.Fatalf("seed valid candidate: %v", err)
	}
	_, err = pool.Exec(ctx, `
		UPDATE event_search_candidates
		SET credited_asset_id = $2
		WHERE event_id = $1 AND tweet_url = 'https://x.com/cross/2'
	`, firstEvent, secondAsset)
	requireConstraintFailure(t, "candidate credited asset", err)

	_, err = pool.Exec(ctx, `UPDATE video_assets SET superseded_by = $2 WHERE id = $1`, firstAsset, secondAsset)
	requireConstraintFailure(t, "asset supersession", err)
}

func TestDurableValueAndStateBoundsRejectInvalidRows(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	fixtureRow := makeStaging(9310, time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC))
	if err := fixtures.Upsert(ctx, fixtureRow); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	eventID := seedIntegrityEvent(t, ctx, pool, fixtureRow.ID, "bounds")
	assetID := seedIntegrityAsset(t, ctx, pool, eventID, fixtureRow.ID, "bounds")

	_, err := pool.Exec(ctx, `
		UPDATE events SET removed = true, removed_reason = 'var' WHERE id = $1
	`, eventID)
	requireConstraintFailure(t, "event removed timestamp", err)

	_, err = pool.Exec(ctx, `
		INSERT INTO video_shares (
			id, asset_id, event_id, timestamp_verified, rank, state, removed_reason
		) VALUES ('s_bad_removed1', $1, $2, true, 1, 'removed', 'policy')
	`, assetID, eventID)
	requireConstraintFailure(t, "share removed timestamp", err)

	_, err = pool.Exec(ctx, `UPDATE video_assets SET popularity = 0 WHERE id = $1`, assetID)
	requireConstraintFailure(t, "asset popularity", err)

	_, err = pool.Exec(ctx, `UPDATE video_assets SET width = 0 WHERE id = $1`, assetID)
	requireConstraintFailure(t, "asset media shape", err)

	_, err = pool.Exec(ctx, `
		INSERT INTO event_search_candidates (
			event_id, fixture_id, search_attempt, query, tweet_url,
			video_page_url, duration_seconds
		) VALUES ($1, $2, 1, 'q', 'https://x.com/bounds/1', 'video', -1)
	`, eventID, fixtureRow.ID)
	requireConstraintFailure(t, "candidate duration", err)
}
