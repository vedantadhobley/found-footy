// candidate_repo.go — Event-serialized candidate observation and terminal writes.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
)

// CandidateRepo owns candidate writes that must serialize with event removal.
// The event row is the shared lock boundary used by observation, terminal
// persistence, replay preparation, accepted placement, and VAR removal.
type CandidateRepo struct {
	pool *Pool
}

// NewCandidateRepo constructs a candidate store over one Postgres pool.
func NewCandidateRepo(pool *Pool) *CandidateRepo { return &CandidateRepo{pool: pool} }

// Observe inserts immutable candidate evidence. A post-removal observation is
// retained but commits directly as rejected/event_removed, never pending.
func (r *CandidateRepo) Observe(
	ctx context.Context,
	evidence discoverycontract.CandidateEvidence,
) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("pg.CandidateRepo.Observe: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	removed, err := lockCandidateEvent(ctx, tx, evidence.EventID, evidence.FixtureID)
	if err != nil {
		return false, fmt.Errorf("pg.CandidateRepo.Observe: %w", err)
	}
	inserted, err := insertCandidateObservation(ctx, tx, evidence)
	if err != nil {
		return false, fmt.Errorf("pg.CandidateRepo.Observe: %w", err)
	}
	if removed {
		if _, err := terminalizePendingCandidatesForRemovedEvent(
			ctx, tx, evidence.EventID, evidence.TweetURL, time.Now().UTC(),
		); err != nil {
			return false, fmt.Errorf("pg.CandidateRepo.Observe: terminalize removed candidate: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("pg.CandidateRepo.Observe: commit: %w", err)
	}
	return inserted, nil
}

// Complete atomically creates missing observation evidence or updates the
// existing row with one terminal outcome. When removal owns the event first,
// only a still-pending row changes and event_removed remains authoritative.
func (r *CandidateRepo) Complete(
	ctx context.Context,
	evidence discoverycontract.CandidateEvidence,
	outcome discoverycontract.CandidateOutcome,
	rejectReason string,
	detail json.RawMessage,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg.CandidateRepo.Complete: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	removed, err := lockCandidateEvent(ctx, tx, evidence.EventID, evidence.FixtureID)
	if err != nil {
		return fmt.Errorf("pg.CandidateRepo.Complete: %w", err)
	}
	if _, err := insertCandidateObservation(ctx, tx, evidence); err != nil {
		return fmt.Errorf("pg.CandidateRepo.Complete: %w", err)
	}
	if removed {
		if _, err := terminalizePendingCandidatesForRemovedEvent(
			ctx, tx, evidence.EventID, evidence.TweetURL, time.Now().UTC(),
		); err != nil {
			return fmt.Errorf("pg.CandidateRepo.Complete: terminalize removed candidate: %w", err)
		}
	} else {
		updated, err := updateCandidateTerminalOutcome(
			ctx, tx, evidence.EventID, evidence.TweetURL, outcome, rejectReason, detail,
		)
		if err != nil {
			return fmt.Errorf("pg.CandidateRepo.Complete: %w", err)
		}
		if !updated {
			return fmt.Errorf("pg.CandidateRepo.Complete: candidate %s is missing", evidence.TweetURL)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg.CandidateRepo.Complete: commit: %w", err)
	}
	return nil
}

// CompleteLegacy preserves the old update-only activity shape while applying
// the same event-removal serialization. Missing rows remain a compatibility
// no-op because this payload does not carry enough evidence to create one.
func (r *CandidateRepo) CompleteLegacy(
	ctx context.Context,
	eventID uuid.UUID,
	tweetURL string,
	outcome discoverycontract.CandidateOutcome,
	rejectReason string,
	detail json.RawMessage,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg.CandidateRepo.CompleteLegacy: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	removed, err := lockCandidateEvent(ctx, tx, eventID, 0)
	if err != nil {
		return fmt.Errorf("pg.CandidateRepo.CompleteLegacy: %w", err)
	}
	if removed {
		if _, err := terminalizePendingCandidatesForRemovedEvent(
			ctx, tx, eventID, tweetURL, time.Now().UTC(),
		); err != nil {
			return fmt.Errorf("pg.CandidateRepo.CompleteLegacy: terminalize removed candidate: %w", err)
		}
	} else {
		if _, err := updateCandidateTerminalOutcome(
			ctx, tx, eventID, tweetURL, outcome, rejectReason, detail,
		); err != nil {
			return fmt.Errorf("pg.CandidateRepo.CompleteLegacy: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg.CandidateRepo.CompleteLegacy: commit: %w", err)
	}
	return nil
}

// lockCandidateEvent serializes a candidate mutation with removal and
// placement. fixtureID zero selects the compatibility event-only identity.
func lockCandidateEvent(
	ctx context.Context,
	tx pgx.Tx,
	eventID uuid.UUID,
	fixtureID int64,
) (bool, error) {
	var removed bool
	var err error
	if fixtureID > 0 {
		err = tx.QueryRow(ctx, `
			SELECT removed FROM events
			WHERE id = $1 AND fixture_id = $2
			FOR UPDATE
		`, eventID, fixtureID).Scan(&removed)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT removed FROM events WHERE id = $1 FOR UPDATE
		`, eventID).Scan(&removed)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("event/fixture identity not found")
	}
	if err != nil {
		return false, fmt.Errorf("lock event: %w", err)
	}
	return removed, nil
}

// insertCandidateObservation writes immutable first-observation evidence and
// reports whether this transaction created the row.
func insertCandidateObservation(
	ctx context.Context,
	tx pgx.Tx,
	evidence discoverycontract.CandidateEvidence,
) (bool, error) {
	var age *float64
	if evidence.AgeMinutesAtDiscovery > 0 {
		age = &evidence.AgeMinutesAtDiscovery
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO event_search_candidates (
			event_id, fixture_id, search_attempt, query,
			tweet_url, tweet_text, video_page_url, duration_seconds,
			username, age_minutes_at_discovery
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (event_id, tweet_url) DO NOTHING
	`, evidence.EventID, evidence.FixtureID, evidence.SearchAttempt, evidence.Query,
		evidence.TweetURL, evidence.TweetText, evidence.VideoPageURL,
		evidence.DurationSeconds, evidence.Username, age)
	if err != nil {
		return false, fmt.Errorf("insert candidate %s: %w", evidence.TweetURL, err)
	}
	return tag.RowsAffected() == 1, nil
}

// updateCandidateTerminalOutcome applies the FF-034 idempotent terminal
// result while retaining replay metadata owned by historical repair runs.
func updateCandidateTerminalOutcome(
	ctx context.Context,
	tx pgx.Tx,
	eventID uuid.UUID,
	tweetURL string,
	outcome discoverycontract.CandidateOutcome,
	rejectReason string,
	detail json.RawMessage,
) (bool, error) {
	var reason *string
	if rejectReason != "" {
		reason = &rejectReason
	}
	var rawDetail []byte
	if len(detail) > 0 {
		rawDetail = detail
	}
	tag, err := tx.Exec(ctx, `
		UPDATE event_search_candidates
		SET outcome_class = $3,
		    reject_reason = $4,
		    outcome_detail = CASE
		        WHEN outcome_detail ? 'replay'
		        THEN COALESCE(NULLIF($5::jsonb, 'null'::jsonb), '{}'::jsonb) ||
		             jsonb_build_object('replay', outcome_detail->'replay')
		        ELSE $5::jsonb
		    END,
		    outcome_at = CASE
		        WHEN outcome_class = $3
		         AND reject_reason IS NOT DISTINCT FROM $4
		         AND outcome_detail IS NOT DISTINCT FROM CASE
		             WHEN outcome_detail ? 'replay'
		             THEN COALESCE(NULLIF($5::jsonb, 'null'::jsonb), '{}'::jsonb) ||
		                  jsonb_build_object('replay', outcome_detail->'replay')
		             ELSE $5::jsonb
		         END
		        THEN COALESCE(outcome_at, NOW())
		        ELSE NOW()
		    END
		WHERE event_id = $1 AND tweet_url = $2
	`, eventID, tweetURL, string(outcome), reason, rawDetail)
	if err != nil {
		return false, fmt.Errorf("update candidate %s: %w", tweetURL, err)
	}
	return tag.RowsAffected() == 1, nil
}

// terminalizePendingCandidatesForRemovedEvent closes either one URL or every
// pending row for an event. Terminal outcomes that committed before removal
// remain immutable, and replay evidence survives under previous_detail.
func terminalizePendingCandidatesForRemovedEvent(
	ctx context.Context,
	tx pgx.Tx,
	eventID uuid.UUID,
	tweetURL string,
	at time.Time,
) (int64, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	tag, err := tx.Exec(ctx, `
		UPDATE event_search_candidates
		SET outcome_class = 'rejected',
		    reject_reason = $4,
		    outcome_detail = CASE
		        WHEN outcome_detail IS NULL THEN '{}'::jsonb
		        WHEN jsonb_typeof(outcome_detail) = 'object' THEN outcome_detail
		        ELSE jsonb_build_object('previous_detail', outcome_detail)
		    END || jsonb_build_object('reason', $4::text),
		    outcome_at = $3,
		    credited_asset_id = NULL
		WHERE event_id = $1
		  AND outcome_class = 'pending'
		  AND ($2 = '' OR tweet_url = $2)
	`, eventID, tweetURL, at, discoverycontract.RejectReasonEventRemoved)
	if err != nil {
		return 0, fmt.Errorf("terminalize pending candidates: %w", err)
	}
	return tag.RowsAffected(), nil
}
