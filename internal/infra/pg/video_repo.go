// video_repo.go — Postgres implementations of video.AssetRepo +
// video.ShareRepo. Backs the video pipeline's asset/share storage and the
// read-derived public ordering:
// InsertAsset writes a clip the EventWorkflow already judged unique
// (ON CONFLICT (event_id, md5) for exact-dupe/retry idempotency), and
// RebalanceRanks remains only for pre-FF-066 Temporal histories. Public reads
// derive rank directly from current evidence. Follows the repo pattern (type
// over *Pool, constructor injection, pgx.ErrNoRows → domain sentinel).
package pg

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// ─── frame-hash BYTEA codec ────────────────────────────────────────────────
// frame_hashes is stored as the per-frame dHash sequence, 8 bytes big-endian
// per uint64. BYTEA (not BIGINT[]) sidesteps Postgres BIGINT being signed —
// a hash with the high bit set would overflow int64.

func encodeFrameHashes(hs []uint64) []byte {
	b := make([]byte, len(hs)*8)
	for i, h := range hs {
		binary.BigEndian.PutUint64(b[i*8:], h)
	}
	return b
}

func decodeFrameHashes(b []byte) []uint64 {
	n := len(b) / 8
	hs := make([]uint64, n)
	for i := 0; i < n; i++ {
		hs[i] = binary.BigEndian.Uint64(b[i*8:])
	}
	return hs
}

// ─── AssetRepo ─────────────────────────────────────────────────────────────

// AssetRepo backs video.AssetRepo. aspect_ratio is a generated column —
// never written, only read.
type AssetRepo struct {
	pool *Pool
}

// NewAssetRepo constructs an AssetRepo bound to pool.
func NewAssetRepo(pool *Pool) *AssetRepo { return &AssetRepo{pool: pool} }

// Column list for SELECTs. Scan order in scanAsset must match this exactly.
const assetColumns = `
	id, event_id, fixture_id,
	s3_bucket, s3_key, object_reclaimed_at,
	md5, hash_version, frame_hashes,
	width, height, duration_ms, file_size_bytes, bitrate, frame_rate,
	aspect_ratio, popularity, superseded_by, first_seen_at
`

func scanAsset(row rowScanner) (*video.Asset, error) {
	var a video.Asset
	var frameBytes []byte
	var hashVersion string
	var bitrate *int
	var supersededBy *uuid.UUID
	if err := row.Scan(
		&a.ID, &a.EventID, &a.FixtureID,
		&a.S3Bucket, &a.S3Key, &a.ObjectReclaimedAt,
		&a.MD5, &hashVersion, &frameBytes,
		&a.Width, &a.Height, &a.DurationMS, &a.FileSizeBytes, &bitrate, &a.FrameRate,
		&a.AspectRatio, &a.Popularity, &supersededBy, &a.FirstSeenAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, video.ErrNotFound
		}
		return nil, fmt.Errorf("pg.AssetRepo.scanAsset: %w", err)
	}
	a.Bitrate = bitrate
	a.SupersededBy = supersededBy
	a.FrameHashVersion = video.NormalizeFrameHashVersion(video.FrameHashVersion(hashVersion))
	a.FrameHashes = decodeFrameHashes(frameBytes)
	return &a, nil
}

// Get returns the asset by UUID or video.ErrNotFound.
func (r *AssetRepo) Get(ctx context.Context, id uuid.UUID) (*video.Asset, error) {
	return scanAsset(r.pool.QueryRow(ctx,
		"SELECT "+assetColumns+" FROM video_assets WHERE id = $1", id))
}

// InsertAsset writes a pre-judged-unique clip. ON CONFLICT (event_id, md5)
// DO NOTHING makes it idempotent on the exact layer — a retry or a
// byte-identical dupe that slipped the in-memory check never doubles a row.
func (r *AssetRepo) InsertAsset(ctx context.Context, a *video.Asset) (bool, error) {
	if err := a.ValidateInvariants(); err != nil {
		return false, fmt.Errorf("pg.AssetRepo.InsertAsset: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO video_assets (
			id, event_id, fixture_id, s3_bucket, s3_key,
			md5, hash_version, frame_hashes,
			width, height, duration_ms, file_size_bytes, bitrate, frame_rate,
			popularity, superseded_by, first_seen_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (event_id, md5) DO NOTHING
	`, a.ID, a.EventID, a.FixtureID, a.S3Bucket, a.S3Key,
		a.MD5, video.NormalizeFrameHashVersion(a.FrameHashVersion), encodeFrameHashes(a.FrameHashes),
		a.Width, a.Height, a.DurationMS, a.FileSizeBytes, a.Bitrate, a.FrameRate,
		a.Popularity, a.SupersededBy, a.FirstSeenAt)
	if err != nil {
		return false, fmt.Errorf("pg.AssetRepo.InsertAsset: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// BumpPopularity increments popularity on an existing asset (collapse persist).
func (r *AssetRepo) AddPopularity(ctx context.Context, id uuid.UUID, n int) error {
	if n < 1 {
		n = 1
	}
	tag, err := r.pool.Exec(ctx,
		"UPDATE video_assets SET popularity = popularity + $2 WHERE id = $1", id, n)
	if err != nil {
		return fmt.Errorf("pg.AssetRepo.AddPopularity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return video.ErrNotFound
	}
	return nil
}

// Supersede atomically retires loser onto winner: sets loser.superseded_by
// AND merges loser's popularity into winner, in a single statement. The loser
// CTE only fires while the loser is still live (superseded_by IS NULL), so a
// retry matches 0 rows → the outer merge's EXISTS is false → popularity is
// never double-counted. The loser row stays (shares FK it ON DELETE RESTRICT)
// but drops out of the live set — the partial index WHERE superseded_by IS NULL
// stops covering it, so the read path skips it. loser==winner is a no-op guard
// (never set an asset as its own successor).
func (r *AssetRepo) Supersede(ctx context.Context, loserID, winnerID uuid.UUID) error {
	if loserID == winnerID {
		return nil
	}
	if _, err := r.pool.Exec(ctx, `
		WITH loser AS (
			UPDATE video_assets
			SET superseded_by = $2
			WHERE id = $1 AND superseded_by IS NULL
			RETURNING popularity
		)
		UPDATE video_assets w
		SET popularity = w.popularity + (SELECT popularity FROM loser)
		WHERE w.id = $2 AND EXISTS (SELECT 1 FROM loser)
	`, loserID, winnerID); err != nil {
		return fmt.Errorf("pg.AssetRepo.Supersede: %w", err)
	}
	return nil
}

// ListUnreclaimedObjectsByEvent returns every asset whose Garage deletion has
// not yet been durably confirmed. Live and superseded assets are both covered.
func (r *AssetRepo) ListUnreclaimedObjectsByEvent(ctx context.Context, eventID uuid.UUID) ([]video.ObjectRef, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, s3_bucket, s3_key FROM video_assets
		 WHERE event_id = $1 AND object_reclaimed_at IS NULL
		 ORDER BY first_seen_at, id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("pg.AssetRepo.ListUnreclaimedObjectsByEvent: %w", err)
	}
	defer rows.Close()
	var out []video.ObjectRef
	for rows.Next() {
		var o video.ObjectRef
		if err := rows.Scan(&o.AssetID, &o.Bucket, &o.Key); err != nil {
			return nil, fmt.Errorf("pg.AssetRepo.ListUnreclaimedObjectsByEvent: scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// MarkObjectReclaimed records successful object deletion while preserving the
// first success time. A missing asset is a typed error; retries of an existing
// row are successful even when it was already marked.
func (r *AssetRepo) MarkObjectReclaimed(ctx context.Context, assetID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE video_assets
		SET object_reclaimed_at = COALESCE(object_reclaimed_at, NOW())
		WHERE id = $1
	`, assetID)
	if err != nil {
		return fmt.Errorf("pg.AssetRepo.MarkObjectReclaimed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return video.ErrNotFound
	}
	return nil
}

// ListUnreclaimedEventIDsBefore returns one work item per event with asset
// bytes outside the public fixture-date window. It does not inspect shares:
// public URL state and object-storage state are independent contracts.
func (r *AssetRepo) ListUnreclaimedEventIDsBefore(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT a.event_id
		FROM video_assets a
		JOIN fixtures f ON f.id = a.fixture_id
		WHERE a.object_reclaimed_at IS NULL
		  AND f.state = 'completed'
		  AND f.kickoff < $1
		ORDER BY a.event_id
	`, cutoff.UTC())
	if err != nil {
		return nil, fmt.Errorf("pg.AssetRepo.ListUnreclaimedEventIDsBefore: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("pg.AssetRepo.ListUnreclaimedEventIDsBefore: scan: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.AssetRepo.ListUnreclaimedEventIDsBefore: rows: %w", err)
	}
	return out, nil
}

// ─── ShareRepo ─────────────────────────────────────────────────────────────

// ShareRepo backs video.ShareRepo.
type ShareRepo struct {
	pool *Pool
}

// NewShareRepo constructs a ShareRepo bound to pool.
func NewShareRepo(pool *Pool) *ShareRepo { return &ShareRepo{pool: pool} }

const shareColumns = `
	id, asset_id, event_id,
	timestamp_verified, extracted_minute,
	state, removed_reason, removed_at,
	rank, created_at
`

func scanShare(row rowScanner) (*video.Share, error) {
	var s video.Share
	var state string
	var removedReason *string
	if err := row.Scan(
		&s.ID, &s.AssetID, &s.EventID,
		&s.TimestampVerified, &s.ExtractedMinute,
		&state, &removedReason, &s.RemovedAt,
		&s.Rank, &s.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, video.ErrNotFound
		}
		return nil, fmt.Errorf("pg.ShareRepo.scanShare: %w", err)
	}
	s.State = video.ShareState(state)
	if removedReason != nil {
		rr := video.RemovalReason(*removedReason)
		s.RemovedReason = &rr
	}
	return &s, nil
}

// Get returns the share by public ID or video.ErrNotFound.
func (r *ShareRepo) Get(ctx context.Context, id string) (*video.Share, error) {
	return scanShare(r.pool.QueryRow(ctx,
		"SELECT "+shareColumns+" FROM video_shares WHERE id = $1", id))
}

// GetByEvent returns all shares in current-evidence order. Share.Rank remains
// the stored pre-FF-066 compatibility value; public callers use
// ListLiveForEvent, which derives both order and rank.
func (r *ShareRepo) GetByEvent(ctx context.Context, eventID uuid.UUID) ([]*video.Share, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT "+shareColumns+` FROM video_shares
		 WHERE event_id = $1
		 ORDER BY
		   CASE WHEN state = 'active' THEN 0 ELSE 1 END,
		   timestamp_verified DESC,
		   (SELECT popularity FROM video_assets WHERE id = video_shares.asset_id) DESC,
		   (SELECT file_size_bytes FROM video_assets WHERE id = video_shares.asset_id) DESC,
		   created_at,
		   id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("pg.ShareRepo.GetByEvent: %w", err)
	}
	defer rows.Close()
	var out []*video.Share
	for rows.Next() {
		s, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListLiveForEvent returns the event's displayable clips and derives visibility
// plus rank from the current evidence on every read. A verified clip at the
// popularity threshold suppresses every singleton; an unverified threshold
// clip suppresses only unverified singletons. The omitted shares remain active
// and directly resolvable. Stored rank is intentionally ignored: popularity
// and active membership can change after mint, and a cached order can become
// stale when any write path misses invalidation (FF-066/FF-078).
func (r *ShareRepo) ListLiveForEvent(ctx context.Context, eventID uuid.UUID) ([]video.LiveClip, error) {
	byEvent, err := r.ListLiveForEvents(ctx, []uuid.UUID{eventID})
	if err != nil {
		return nil, err
	}
	return byEvent[eventID], nil
}

// ListLiveForEvents derives visibility and contiguous rank for several events
// in one query. Ranking and threshold evidence remain event-scoped through SQL
// partitions; the returned map omits events without public clips.
func (r *ShareRepo) ListLiveForEvents(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID][]video.LiveClip, error) {
	out := make(map[uuid.UUID][]video.LiveClip, len(eventIDs))
	if len(eventIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		WITH live AS (
			SELECT s.event_id, s.id AS share_id,
			       s.timestamp_verified, s.extracted_minute,
			       a.popularity, a.width, a.height, a.duration_ms,
			       a.file_size_bytes, s.created_at
			FROM video_shares s
			JOIN video_assets a ON a.id = s.asset_id
			WHERE s.event_id = ANY($1::uuid[])
			  AND s.state = 'active'
			  AND a.superseded_by IS NULL
			  AND a.object_reclaimed_at IS NULL
		), evidence AS (
			SELECT event_id,
				COALESCE(BOOL_OR(timestamp_verified AND popularity >= $2), FALSE)
					AS has_verified_threshold,
				COALESCE(BOOL_OR(NOT timestamp_verified AND popularity >= $2), FALSE)
					AS has_unverified_threshold
			FROM live
			GROUP BY event_id
		), visible AS (
			SELECT live.*
			FROM live
			JOIN evidence USING (event_id)
			WHERE live.popularity <> $3
			   OR NOT (
				   evidence.has_verified_threshold
				   OR (NOT live.timestamp_verified AND evidence.has_unverified_threshold)
			   )
		), ranked AS (
			SELECT event_id, share_id,
			       ROW_NUMBER() OVER (
			           PARTITION BY event_id
				   ORDER BY timestamp_verified DESC,
				            popularity DESC,
				            file_size_bytes DESC,
				            created_at,
				            share_id
			       )::int AS rank,
			       timestamp_verified, extracted_minute,
			       popularity, width, height, duration_ms
			FROM visible
		)
		SELECT event_id, share_id, rank, timestamp_verified, extracted_minute,
		       popularity, width, height, duration_ms
		FROM ranked
		ORDER BY array_position($1::uuid[], event_id), rank`, eventIDs, video.PublicVisibilityPopularityThreshold,
		video.PublicVisibilitySingletonPopularity)
	if err != nil {
		return nil, fmt.Errorf("pg.ShareRepo.ListLiveForEvents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventID uuid.UUID
		var c video.LiveClip
		if err := rows.Scan(&eventID, &c.ShareID, &c.Rank, &c.Verified, &c.ExtractedMinute,
			&c.Popularity, &c.Width, &c.Height, &c.DurationMS); err != nil {
			return nil, fmt.Errorf("pg.ShareRepo.ListLiveForEvents: scan: %w", err)
		}
		out[eventID] = append(out[eventID], c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.ShareRepo.ListLiveForEvents: rows: %w", err)
	}
	return out, nil
}

// ResolveShare follows share_id → asset → superseded_by chain → live asset via
// a recursive CTE, returning the share's State + the live asset's (bucket, key)
// — the /videos/{share_id} redirect's 302 target (#167). ErrNotFound when the
// share id was never minted (→ 404). A superseded share still resolves (URL
// stability): the anchor asset is found via the share, then the chain walks
// superseded_by to the single live asset. A reclaimed terminal object resolves
// as removed even if a concurrent placement has not yet had its share state
// swept; the redirect must never presign bytes known to be gone. depth<64
// guards a pathological cycle.
func (r *ShareRepo) ResolveShare(ctx context.Context, id string) (video.ResolvedShare, error) {
	var rs video.ResolvedShare
	var state string
	err := r.pool.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
			SELECT a.id, a.superseded_by, a.s3_bucket, a.s3_key,
			       a.object_reclaimed_at, 0 AS depth
			FROM video_assets a
			WHERE a.id = (SELECT asset_id FROM video_shares WHERE id = $1)
			UNION ALL
			SELECT a.id, a.superseded_by, a.s3_bucket, a.s3_key,
			       a.object_reclaimed_at, c.depth + 1
			FROM chain c JOIN video_assets a ON a.id = c.superseded_by
			WHERE c.depth < 64
		)
		SELECT CASE
		         WHEN c.object_reclaimed_at IS NOT NULL THEN 'removed'::share_state
		         ELSE (SELECT state FROM video_shares WHERE id = $1)
		       END,
		       c.s3_bucket, c.s3_key
		FROM chain c
		WHERE c.superseded_by IS NULL
		LIMIT 1`, id).Scan(&state, &rs.Bucket, &rs.Key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rs, video.ErrNotFound
		}
		return rs, fmt.Errorf("pg.ShareRepo.ResolveShare: %w", err)
	}
	rs.State = video.ShareState(state)
	return rs, nil
}

// Insert creates a new share. A rank collision with an active share in the
// same event violates the partial UNIQUE index and errors here.
func (r *ShareRepo) Insert(ctx context.Context, s *video.Share) error {
	if err := s.ValidateInvariants(); err != nil {
		return fmt.Errorf("pg.ShareRepo.Insert: %w", err)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO video_shares (
			id, asset_id, event_id, timestamp_verified, extracted_minute,
			state, removed_reason, removed_at, rank, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, s.ID, s.AssetID, s.EventID, s.TimestampVerified, s.ExtractedMinute,
		string(s.State), removalReasonPtr(s.RemovedReason), s.RemovedAt, s.Rank, s.CreatedAt)
	if err != nil {
		return fmt.Errorf("pg.ShareRepo.Insert: %w", err)
	}
	return nil
}

// Upsert saves state changes (Remove) back. ON CONFLICT (id) updates the
// mutable state fields.
func (r *ShareRepo) Upsert(ctx context.Context, s *video.Share) error {
	if err := s.ValidateInvariants(); err != nil {
		return fmt.Errorf("pg.ShareRepo.Upsert: %w", err)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO video_shares (
			id, asset_id, event_id, timestamp_verified, extracted_minute,
			state, removed_reason, removed_at, rank, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE SET
			state = EXCLUDED.state,
			removed_reason = EXCLUDED.removed_reason,
			removed_at = EXCLUDED.removed_at,
			rank = EXCLUDED.rank,
			extracted_minute = EXCLUDED.extracted_minute,
			timestamp_verified = EXCLUDED.timestamp_verified
	`, s.ID, s.AssetID, s.EventID, s.TimestampVerified, s.ExtractedMinute,
		string(s.State), removalReasonPtr(s.RemovedReason), s.RemovedAt, s.Rank, s.CreatedAt)
	if err != nil {
		return fmt.Errorf("pg.ShareRepo.Upsert: %w", err)
	}
	return nil
}

// MarkSuperseded flips a share to state='superseded'. It leaves the active
// pool (RebalanceRanks + the read list both filter WHERE state='active'),
// freeing its rank slot, but its row and asset_id FK remain so a direct
// s_<hex> URL still resolves through the asset chain. No-op-safe: a second
// call on an already-superseded share affects 0 rows and returns nil.
func (r *ShareRepo) MarkSuperseded(ctx context.Context, id string) error {
	// Guard on state='active' so a 'removed' (VAR) share is never clobbered
	// into 'superseded' — removal is terminal. 0 rows (missing / already
	// superseded on retry / removed) is swallowed: the share is simply no
	// longer active, which is the desired end state.
	if _, err := r.pool.Exec(ctx,
		"UPDATE video_shares SET state = 'superseded' WHERE id = $1 AND state = 'active'", id); err != nil {
		return fmt.Errorf("pg.ShareRepo.MarkSuperseded: %w", err)
	}
	return nil
}

// RemoveByEvent revokes all of an event's non-removed shares (active +
// superseded) to state='removed' with reason — the VAR/destroy path (#172).
// Idempotent: already-removed shares are untouched. now() stamps removed_at,
// satisfying the CHECK (removed ⇒ reason + removed_at present).
func (r *ShareRepo) RemoveByEvent(ctx context.Context, eventID uuid.UUID, reason video.RemovalReason) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE video_shares
		SET state = 'removed', removed_reason = $2, removed_at = now()
		WHERE event_id = $1 AND state <> 'removed'`, eventID, string(reason)); err != nil {
		return fmt.Errorf("pg.ShareRepo.RemoveByEvent: %w", err)
	}
	return nil
}

// rankItem pairs a share with the subset of its asset needed for ranking.
type rankItem struct {
	share *video.Share
	asset *video.Asset
}

// RebalanceRanks reads the event's active shares, sorts them via
// CompareShares, and rewrites rank 1..N in one transaction. The partial
// UNIQUE (event_id, rank) WHERE state='active' forbids two active shares
// sharing a rank even mid-rewrite, so we first shift every rank out of the
// 1..N band (a constant offset keeps them distinct) before assigning finals.
func (r *ShareRepo) RebalanceRanks(ctx context.Context, eventID uuid.UUID) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("pg.ShareRepo.RebalanceRanks: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT
			s.id, s.asset_id, s.event_id, s.timestamp_verified, s.extracted_minute,
			s.state, s.removed_reason, s.removed_at, s.rank, s.created_at,
			a.popularity, a.file_size_bytes
		FROM video_shares s
		JOIN video_assets a ON a.id = s.asset_id
		WHERE s.event_id = $1 AND s.state = 'active'
	`, eventID)
	if err != nil {
		return 0, fmt.Errorf("pg.ShareRepo.RebalanceRanks: query: %w", err)
	}
	var items []rankItem
	for rows.Next() {
		var s video.Share
		var a video.Asset
		var state string
		var removedReason *string
		if err := rows.Scan(
			&s.ID, &s.AssetID, &s.EventID, &s.TimestampVerified, &s.ExtractedMinute,
			&state, &removedReason, &s.RemovedAt, &s.Rank, &s.CreatedAt,
			&a.Popularity, &a.FileSizeBytes,
		); err != nil {
			rows.Close()
			return 0, fmt.Errorf("pg.ShareRepo.RebalanceRanks: scan: %w", err)
		}
		s.State = video.ShareState(state)
		items = append(items, rankItem{share: &s, asset: &a})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("pg.ShareRepo.RebalanceRanks: rows: %w", err)
	}
	if len(items) == 0 {
		return 0, nil
	}

	sort.SliceStable(items, func(i, j int) bool {
		return video.CompareShares(items[i].share, items[j].share, items[i].asset, items[j].asset) < 0
	})

	// Shift all active ranks out of the target band so the finals are free.
	const rankOffset = 1_000_000
	if _, err := tx.Exec(ctx,
		"UPDATE video_shares SET rank = rank + $2 WHERE event_id = $1 AND state = 'active'",
		eventID, rankOffset); err != nil {
		return 0, fmt.Errorf("pg.ShareRepo.RebalanceRanks: shift: %w", err)
	}

	repositioned := 0
	for i, it := range items {
		newRank := i + 1
		if it.share.Rank != newRank { // compare to the pre-shift original
			repositioned++
		}
		if _, err := tx.Exec(ctx,
			"UPDATE video_shares SET rank = $2 WHERE id = $1", it.share.ID, newRank); err != nil {
			return 0, fmt.Errorf("pg.ShareRepo.RebalanceRanks: assign: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("pg.ShareRepo.RebalanceRanks: commit: %w", err)
	}
	return repositioned, nil
}

// removalReasonPtr converts the domain enum pointer to a string pointer for
// pgx (nil → SQL NULL).
func removalReasonPtr(r *video.RemovalReason) *string {
	if r == nil {
		return nil
	}
	s := string(*r)
	return &s
}
