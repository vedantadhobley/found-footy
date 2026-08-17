// persist.go — consumer-queue persistence activities: the steps the
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
//	SupersedeAssets — consolidate loser assets onto a winner (post-vision,
//	  category-scoped dedup #171): superseded_by chain + popularity merge +
//	  retire loser shares + reclaim loser bytes + one rank rebalance.
//	DestroyEvent — tear down an overturned event's clips (#172): revoke all its
//	  shares (→ 410) + reclaim its Garage objects. Caller cancels discovery first.
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
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"

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

	// Popularity the new asset starts at — its own sighting plus any gate
	// md5-dups that collapsed onto it while it was pending (#180). 0/1 → 1.
	Popularity int

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
	// Minted is true when a NEW share was minted this call (vs an
	// idempotent-retry no-op that found an existing share). The pipeline
	// pings event.video only when Minted, so a retry never re-announces.
	Minted bool
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
	if in.Popularity > 1 {
		asset.Popularity = in.Popularity // NewAsset defaults to 1; carry accumulated votes
	}

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
	out.Minted = true
	return out, nil
}

// BumpAssetPopularityInput identifies the asset a candidate collapsed onto.
// Count is the loser's accumulated vote total (a clip that absorbed gate
// md5-dups while pending, #180, carries them all here); 0 means a single vote.
type BumpAssetPopularityInput struct {
	AssetID uuid.UUID
	Count   int
}

// BumpAssetPopularity persists a collapse onto an already-inserted asset,
// adding Count (min 1) votes.
func (a *PersistActivities) BumpAssetPopularity(ctx context.Context, in BumpAssetPopularityInput) error {
	n := in.Count
	if n < 1 {
		n = 1
	}
	if err := a.Assets.AddPopularity(ctx, in.AssetID, n); err != nil {
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

// SupersedeAssetsInput consolidates one or more loser assets onto a winner —
// the write side of a post-vision dedup collapse (#171). Losers may be a
// just-beaten incumbent (a higher-quality clip won the pool) AND/OR bridged
// assets (a clip perceptually matched two assets that don't match each other;
// dHash isn't transitive).
type SupersedeAssetsInput struct {
	EventID       uuid.UUID
	WinnerAssetID uuid.UUID
	LoserAssetIDs []uuid.UUID
}

// SupersedeAssets retires each loser onto the winner: atomic superseded_by +
// popularity merge (Assets.Supersede), retire the loser's active share (still
// resolves via the chain, but leaves the ranked list), and reclaim the loser's
// Garage bytes. One RebalanceRanks at the end, after the active pool shrank.
// Idempotent end-to-end — Supersede, MarkSuperseded, and the S3 delete all
// no-op on a retry — so the whole activity is safe to replay.
func (a *PersistActivities) SupersedeAssets(ctx context.Context, in SupersedeAssetsInput) error {
	if len(in.LoserAssetIDs) == 0 {
		return nil
	}

	// One share lookup for the event; index the active share per asset.
	shares, err := a.Shares.GetByEvent(ctx, in.EventID)
	if err != nil {
		return fmt.Errorf("video.SupersedeAssets: get shares: %w", err)
	}
	activeShareByAsset := make(map[uuid.UUID]string, len(shares))
	for _, s := range shares {
		if s.State == dvideo.ShareStateActive {
			activeShareByAsset[s.AssetID] = s.ID
		}
	}

	for _, loserID := range in.LoserAssetIDs {
		if loserID == in.WinnerAssetID {
			continue
		}

		// Read the loser's S3 key first (the row survives supersede). Tolerate a
		// missing loser — a retry may already have reclaimed it.
		var loserKey string
		if loser, err := a.Assets.Get(ctx, loserID); err == nil {
			loserKey = loser.S3Key
		} else if !errors.Is(err, dvideo.ErrNotFound) {
			return fmt.Errorf("video.SupersedeAssets: get loser %s: %w", loserID, err)
		}

		if err := a.Assets.Supersede(ctx, loserID, in.WinnerAssetID); err != nil {
			return fmt.Errorf("video.SupersedeAssets: supersede %s: %w", loserID, err)
		}
		if shareID, ok := activeShareByAsset[loserID]; ok {
			if err := a.Shares.MarkSuperseded(ctx, shareID); err != nil {
				return fmt.Errorf("video.SupersedeAssets: retire share %s: %w", shareID, err)
			}
		}
		// Reclaim bytes best-effort: the chain resolves reads to the winner, so
		// the loser's object is never served. A failed delete just leaves it for
		// the retention prune (#176) — don't fail a DB-consistent supersede on it.
		if loserKey != "" {
			if err := a.S3.Delete(ctx, loserKey); err != nil {
				activity.GetLogger(ctx).Warn("supersede: loser byte reclaim failed",
					"key", loserKey, "err", err)
			}
		}
	}

	if _, err := a.Shares.RebalanceRanks(ctx, in.EventID); err != nil {
		return fmt.Errorf("video.SupersedeAssets: rebalance: %w", err)
	}
	return nil
}

// DestroyEventInput identifies an event whose surfaced clips must be torn down.
type DestroyEventInput struct {
	EventID uuid.UUID
	Reason  string // removal reason; empty → "var"
}

// DestroyEvent tears down an overturned event's clips (#172): revoke ALL its
// shares to 'removed' (→ ResolveShare returns 'removed', the redirect 410s, so
// the clips stop serving) then delete its asset objects from Garage (reclaim).
// The caller (poll workflow) cancels the event's discovery FIRST, so nothing
// mints new clips after this runs. Idempotent: RemoveByEvent skips already-
// removed shares; S3 delete no-ops on missing keys. Revoke precedes reclaim so
// serving stops even if a byte delete lags/fails.
func (a *PersistActivities) DestroyEvent(ctx context.Context, in DestroyEventInput) error {
	reason := dvideo.RemovalReason(in.Reason)
	if reason == "" {
		reason = dvideo.RemovalVAR
	}
	if err := a.Shares.RemoveByEvent(ctx, in.EventID, reason); err != nil {
		return fmt.Errorf("video.DestroyEvent: revoke shares: %w", err)
	}
	keys, err := a.Assets.ListObjectKeysByEvent(ctx, in.EventID)
	if err != nil {
		return fmt.Errorf("video.DestroyEvent: list object keys: %w", err)
	}
	for _, k := range keys {
		if err := a.S3.Delete(ctx, k.Key); err != nil {
			// Best-effort byte reclaim: shares are already revoked (clips no longer
			// serve), so a failed delete just leaves the object for the prune (#176).
			activity.GetLogger(ctx).Warn("destroy: object reclaim failed", "key", k.Key, "err", err)
		}
	}
	return nil
}
