// persist.go — consumer-queue persistence activities: the steps the
// EventWorkflow's serialized Selector runs AFTER dedup + vision. Separate from
// the per-candidate child activities
// (DownloadAndStage/HashVideo) because these are parent/cross-candidate work
// touching pg + the assets/ prefix.
//
//	CommitClipPlacement — the current FF-066 path: candidate terminal state,
//	  popularity credit, asset/share mint, and supersession commit in one pg
//	  transaction; S3 copy and cleanup form its retry-safe activity tail.
//	LoadEventAssets — restore live persisted assets plus retired-MD5 aliases
//	  into a replacement EventWorkflow execution so dedup retains prior progress.
//	DeleteStaging — drop a staging object (rejected clip / dedup loser).
//	DestroyEvent — tear down an overturned event's clips (#172): revoke all its
//	  shares (→ 410) + reclaim its Garage objects. Caller cancels discovery first.
//	PromoteAndPersist, BumpAssetPopularity, and SupersedeAssets — retained only
//	  for replay of pre-FF-066 Temporal histories.
//
// Idempotency: the asset UUID is DERIVED from (event_id, md5) via uuid v5. A
// retry first checks that row: when it exists, destination bytes are already
// durable, so the retry skips Copy even if its prior attempt deleted staging
// before the completion acknowledgement was delivered. The share mint is
// guarded by a "does a share already exist for this asset?" check, but an
// existing share is progress rather than an early return: compatibility rank
// repair and staging cleanup still run. New histories never call the legacy
// non-idempotent BumpAssetPopularity activity.
package video

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
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
	Placements   dvideo.PlacementRepo
	Bucket       string // garage bucket the asset row records
	AssetsPrefix string // e.g. "assets"
}

// PlacementCandidateInput carries one accepted candidate's complete evidence
// and terminal class into the atomic placement transaction.
type PlacementCandidateInput struct {
	Evidence discoverycontract.CandidateEvidence
	Outcome  discoverycontract.CandidateOutcome
	Detail   json.RawMessage
}

// CommitClipPlacementInput is one complete post-dedup placement. NewWinner
// selects deterministic staging→asset promotion; otherwise WinnerAssetID is an
// existing live asset. Losers consolidate within the same transaction.
type CommitClipPlacementInput struct {
	EventID       uuid.UUID
	FixtureID     int64
	NewWinner     bool
	WinnerAssetID uuid.UUID
	LoserAssetIDs []uuid.UUID
	Candidates    []PlacementCandidateInput

	StagingKey      string
	MD5             string
	HashVersion     dvideo.FrameHashVersion
	FrameHashes     []uint64
	Width, Height   int
	DurationMS      int
	FileSizeBytes   int64
	Bitrate         *int
	Verified        bool
	ExtractedMinute *int
}

// CommitClipPlacementOutput identifies either the canonical public winner or a
// removed-event discard. Announce is true after every committed placement,
// including a retry whose workflow still owes the dirty signal; EventRemoved
// is terminal and never announceable.
type CommitClipPlacementOutput struct {
	WinnerAssetID uuid.UUID
	ShareID       string
	WinnerCreated bool
	Announce      bool
	EventRemoved  bool
}

// CommitClipPlacement copies a new winner when needed, commits the complete
// accepted-candidate database mutation atomically, and then performs
// idempotent staging/loser-object cleanup. New workflow histories use this
// instead of the independent promote/bump/supersede/outcome calls.
func (a *PersistActivities) CommitClipPlacement(ctx context.Context, in CommitClipPlacementInput) (CommitClipPlacementOutput, error) {
	var out CommitClipPlacementOutput
	if a.Placements == nil {
		return out, fmt.Errorf("video.CommitClipPlacement: placement store is required")
	}
	if in.EventID == uuid.Nil || in.FixtureID <= 0 || in.StagingKey == "" || len(in.Candidates) == 0 {
		return out, fmt.Errorf("video.CommitClipPlacement: incomplete placement input")
	}

	placement := dvideo.ClipPlacement{
		EventID: in.EventID, FixtureID: in.FixtureID,
		WinnerAssetID: in.WinnerAssetID,
		Verified:      in.Verified, ExtractedMinute: in.ExtractedMinute,
		LoserAssetIDs: append([]uuid.UUID(nil), in.LoserAssetIDs...),
		CommittedAt:   time.Now().UTC(),
	}
	for _, candidate := range in.Candidates {
		placement.Candidates = append(placement.Candidates, dvideo.PlacementCandidate{
			Evidence: candidate.Evidence, Outcome: candidate.Outcome, Detail: candidate.Detail,
		})
	}

	var destinationKey string
	if in.NewWinner {
		md5Bytes, err := hex.DecodeString(in.MD5)
		if err != nil {
			return out, fmt.Errorf("video.CommitClipPlacement: bad md5 %q: %w", in.MD5, err)
		}
		assetID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(in.EventID.String()+":"+in.MD5))
		dstKey := path.Join(a.AssetsPrefix, fmt.Sprint(in.FixtureID), in.EventID.String(), assetID.String()+".mp4")
		destinationKey = dstKey
		existing, err := a.Assets.Get(ctx, assetID)
		switch {
		case err == nil:
			compat := PromoteAndPersistInput{
				EventID: in.EventID, FixtureID: in.FixtureID, StagingKey: in.StagingKey,
				MD5: in.MD5, HashVersion: in.HashVersion, FrameHashes: in.FrameHashes,
				Width: in.Width, Height: in.Height, DurationMS: in.DurationMS,
				FileSizeBytes: in.FileSizeBytes, Bitrate: in.Bitrate,
			}
			if err := validatePromotionAsset(existing, compat, md5Bytes, a.Bucket, dstKey); err != nil {
				return out, fmt.Errorf("video.CommitClipPlacement: %w", err)
			}
		case errors.Is(err, dvideo.ErrNotFound):
			if err := a.S3.Copy(ctx, in.StagingKey, dstKey); err != nil {
				return out, fmt.Errorf("video.CommitClipPlacement: copy: %w", err)
			}
		default:
			return out, fmt.Errorf("video.CommitClipPlacement: get winner: %w", err)
		}

		asset := dvideo.NewAsset(in.EventID, in.FixtureID, a.Bucket, dstKey,
			md5Bytes, in.HashVersion, in.FrameHashes,
			in.Width, in.Height, in.DurationMS, in.FileSizeBytes, placement.CommittedAt)
		asset.ID = assetID
		asset.Bitrate = in.Bitrate
		placement.Winner = asset
		placement.WinnerAssetID = uuid.Nil
	} else if in.WinnerAssetID == uuid.Nil {
		return out, fmt.Errorf("video.CommitClipPlacement: existing winner id is required")
	}

	result, err := a.Placements.CommitClipPlacement(ctx, placement)
	if err != nil {
		return out, fmt.Errorf("video.CommitClipPlacement: persist: %w", err)
	}
	if result.EventRemoved {
		var cleanupErrs []error
		if destinationKey != "" {
			if err := a.S3.Delete(ctx, destinationKey); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("delete removed destination: %w", err))
			}
		}
		if err := a.S3.Delete(ctx, in.StagingKey); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("delete removed staging: %w", err))
		}
		if err := errors.Join(cleanupErrs...); err != nil {
			return out, fmt.Errorf("video.CommitClipPlacement: event removed cleanup: %w", err)
		}
		return CommitClipPlacementOutput{EventRemoved: true}, nil
	}
	if err := a.S3.Delete(ctx, in.StagingKey); err != nil {
		return out, fmt.Errorf("video.CommitClipPlacement: delete staging: %w", err)
	}
	for _, object := range result.LoserObjects {
		if object.Key == "" {
			continue
		}
		if err := a.S3.Delete(ctx, object.Key); err != nil {
			activity.GetLogger(ctx).Warn("placement: loser byte reclaim failed",
				"key", object.Key, "err", err)
			continue
		}
		if err := a.Assets.MarkObjectReclaimed(ctx, object.AssetID); err != nil {
			activity.GetLogger(ctx).Warn("placement: record loser reclaim failed",
				"asset_id", object.AssetID.String(), "err", err)
		}
	}
	return CommitClipPlacementOutput{
		WinnerAssetID: result.WinnerAssetID,
		ShareID:       result.ShareID,
		WinnerCreated: result.WinnerCreated,
		Announce:      true,
	}, nil
}

// PromoteAndPersistInput is one verified unique clip ready to surface.
type PromoteAndPersistInput struct {
	EventID       uuid.UUID
	FixtureID     int64
	StagingKey    string
	MD5           string // hex, from DownloadAndStage
	HashVersion   dvideo.FrameHashVersion
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

// LoadEventAssetsInput identifies the event whose live dedup state is needed.
type LoadEventAssetsInput struct {
	EventID uuid.UUID
}

// RestoredEventAsset is the workflow-safe projection of one active share and
// its live asset. It contains exactly the fields the in-memory dedup
// pipeline needs after an abnormal EventWorkflow restart.
type RestoredEventAsset struct {
	AssetID       uuid.UUID
	MD5           string
	HashVersion   dvideo.FrameHashVersion
	FrameHashes   []uint64
	Width         int
	Height        int
	DurationMS    int
	FileSizeBytes int64
	Bitrate       *int
	Popularity    int
	Verified      bool
}

// RestoredExactAlias maps a persisted exact-byte variant to the live asset
// that currently represents it. Superseded variants remain useful dedup
// evidence after their own asset leaves the public set.
type RestoredExactAlias struct {
	MD5     string
	AssetID uuid.UUID
}

// LoadEventAssetsOutput returns the event's live durable dedup state and the
// exact-byte aliases needed to resolve retired variants to that state.
type LoadEventAssetsOutput struct {
	Assets       []RestoredEventAsset
	ExactAliases []RestoredExactAlias
}

// LoadEventAssets restores the durable portion of EventWorkflow's in-memory
// state. Shares arrive in current-evidence order. Only active, unsuperseded
// assets re-enter the perceptual matcher; every share asset whose supersession
// chain terminates at one of those live assets contributes an exact-byte alias.
// A corrupt supersession cycle fails closed instead of hanging recovery.
func (a *PersistActivities) LoadEventAssets(ctx context.Context, in LoadEventAssetsInput) (LoadEventAssetsOutput, error) {
	var out LoadEventAssetsOutput
	shares, err := a.Shares.GetByEvent(ctx, in.EventID)
	if err != nil {
		return out, fmt.Errorf("video.LoadEventAssets: get shares: %w", err)
	}
	assets := make(map[uuid.UUID]*dvideo.Asset, len(shares))
	loadAsset := func(id uuid.UUID) (*dvideo.Asset, error) {
		if asset, ok := assets[id]; ok {
			return asset, nil
		}
		asset, err := a.Assets.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("get asset %s: %w", id, err)
		}
		if asset.EventID != in.EventID {
			return nil, fmt.Errorf("asset %s belongs to event %s, want %s", id, asset.EventID, in.EventID)
		}
		assets[id] = asset
		return asset, nil
	}

	// Load every share asset once so the alias pass can follow chains even
	// when the terminal winner's share appears later in evidence order.
	for _, share := range shares {
		if _, err := loadAsset(share.AssetID); err != nil {
			return out, fmt.Errorf("video.LoadEventAssets: %w", err)
		}
	}

	live := make(map[uuid.UUID]struct{}, len(shares))
	for _, share := range shares {
		if share.State != dvideo.ShareStateActive {
			continue
		}
		asset := assets[share.AssetID]
		if asset.SupersededBy != nil {
			continue
		}
		live[asset.ID] = struct{}{}
		out.Assets = append(out.Assets, RestoredEventAsset{
			AssetID: asset.ID, MD5: hex.EncodeToString(asset.MD5),
			HashVersion: asset.FrameHashVersion, FrameHashes: asset.FrameHashes,
			Width: asset.Width, Height: asset.Height, DurationMS: asset.DurationMS,
			FileSizeBytes: asset.FileSizeBytes, Bitrate: asset.Bitrate,
			Popularity: asset.Popularity, Verified: share.TimestampVerified,
		})
	}

	seenMD5 := make(map[string]struct{}, len(shares))
	for _, share := range shares {
		asset := assets[share.AssetID]
		md5Hex := hex.EncodeToString(asset.MD5)
		if _, seen := seenMD5[md5Hex]; seen {
			continue
		}
		root := asset
		visited := make(map[uuid.UUID]struct{})
		for root.SupersededBy != nil {
			if _, seen := visited[root.ID]; seen {
				return out, fmt.Errorf("video.LoadEventAssets: supersession cycle at asset %s", root.ID)
			}
			visited[root.ID] = struct{}{}
			root, err = loadAsset(*root.SupersededBy)
			if err != nil {
				return out, fmt.Errorf("video.LoadEventAssets: %w", err)
			}
		}
		if _, isLive := live[root.ID]; !isLive {
			continue
		}
		seenMD5[md5Hex] = struct{}{}
		out.ExactAliases = append(out.ExactAliases, RestoredExactAlias{
			MD5: md5Hex, AssetID: root.ID,
		})
	}
	return out, nil
}

// PromoteAndPersistOutput reports what got persisted. Inserted=false means
// the ON CONFLICT (event_id, md5) path was taken (retry / exact dupe).
type PromoteAndPersistOutput struct {
	AssetID  uuid.UUID
	ShareID  string
	S3Key    string
	Inserted bool
	// Minted tells the workflow that this successful activity completion has
	// a durable share ready to announce. It is also true when a retry finds a
	// share inserted by its failed prior attempt: the workflow never observed
	// that failed attempt, so the final success still owes one dirty signal.
	// Duplicate signals after an external re-drive are harmless; consumers
	// refetch current event state.
	Minted bool
}

// PromoteAndPersist is the pre-FF-066 compatibility path. It copies the clip
// into the assets prefix, records the asset and share, repairs stored rank,
// then deletes staging. All durable identity is keyed off a deterministic
// asset UUID. An existing asset proves a prior attempt
// copied destination bytes before inserting its row, so retries can skip Copy
// after staging has been deleted.
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

	existingAsset, err := a.Assets.Get(ctx, assetID)
	switch {
	case err == nil:
		if err := validatePromotionAsset(existingAsset, in, md5Bytes, a.Bucket, dstKey); err != nil {
			return out, err
		}
	case errors.Is(err, dvideo.ErrNotFound):
		if err := a.S3.Copy(ctx, in.StagingKey, dstKey); err != nil {
			return out, fmt.Errorf("video.PromoteAndPersist: copy: %w", err)
		}

		// Build the asset with the deterministic id (NewAsset mints a random
		// one we override) + the computed key.
		asset := dvideo.NewAsset(in.EventID, in.FixtureID, a.Bucket, dstKey,
			md5Bytes, in.HashVersion, in.FrameHashes,
			in.Width, in.Height, in.DurationMS, in.FileSizeBytes, time.Now().UTC())
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
		if !inserted {
			// A concurrent/idempotent ON CONFLICT must resolve to the same
			// deterministic row. Verify that invariant before minting its share.
			existingAsset, err = a.Assets.Get(ctx, assetID)
			if err != nil {
				return out, fmt.Errorf("video.PromoteAndPersist: get conflicted asset: %w", err)
			}
			if err := validatePromotionAsset(existingAsset, in, md5Bytes, a.Bucket, dstKey); err != nil {
				return out, err
			}
		}
	default:
		return out, fmt.Errorf("video.PromoteAndPersist: get asset: %w", err)
	}

	// Mint the share unless one already exists for this asset (retry guard).
	existingShares, err := a.Shares.GetByEvent(ctx, in.EventID)
	if err != nil {
		return out, fmt.Errorf("video.PromoteAndPersist: get shares: %w", err)
	}
	for _, s := range existingShares {
		if s.AssetID == assetID {
			out.ShareID = s.ID
			break // prior-attempt progress; rebalance + cleanup are still owed
		}
	}

	if out.ShareID == "" {
		// Append at the next rank; RebalanceRanks reorders by CompareShares.
		share, err := dvideo.NewShare(assetID, in.EventID, in.Verified, in.ExtractedMinute, len(existingShares)+1, time.Now().UTC())
		if err != nil {
			return out, fmt.Errorf("video.PromoteAndPersist: new share: %w", err)
		}
		if err := a.Shares.Insert(ctx, share); err != nil {
			return out, fmt.Errorf("video.PromoteAndPersist: insert share: %w", err)
		}
		out.ShareID = share.ID
	}
	if _, err := a.Shares.RebalanceRanks(ctx, in.EventID); err != nil {
		return out, fmt.Errorf("video.PromoteAndPersist: rebalance: %w", err)
	}
	if err := a.S3.Delete(ctx, in.StagingKey); err != nil {
		return out, fmt.Errorf("video.PromoteAndPersist: delete staging: %w", err)
	}
	out.Minted = true
	return out, nil
}

// validatePromotionAsset rejects a deterministic-ID collision or a row whose
// immutable storage identity drifted. A retry may trust an existing row only
// because the activity always copied destination bytes before inserting it.
func validatePromotionAsset(existing *dvideo.Asset, in PromoteAndPersistInput, md5Bytes []byte, bucket, dstKey string) error {
	if existing == nil || existing.ID != uuid.NewSHA1(uuid.NameSpaceOID, []byte(in.EventID.String()+":"+in.MD5)) ||
		existing.EventID != in.EventID || existing.FixtureID != in.FixtureID ||
		existing.S3Bucket != bucket || existing.S3Key != dstKey || !bytes.Equal(existing.MD5, md5Bytes) ||
		dvideo.NormalizeFrameHashVersion(existing.FrameHashVersion) != dvideo.NormalizeFrameHashVersion(in.HashVersion) {
		return fmt.Errorf("video.PromoteAndPersist: deterministic asset identity mismatch for %s", dstKey)
	}
	return nil
}

// BumpAssetPopularityInput identifies the asset a candidate collapsed onto.
// Count is the loser's accumulated vote total (a clip that absorbed gate
// md5-dups while pending, #180, carries them all here); 0 means a single vote.
type BumpAssetPopularityInput struct {
	AssetID uuid.UUID
	Count   int
}

// BumpAssetPopularity is the retry-unsafe pre-FF-066 collapse writer. New
// histories credit candidates through CommitClipPlacement instead.
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
// Garage bytes. One compatibility RebalanceRanks runs after the pool shrank.
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
		// the durable retention planner — don't fail a DB-consistent supersede on it.
		if loserKey != "" {
			if err := a.S3.Delete(ctx, loserKey); err != nil {
				activity.GetLogger(ctx).Warn("supersede: loser byte reclaim failed",
					"key", loserKey, "err", err)
			} else if err := a.Assets.MarkObjectReclaimed(ctx, loserID); err != nil {
				activity.GetLogger(ctx).Warn("supersede: record loser reclaim failed",
					"asset_id", loserID.String(), "err", err)
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
// The caller asks the event's discovery to cancel; FF-067's event-row gate owns
// the late-placement race. Idempotent: RemoveByEvent skips already-removed
// shares and S3 delete no-ops on missing keys. Revoke precedes reclaim so
// serving stops even when byte deletion must retry.
func (a *PersistActivities) DestroyEvent(ctx context.Context, in DestroyEventInput) error {
	reason := dvideo.RemovalReason(in.Reason)
	if reason == "" {
		reason = dvideo.RemovalVAR
	}
	if err := a.Shares.RemoveByEvent(ctx, in.EventID, reason); err != nil {
		return fmt.Errorf("video.DestroyEvent: revoke shares: %w", err)
	}
	objects, err := a.Assets.ListUnreclaimedObjectsByEvent(ctx, in.EventID)
	if err != nil {
		return fmt.Errorf("video.DestroyEvent: list unreclaimed objects: %w", err)
	}
	var reclaimErrs []error
	for _, object := range objects {
		if err := a.S3.Delete(ctx, object.Key); err != nil {
			reclaimErrs = append(reclaimErrs, fmt.Errorf("delete %s: %w", object.Key, err))
			continue
		}
		if err := a.Assets.MarkObjectReclaimed(ctx, object.AssetID); err != nil {
			reclaimErrs = append(reclaimErrs, fmt.Errorf("record reclaim %s: %w", object.AssetID, err))
		}
	}
	if err := errors.Join(reclaimErrs...); err != nil {
		return fmt.Errorf("video.DestroyEvent: reclaim objects: %w", err)
	}
	return nil
}
