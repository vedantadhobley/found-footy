// Atomic accepted-candidate placement across video, share, attribution, and
// supersession rows. Public rank is derived by the read query, never written.
package pg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// PlacementRepo implements video.PlacementRepo over one Postgres pool.
type PlacementRepo struct {
	pool *Pool
}

// NewPlacementRepo constructs the atomic accepted-candidate store.
func NewPlacementRepo(pool *Pool) *PlacementRepo { return &PlacementRepo{pool: pool} }

// CommitClipPlacement applies one complete accepted-candidate decision. The
// event row lock serializes placement/recovery executions for one event. Every
// retry either observes the prior transaction or applies all mutations once.
func (r *PlacementRepo) CommitClipPlacement(ctx context.Context, in video.ClipPlacement) (video.ClipPlacementResult, error) {
	var out video.ClipPlacementResult
	in.CommittedAt = placementNow(in.CommittedAt)
	if err := validateClipPlacement(in); err != nil {
		return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked bool
	if err := tx.QueryRow(ctx, `
		SELECT true FROM events WHERE id = $1 AND fixture_id = $2 FOR UPDATE
	`, in.EventID, in.FixtureID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: event/fixture identity not found")
		}
		return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: lock event: %w", err)
	}

	winnerID := in.WinnerAssetID
	winnerCreated := false
	if in.Winner != nil {
		winnerID = in.Winner.ID
		tag, err := tx.Exec(ctx, `
			INSERT INTO video_assets (
				id, event_id, fixture_id, s3_bucket, s3_key,
				md5, hash_version, frame_hashes,
				width, height, duration_ms, file_size_bytes, bitrate,
				popularity, superseded_by, first_seen_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,0,NULL,$14)
			ON CONFLICT (event_id, md5) DO NOTHING
		`, in.Winner.ID, in.Winner.EventID, in.Winner.FixtureID,
			in.Winner.S3Bucket, in.Winner.S3Key, in.Winner.MD5,
			video.NormalizeFrameHashVersion(in.Winner.FrameHashVersion), encodeFrameHashes(in.Winner.FrameHashes),
			in.Winner.Width, in.Winner.Height, in.Winner.DurationMS,
			in.Winner.FileSizeBytes, in.Winner.Bitrate, in.Winner.FirstSeenAt)
		if err != nil {
			return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: insert winner: %w", err)
		}
		winnerCreated = tag.RowsAffected() == 1
	}

	winner, err := getAssetTx(ctx, tx, winnerID, true)
	if err != nil {
		return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: winner: %w", err)
	}
	if winner.EventID != in.EventID || winner.FixtureID != in.FixtureID || winner.SupersededBy != nil {
		return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: winner is not live in event")
	}
	if in.Winner != nil && !samePlacementAsset(winner, in.Winner) {
		return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: deterministic winner identity mismatch")
	}

	shareID, err := ensurePlacementShare(ctx, tx, in, winnerID)
	if err != nil {
		return out, err
	}

	loserSet := make(map[uuid.UUID]struct{}, len(in.LoserAssetIDs))
	losers := append([]uuid.UUID(nil), in.LoserAssetIDs...)
	sort.Slice(losers, func(i, j int) bool { return losers[i].String() < losers[j].String() })
	for _, loserID := range losers {
		if loserID != uuid.Nil && loserID != winnerID {
			loserSet[loserID] = struct{}{}
		}
	}

	addedVotes := 0
	for _, candidate := range in.Candidates {
		added, err := creditPlacementCandidate(ctx, tx, in, winnerID, loserSet, candidate)
		if err != nil {
			return out, err
		}
		if added {
			addedVotes++
		}
	}
	if addedVotes > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE video_assets SET popularity = popularity + $2 WHERE id = $1
		`, winnerID, addedVotes); err != nil {
			return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: add popularity: %w", err)
		}
	}
	if winnerCreated && addedVotes == 0 {
		return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: new winner received no candidate vote")
	}

	seenLoser := make(map[uuid.UUID]struct{}, len(loserSet))
	for _, loserID := range losers {
		if _, ok := loserSet[loserID]; !ok {
			continue
		}
		if _, duplicate := seenLoser[loserID]; duplicate {
			continue
		}
		seenLoser[loserID] = struct{}{}
		object, err := supersedePlacementLoser(ctx, tx, in.EventID, loserID, winnerID)
		if err != nil {
			return out, err
		}
		out.LoserObjects = append(out.LoserObjects, object)
	}

	if err := tx.Commit(ctx); err != nil {
		return out, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: commit: %w", err)
	}
	out.WinnerAssetID = winnerID
	out.ShareID = shareID
	out.WinnerCreated = winnerCreated
	return out, nil
}

func validateClipPlacement(in video.ClipPlacement) error {
	if in.EventID == uuid.Nil || in.FixtureID <= 0 || len(in.Candidates) == 0 {
		return fmt.Errorf("incomplete event, fixture, or candidate set")
	}
	if (in.Winner == nil) == (in.WinnerAssetID == uuid.Nil) {
		return fmt.Errorf("exactly one new or existing winner is required")
	}
	if in.Winner != nil && (in.Winner.ID == uuid.Nil || in.Winner.EventID != in.EventID || in.Winner.FixtureID != in.FixtureID) {
		return fmt.Errorf("new winner identity does not match placement")
	}
	for _, c := range in.Candidates {
		if c.Evidence.EventID != in.EventID || c.Evidence.FixtureID != in.FixtureID ||
			c.Evidence.SearchAttempt <= 0 || c.Evidence.Query == "" || c.Evidence.TweetURL == "" {
			return fmt.Errorf("candidate evidence is incomplete for %q", c.Evidence.TweetURL)
		}
		if c.Outcome != discoverycontract.OutcomePromoted && c.Outcome != discoverycontract.OutcomeDuplicate {
			return fmt.Errorf("placement candidate outcome %q is not accepted", c.Outcome)
		}
	}
	return nil
}

func getAssetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, lock bool) (*video.Asset, error) {
	query := "SELECT " + assetColumns + " FROM video_assets WHERE id = $1"
	if lock {
		query += " FOR UPDATE"
	}
	return scanAsset(tx.QueryRow(ctx, query, id))
}

func samePlacementAsset(got, want *video.Asset) bool {
	return got.ID == want.ID && got.EventID == want.EventID && got.FixtureID == want.FixtureID &&
		got.S3Bucket == want.S3Bucket && got.S3Key == want.S3Key && bytes.Equal(got.MD5, want.MD5) &&
		got.FrameHashVersion == video.NormalizeFrameHashVersion(want.FrameHashVersion) &&
		bytes.Equal(encodeFrameHashes(got.FrameHashes), encodeFrameHashes(want.FrameHashes)) &&
		got.Width == want.Width && got.Height == want.Height && got.DurationMS == want.DurationMS &&
		got.FileSizeBytes == want.FileSizeBytes
}

func ensurePlacementShare(ctx context.Context, tx pgx.Tx, in video.ClipPlacement, winnerID uuid.UUID) (string, error) {
	var shareID, state string
	err := tx.QueryRow(ctx, `
		SELECT id, state::text FROM video_shares WHERE event_id = $1 AND asset_id = $2
	`, in.EventID, winnerID).Scan(&shareID, &state)
	switch {
	case err == nil:
		if state != string(video.ShareStateActive) {
			return "", fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: winner share is %s", state)
		}
		return shareID, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: find share: %w", err)
	}

	var rank int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(rank), 0) + 1 FROM video_shares
		WHERE event_id = $1 AND state = 'active'
	`, in.EventID).Scan(&rank); err != nil {
		return "", fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: next compatibility rank: %w", err)
	}
	share, err := video.NewShare(winnerID, in.EventID, in.Verified, in.ExtractedMinute, rank, in.CommittedAt)
	if err != nil {
		return "", fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: new share: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO video_shares (
			id, asset_id, event_id, timestamp_verified, extracted_minute,
			state, removed_reason, removed_at, rank, created_at
		) VALUES ($1,$2,$3,$4,$5,'active',NULL,NULL,$6,$7)
		ON CONFLICT (event_id, asset_id) DO NOTHING
	`, share.ID, winnerID, in.EventID, in.Verified, in.ExtractedMinute, rank, in.CommittedAt); err != nil {
		return "", fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: insert share: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT id, state::text FROM video_shares WHERE event_id = $1 AND asset_id = $2
	`, in.EventID, winnerID).Scan(&shareID, &state); err != nil {
		return "", fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: read share: %w", err)
	}
	if state != string(video.ShareStateActive) {
		return "", fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: inserted winner share is %s", state)
	}
	return shareID, nil
}

func creditPlacementCandidate(
	ctx context.Context,
	tx pgx.Tx,
	in video.ClipPlacement,
	winnerID uuid.UUID,
	losers map[uuid.UUID]struct{},
	candidate video.PlacementCandidate,
) (bool, error) {
	e := candidate.Evidence
	var priorFixtureID int64
	var priorOutcome string
	var priorCredit *uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT fixture_id, outcome_class, credited_asset_id
		FROM event_search_candidates
		WHERE event_id = $1 AND tweet_url = $2
		FOR UPDATE
	`, in.EventID, e.TweetURL).Scan(&priorFixtureID, &priorOutcome, &priorCredit)

	var age *float64
	if e.AgeMinutesAtDiscovery > 0 {
		age = &e.AgeMinutesAtDiscovery
	}
	detail := []byte(candidate.Detail)
	if len(detail) == 0 {
		detail = nil
	}

	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO event_search_candidates (
				event_id, fixture_id, search_attempt, query,
				tweet_url, tweet_text, video_page_url, duration_seconds,
				username, age_minutes_at_discovery,
				outcome_class, reject_reason, outcome_detail, outcome_at,
				credited_asset_id
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULL,$12,NOW(),$13)
		`, e.EventID, e.FixtureID, e.SearchAttempt, e.Query, e.TweetURL,
			e.TweetText, e.VideoPageURL, e.DurationSeconds, e.Username, age,
			string(candidate.Outcome), detail, winnerID); err != nil {
			return false, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: insert candidate %s: %w", e.TweetURL, err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: lock candidate %s: %w", e.TweetURL, err)
	}
	if priorFixtureID != in.FixtureID {
		return false, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: candidate fixture mismatch for %s", e.TweetURL)
	}

	added := false
	if priorCredit == nil {
		added = !discoverycontract.CandidateOutcome(priorOutcome).Credited()
	} else if *priorCredit != winnerID {
		if _, ok := losers[*priorCredit]; !ok {
			return false, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: candidate %s already credited to %s", e.TweetURL, *priorCredit)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE event_search_candidates
		SET outcome_class = $3,
		    reject_reason = NULL,
		    outcome_detail = CASE
		        WHEN outcome_detail ? 'replay'
		        THEN COALESCE(NULLIF($4::jsonb, 'null'::jsonb), '{}'::jsonb) ||
		             jsonb_build_object('replay', outcome_detail->'replay')
		        ELSE $4::jsonb
		    END,
		    outcome_at = CASE
		        WHEN outcome_class = $3 AND credited_asset_id = $5
		        THEN COALESCE(outcome_at, NOW())
		        ELSE NOW()
		    END,
		    credited_asset_id = $5
		WHERE event_id = $1 AND tweet_url = $2
	`, in.EventID, e.TweetURL, string(candidate.Outcome), detail, winnerID); err != nil {
		return false, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: update candidate %s: %w", e.TweetURL, err)
	}
	return added, nil
}

func supersedePlacementLoser(
	ctx context.Context,
	tx pgx.Tx,
	eventID, loserID, winnerID uuid.UUID,
) (video.ObjectRef, error) {
	var object video.ObjectRef
	var loserEventID uuid.UUID
	var popularity int
	var supersededBy *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT event_id, s3_bucket, s3_key, popularity, superseded_by
		FROM video_assets WHERE id = $1 FOR UPDATE
	`, loserID).Scan(&loserEventID, &object.Bucket, &object.Key, &popularity, &supersededBy); err != nil {
		return object, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: lock loser %s: %w", loserID, err)
	}
	if loserEventID != eventID {
		return object, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: loser %s belongs to another event", loserID)
	}
	if supersededBy != nil && *supersededBy != winnerID {
		return object, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: loser %s already points to %s", loserID, *supersededBy)
	}
	if supersededBy == nil {
		if _, err := tx.Exec(ctx, `
			UPDATE video_assets SET popularity = popularity + $2 WHERE id = $1
		`, winnerID, popularity); err != nil {
			return object, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: merge loser %s: %w", loserID, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE video_assets SET superseded_by = $2 WHERE id = $1
		`, loserID, winnerID); err != nil {
			return object, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: retire loser %s: %w", loserID, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE video_shares SET state = 'superseded'
		WHERE asset_id = $1 AND state = 'active'
	`, loserID); err != nil {
		return object, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: retire loser share %s: %w", loserID, err)
	}
	winnerDetail, _ := json.Marshal(map[string]string{"winner_asset_id": winnerID.String()})
	if _, err := tx.Exec(ctx, `
		UPDATE event_search_candidates
		SET credited_asset_id = $2,
		    outcome_class = CASE WHEN outcome_class = 'promoted' THEN 'superseded' ELSE outcome_class END,
		    outcome_detail = CASE
		        WHEN outcome_detail ? 'replay'
		        THEN $3::jsonb || jsonb_build_object('replay', outcome_detail->'replay')
		        ELSE $3::jsonb
		    END,
		    outcome_at = CASE WHEN outcome_class = 'promoted' THEN NOW() ELSE outcome_at END
		WHERE credited_asset_id = $1
	`, loserID, winnerID, winnerDetail); err != nil {
		return object, fmt.Errorf("pg.PlacementRepo.CommitClipPlacement: move loser credits %s: %w", loserID, err)
	}
	return object, nil
}

// placementNow supplies a non-zero UTC timestamp to callers that construct a
// placement outside a workflow activity test.
func placementNow(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}
