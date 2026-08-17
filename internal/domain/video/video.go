// Package video is the domain layer for video assets + shares. Pure Go
// — no HTTP, no S3, no DB. Mirrors the schema's video_assets +
// video_shares tables (see internal/infra/pg/schema.sql).
//
// Two related concepts:
//
//	Asset — the canonical byte store. One row per unique perceptual
//	        hash per event. Dedup is scoped to the event, never across
//	        events (cross-event/per-fixture dedup is dead — see
//	        decisions.md 2026-07-25). Popularity counts within-event
//	        dedup hits.
//	Share — a public link (id: "s_<12-hex>") that grants a browser
//	        access to one Asset in the context of one Event. Ranked
//	        within event.
//
// The Asset ↔ Share split is what enables URL stability: even if we
// dedup two shares' underlying assets or supersede one asset with a
// higher-quality re-encode, the public s_<hex> share URL keeps working
// via the video_shares → video_assets FK (RESTRICT).
package video

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ShareState matches the share_state enum in the schema.
type ShareState string

const (
	ShareStateActive     ShareState = "active"
	ShareStateRemoved    ShareState = "removed"
	ShareStateSuperseded ShareState = "superseded" // replaced by a better/consolidated clip; still resolvable, not listed
)

// Valid reports whether s is a known share state.
func (s ShareState) Valid() bool {
	switch s {
	case ShareStateActive, ShareStateRemoved, ShareStateSuperseded:
		return true
	}
	return false
}

// RemovalReason mirrors the removal_reason enum. Same set as event's
// (VAR / policy / asset_gone). Duplicated locally rather than imported
// to keep the domain packages loose-coupled.
type RemovalReason string

const (
	RemovalVAR       RemovalReason = "var"
	RemovalPolicy    RemovalReason = "policy"
	RemovalAssetGone RemovalReason = "asset_gone"
)

// Valid reports whether r is a known removal reason.
func (r RemovalReason) Valid() bool {
	switch r {
	case RemovalVAR, RemovalPolicy, RemovalAssetGone:
		return true
	}
	return false
}

// Asset is the canonical byte-store row (video_assets table).
//
// Dedup model (2026-08-03, #166): the ONLY storage-enforced dedup is
//
//	UNIQUE (event_id, md5) — exact-byte, and it doubles as insert
//
// idempotency. Perceptual dedup is FUZZY (a sliding-window match over the
// FrameHashes sequence) and lives in EventWorkflow code, decided in-memory
// before insert — no SQL constraint can express it. FrameHashes is persisted
// as a queryable record (debugging / re-dedup), not as a decision-maker.
//
// EventID is the dedup scope; FixtureID is a denormalized convenience
// (s3 path + prune). Dedup is never cross-event — decisions.md 2026-07-25.
type Asset struct {
	ID        uuid.UUID
	EventID   uuid.UUID
	FixtureID int64

	S3Bucket string
	S3Key    string // computed by the S3 client from (fixture_id, id); NOT stored redundantly per §3 principle #3, but IS on the row for read speed

	MD5         []byte   // 16-byte whole-file digest — the exact-dup layer
	FrameHashes []uint64 // per-frame dHash sequence (one per 0.1s frame); the perceptual-dedup signal, kept opaque

	Width         int
	Height        int
	DurationMS    int
	FileSizeBytes int64
	Bitrate       *int
	AspectRatio   float32 // schema-generated column; carried in domain for read-only use

	Popularity int // starts at 1; bumped on each dedup hit

	SupersededBy *uuid.UUID // set when this asset is replaced by a higher-quality re-encode

	FirstSeenAt time.Time
}

// NewAsset constructs a fresh Asset with a new UUID.
// Popularity starts at 1 (this is the first sighting).
func NewAsset(
	eventID uuid.UUID,
	fixtureID int64,
	s3Bucket, s3Key string,
	md5 []byte,
	frameHashes []uint64,
	width, height, durationMS int,
	fileSize int64,
	at time.Time,
) *Asset {
	return &Asset{
		ID:            uuid.New(),
		EventID:       eventID,
		FixtureID:     fixtureID,
		S3Bucket:      s3Bucket,
		S3Key:         s3Key,
		MD5:           md5,
		FrameHashes:   frameHashes,
		Width:         width,
		Height:        height,
		DurationMS:    durationMS,
		FileSizeBytes: fileSize,
		AspectRatio:   computeAspect(width, height),
		Popularity:    1,
		FirstSeenAt:   at.UTC(),
	}
}

// computeAspect matches the schema's generated column formula.
func computeAspect(width, height int) float32 {
	if height == 0 {
		return 0
	}
	return float32(width) / float32(height)
}

// BumpPopularity records that a dedup hit landed on this asset. Called
// in-memory by the EventWorkflow consumer when a candidate collapses onto
// this asset (the AssetRepo.AddPopularity port persists it when the asset
// already has a DB row).
func (a *Asset) BumpPopularity() {
	a.Popularity++
}

// SupersededBySet marks this asset as replaced by a higher-quality
// re-encode. The successor's UUID becomes SupersededBy so the share-id
// redirect layer (§8) can follow the chain.
func (a *Asset) SupersededBySet(successorID uuid.UUID) {
	a.SupersededBy = &successorID
}

// Share is one public link to an Asset in the context of one Event.
// Public URL is `/api/v1/videos/{ID}` which 302s to a presigned S3 URL.
type Share struct {
	ID      string // "s_<12-hex>", public
	AssetID uuid.UUID
	EventID uuid.UUID

	TimestampVerified bool
	ExtractedMinute   *int // clock minute the vision model extracted

	State         ShareState
	RemovedReason *RemovalReason
	RemovedAt     *time.Time

	Rank int // 1-indexed, UNIQUE per event WHERE state='active'

	CreatedAt time.Time
}

// NewShare mints a fresh share with a cryptographically-random ID.
// Rank is passed in — computing it needs to know what other shares
// exist for the event (repo concern, not domain).
func NewShare(
	assetID, eventID uuid.UUID,
	timestampVerified bool,
	extractedMinute *int,
	rank int,
	at time.Time,
) (*Share, error) {
	id, err := generateShareID()
	if err != nil {
		return nil, fmt.Errorf("video.NewShare: generate id: %w", err)
	}
	if rank < 1 {
		return nil, fmt.Errorf("video.NewShare: rank must be >= 1, got %d", rank)
	}
	return &Share{
		ID:                id,
		AssetID:           assetID,
		EventID:           eventID,
		TimestampVerified: timestampVerified,
		ExtractedMinute:   extractedMinute,
		State:             ShareStateActive,
		Rank:              rank,
		CreatedAt:         at.UTC(),
	}, nil
}

// generateShareID returns "s_<12-hex>" — 48 bits of entropy, matches
// the schema's TEXT PRIMARY KEY convention.
func generateShareID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "s_" + hex.EncodeToString(b[:]), nil
}

// Remove flips the share to state='removed' with a reason. Idempotent
// for same-reason. Errors on invalid reason. Once removed, cannot go
// back — the terminal transition.
func (s *Share) Remove(reason RemovalReason, at time.Time) error {
	if !reason.Valid() {
		return fmt.Errorf("video: unknown removal reason %q", reason)
	}
	if s.State == ShareStateRemoved {
		if s.RemovedReason != nil && *s.RemovedReason == reason {
			return nil // idempotent
		}
		return fmt.Errorf("video: share already removed for reason %v", *s.RemovedReason)
	}
	utc := at.UTC()
	s.State = ShareStateRemoved
	s.RemovedReason = &reason
	s.RemovedAt = &utc
	return nil
}

// ValidateInvariants mirrors the schema CHECK:
//
//	state=active  → RemovedReason=nil, RemovedAt=nil
//	state=removed → RemovedReason!=nil, RemovedAt!=nil
//	rank >= 1
func (s *Share) ValidateInvariants() error {
	if s.Rank < 1 {
		return fmt.Errorf("video.Share.ValidateInvariants: rank=%d, must be >= 1", s.Rank)
	}
	switch s.State {
	case ShareStateActive:
		if s.RemovedReason != nil || s.RemovedAt != nil {
			return fmt.Errorf("video.Share.ValidateInvariants: active with RemovedReason/At set")
		}
	case ShareStateRemoved:
		if s.RemovedReason == nil || s.RemovedAt == nil {
			return fmt.Errorf("video.Share.ValidateInvariants: removed requires RemovedReason + RemovedAt")
		}
	default:
		return fmt.Errorf("video.Share.ValidateInvariants: invalid State %q", s.State)
	}
	return nil
}
