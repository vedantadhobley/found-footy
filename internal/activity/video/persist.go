// persist.go — the V-phase consumer-queue activities (#164b): the steps the
// EventWorkflow's serialized Selector runs AFTER dedup + vision, per unique
// verified clip. Separate from the per-candidate child activities
// (DownloadAndStage/HashVideo) because these are parent/cross-candidate work
// touching pg + the assets/ prefix.
//
//	PromoteAndPersist — copy staging→assets (server-side) + insert the asset
//	  + mint a share + rebalance ranks. Combined into ONE activity (vs the
//	  design's two steps) because the asset UUID can't be minted in workflow
//	  code (non-deterministic), and it drives both the S3 key and the DB row.
//	BumpAssetPopularity — persist a collapse onto an already-inserted asset.
//	DeleteStaging — drop a staging object (rejected clip / dedup loser).
//
// Idempotency: the asset UUID is DERIVED from (event_id, md5) via uuid v5, so
// a retried activity produces the same UUID → the same assets key (copy is a
// no-op overwrite) and the same row (InsertAsset is ON CONFLICT). The share
// mint is guarded by a "does a share already exist for this asset?" check so
// a retry never double-mints. BumpPopularity is the one non-idempotent step
// (a retry may over-count by one) — benign for a soft vote signal.
package video

import (
	"context"
	"encoding/hex"
	"fmt"
	"path"
	"time"

	"github.com/google/uuid"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// s3Promoter is the S3 subset the persist activities need.
type s3Promoter interface {
	Copy(ctx context.Context, srcKey, dstKey string) error
	Delete(ctx context.Context, key string) error
}

// PersistActivities bundles the consumer-queue persistence activities + deps.
type PersistActivities struct {
	S3           s3Promoter
	Assets       dvideo.AssetRepo
	Shares       dvideo.ShareRepo
	Bucket       string // garage bucket the asset row records
	AssetsPrefix string // e.g. "assets"
}

// PromoteAndPersistInput is one verified unique clip ready to surface.
type PromoteAndPersistInput struct {
	EventID       uuid.UUID
	FixtureID     int64
	StagingKey    string
	MD5           string // hex, from DownloadAndStage
	FrameHashes   []uint64
	Width, Height int
	DurationMS    int
	FileSizeBytes int64
	Bitrate       *int

	// Vision verdict, snapshotted onto the share.
	Verified        bool
	ExtractedMinute *int
}

// PromoteAndPersistOutput reports what got persisted. Inserted=false means
// the ON CONFLICT (event_id, md5) path was taken (retry / exact dupe).
type PromoteAndPersistOutput struct {
	AssetID  uuid.UUID
	ShareID  string
	S3Key    string
	Inserted bool
}

// PromoteAndPersist copies the clip into the assets prefix and records the
// asset + share, all keyed off a deterministic asset UUID for idempotency.
func (a *PersistActivities) PromoteAndPersist(ctx context.Context, in PromoteAndPersistInput) (PromoteAndPersistOutput, error) {
	var out PromoteAndPersistOutput

	md5Bytes, err := hex.DecodeString(in.MD5)
	if err != nil {
		return out, fmt.Errorf("video.PromoteAndPersist: bad md5 %q: %w", in.MD5, err)
	}
	// Deterministic asset id from (event, md5): stable across retries so the
	// copy key + the row are the same on every attempt.
	assetID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(in.EventID.String()+":"+in.MD5))
	dstKey := path.Join(a.AssetsPrefix, fmt.Sprint(in.FixtureID), in.EventID.String(), assetID.String()+".mp4")
	out.AssetID, out.S3Key = assetID, dstKey

	if err := a.S3.Copy(ctx, in.StagingKey, dstKey); err != nil {
		return out, fmt.Errorf("video.PromoteAndPersist: copy: %w", err)
	}

	// Build the asset with the deterministic id (NewAsset mints a random one
	// we override) + the computed key.
	asset := dvideo.NewAsset(in.EventID, in.FixtureID, a.Bucket, dstKey,
		md5Bytes, in.FrameHashes, in.Width, in.Height, in.DurationMS, in.FileSizeBytes, time.Now().UTC())
	asset.ID = assetID
	asset.Bitrate = in.Bitrate

	inserted, err := a.Assets.InsertAsset(ctx, asset)
	if err != nil {
		return out, fmt.Errorf("video.PromoteAndPersist: insert asset: %w", err)
	}
	out.Inserted = inserted

	// Mint the share unless one already exists for this asset (retry guard).
	existing, err := a.Shares.GetByEvent(ctx, in.EventID)
	if err != nil {
		return out, fmt.Errorf("video.PromoteAndPersist: get shares: %w", err)
	}
	for _, s := range existing {
		if s.AssetID == assetID {
			out.ShareID = s.ID
			return out, nil // already minted on a prior attempt — idempotent
		}
	}

	// Append at the next rank; RebalanceRanks reorders by CompareShares.
	share, err := dvideo.NewShare(assetID, in.EventID, in.Verified, in.ExtractedMinute, len(existing)+1, time.Now().UTC())
	if err != nil {
		return out, fmt.Errorf("video.PromoteAndPersist: new share: %w", err)
	}
	if err := a.Shares.Insert(ctx, share); err != nil {
		return out, fmt.Errorf("video.PromoteAndPersist: insert share: %w", err)
	}
	if _, err := a.Shares.RebalanceRanks(ctx, in.EventID); err != nil {
		return out, fmt.Errorf("video.PromoteAndPersist: rebalance: %w", err)
	}
	out.ShareID = share.ID
	return out, nil
}

// BumpAssetPopularityInput identifies the asset a candidate collapsed onto.
type BumpAssetPopularityInput struct {
	AssetID uuid.UUID
}

// BumpAssetPopularity persists a collapse onto an already-inserted asset.
func (a *PersistActivities) BumpAssetPopularity(ctx context.Context, in BumpAssetPopularityInput) error {
	if err := a.Assets.BumpPopularity(ctx, in.AssetID); err != nil {
		return fmt.Errorf("video.BumpAssetPopularity: %w", err)
	}
	return nil
}

// DeleteStagingInput identifies a staging object to drop.
type DeleteStagingInput struct {
	StagingKey string
}

// DeleteStaging removes a staging object (rejected clip / dedup loser).
// S3 delete of a missing key is a no-op, so this is safe to retry.
func (a *PersistActivities) DeleteStaging(ctx context.Context, in DeleteStagingInput) error {
	if err := a.S3.Delete(ctx, in.StagingKey); err != nil {
		return fmt.Errorf("video.DeleteStaging: %w", err)
	}
	return nil
}
