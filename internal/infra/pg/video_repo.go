// video_repo.go — Postgres implementations of video.AssetRepo +
// video.ShareRepo. Backs the video pipeline's promote/insert/rank step:
// InsertAsset writes a clip the EventWorkflow already judged unique
// (ON CONFLICT (event_id, md5) for exact-dupe/retry idempotency), and
// RebalanceRanks rewrites the per-event share order atomically via
// CompareShares. Follows the FixtureRepo/EventRepo pattern (dedicated type
// over *Pool, constructor injection, pgx.ErrNoRows → domain sentinel).
package pg

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

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
	s3_bucket, s3_key,
	md5, frame_hashes,
	width, height, duration_ms, file_size_bytes, bitrate,
	aspect_ratio, popularity, superseded_by, first_seen_at
`

func scanAsset(row rowScanner) (*video.Asset, error) {
	var a video.Asset
	var frameBytes []byte
	var bitrate *int
	var supersededBy *uuid.UUID
	if err := row.Scan(
		&a.ID, &a.EventID, &a.FixtureID,
		&a.S3Bucket, &a.S3Key,
		&a.MD5, &frameBytes,
		&a.Width, &a.Height, &a.DurationMS, &a.FileSizeBytes, &bitrate,
		&a.AspectRatio, &a.Popularity, &supersededBy, &a.FirstSeenAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, video.ErrNotFound
		}
		return nil, fmt.Errorf("pg.AssetRepo.scanAsset: %w", err)
	}
	a.Bitrate = bitrate
	a.SupersededBy = supersededBy
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
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO video_assets (
			id, event_id, fixture_id, s3_bucket, s3_key,
			md5, frame_hashes,
			width, height, duration_ms, file_size_bytes, bitrate,
			popularity, superseded_by, first_seen_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (event_id, md5) DO NOTHING
	`, a.ID, a.EventID, a.FixtureID, a.S3Bucket, a.S3Key,
		a.MD5, encodeFrameHashes(a.FrameHashes),
		a.Width, a.Height, a.DurationMS, a.FileSizeBytes, a.Bitrate,
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

// ListObjectKeysByEvent returns the (bucket, key) of every asset for an event
// (live + superseded) — the destroy/reclaim path (#172/#176).
func (r *AssetRepo) ListObjectKeysByEvent(ctx context.Context, eventID uuid.UUID) ([]video.ObjectRef, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT s3_bucket, s3_key FROM video_assets WHERE event_id = $1", eventID)
	if err != nil {
		return nil, fmt.Errorf("pg.AssetRepo.ListObjectKeysByEvent: %w", err)
	}
	defer rows.Close()
	var out []video.ObjectRef
	for rows.Next() {
		var o video.ObjectRef
		if err := rows.Scan(&o.Bucket, &o.Key); err != nil {
			return nil, fmt.Errorf("pg.AssetRepo.ListObjectKeysByEvent: scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
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

// GetByEvent returns all shares for an event, rank-ordered.
func (r *ShareRepo) GetByEvent(ctx context.Context, eventID uuid.UUID) ([]*video.Share, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT "+shareColumns+" FROM video_shares WHERE event_id = $1 ORDER BY rank", eventID)
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

// ListLiveForEvent returns the event's displayable clips — active shares joined
// to their LIVE assets (superseded_by IS NULL) — rank-ordered (#167). This is
// the read model the API's videos array is built from; superseded/removed
// shares are excluded (their old URLs still resolve via ResolveShare). Uses the
// video_shares state='active' + video_assets superseded_by IS NULL indexes.
func (r *ShareRepo) ListLiveForEvent(ctx context.Context, eventID uuid.UUID) ([]video.LiveClip, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.id, s.rank, s.timestamp_verified, s.extracted_minute,
		       a.popularity, a.width, a.height, a.duration_ms
		FROM video_shares s
		JOIN video_assets a ON a.id = s.asset_id
		WHERE s.event_id = $1 AND s.state = 'active' AND a.superseded_by IS NULL
		ORDER BY s.rank`, eventID)
	if err != nil {
		return nil, fmt.Errorf("pg.ShareRepo.ListLiveForEvent: %w", err)
	}
	defer rows.Close()
	var out []video.LiveClip
	for rows.Next() {
		var c video.LiveClip
		if err := rows.Scan(&c.ShareID, &c.Rank, &c.Verified, &c.ExtractedMinute,
			&c.Popularity, &c.Width, &c.Height, &c.DurationMS); err != nil {
			return nil, fmt.Errorf("pg.ShareRepo.ListLiveForEvent: scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveShare follows share_id → asset → superseded_by chain → live asset via
// a recursive CTE, returning the share's State + the live asset's (bucket, key)
// — the /videos/{share_id} redirect's 302 target (#167). ErrNotFound when the
// share id was never minted (→ 404). A superseded share still resolves (URL
// stability): the anchor asset is found via the share, then the chain walks
// superseded_by to the single live asset. depth<64 guards a pathological cycle.
func (r *ShareRepo) ResolveShare(ctx context.Context, id string) (video.ResolvedShare, error) {
	var rs video.ResolvedShare
	var state string
	err := r.pool.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
			SELECT a.id, a.superseded_by, a.s3_bucket, a.s3_key, 0 AS depth
			FROM video_assets a
			WHERE a.id = (SELECT asset_id FROM video_shares WHERE id = $1)
			UNION ALL
			SELECT a.id, a.superseded_by, a.s3_bucket, a.s3_key, c.depth + 1
			FROM chain c JOIN video_assets a ON a.id = c.superseded_by
			WHERE c.depth < 64
		)
		SELECT (SELECT state FROM video_shares WHERE id = $1), c.s3_bucket, c.s3_key
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
