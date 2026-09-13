// Selection preparation checks real objects outside SQL and returns current committed workflow state.
package video

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// PlacementSelectionInput binds this activity command to one policy and retry identity.
type PlacementSelectionInput struct {
	ID     uuid.UUID
	Policy dvideo.SelectionPolicy
}

// PlacementSelectionOutput supplies current roots/aliases, not a historical receipt snapshot.
type PlacementSelectionOutput struct {
	State    LoadEventAssetsOutput
	Restored []uuid.UUID
	Skipped  dvideo.SelectionSkip
}

// selectionLoader keeps the new read boundary mandatory only for versioned callers.
type selectionLoader interface {
	LoadSelection(context.Context, uuid.UUID) (dvideo.SelectionSnapshot, error)
}

// selectionObjectChecker is satisfied by the existing bucket-scoped S3 client.
type selectionObjectChecker interface {
	Head(context.Context, string) (bool, error)
}

// loadSelection refuses a missing adapter rather than downgrading new histories.
func (a *PersistActivities) loadSelection(ctx context.Context, eventID uuid.UUID) (dvideo.SelectionSnapshot, error) {
	loader, ok := a.Placements.(selectionLoader)
	if !ok {
		return dvideo.SelectionSnapshot{}, fmt.Errorf("video selection: consistent store required")
	}
	return loader.LoadSelection(ctx, eventID)
}

// preparePlacementSelection attests only to bytes that actually exist. Missing
// hidden media is ineligible; HEAD failures retry, never masquerade as absence.
func (a *PersistActivities) preparePlacementSelection(ctx context.Context, in CommitClipPlacementInput, placement *dvideo.ClipPlacement, snapshot dvideo.SelectionSnapshot) error {
	if !in.CaptureVariant || in.Selection.ID == uuid.Nil {
		return fmt.Errorf("video selection: exact attribution and operation ID required")
	}
	if err := in.Selection.Policy.Validate(); err != nil {
		return err
	}
	checker, ok := a.S3.(selectionObjectChecker)
	if !ok {
		return fmt.Errorf("video selection: object checker required")
	}
	fingerprint, err := snapshot.Fingerprint()
	if err != nil {
		return err
	}
	request := &dvideo.SelectionRequest{ID: in.Selection.ID, EventID: in.EventID, FixtureID: in.FixtureID,
		SnapshotHash: fingerprint, Policy: in.Selection.Policy}
	placement.Selection = request
	if snapshot.Removed {
		return nil // the transaction owns removal and candidate terminalization
	}
	var assets []*dvideo.Asset
	known := make(map[uuid.UUID]bool)
	for _, node := range snapshot.Nodes {
		known[node.Asset.ID] = true
		if node.Asset.ObjectReclaimedAt == nil && (node.Share != nil && node.Share.State != dvideo.ShareStateRemoved || node.Share == nil && node.Validation != nil) {
			assets = append(assets, node.Asset)
		}
	}
	for _, incoming := range []*dvideo.Asset{placement.Winner, placement.Variant} {
		if incoming != nil && !known[incoming.ID] {
			assets = append(assets, incoming)
		}
	}
	ready := make([]bool, len(assets))
	group, headCtx := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for i, asset := range assets {
		group.Go(func() error {
			if asset.S3Bucket != a.Bucket {
				return fmt.Errorf("video selection: asset bucket mismatch")
			}
			exists, err := checker.Head(headCtx, asset.S3Key)
			ready[i] = exists
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return fmt.Errorf("video selection: check objects: %w", err)
	}
	for i, asset := range assets {
		if ready[i] {
			request.PreparedAssetIDs = append(request.PreparedAssetIDs, asset.ID)
		}
	}
	return nil
}

// projectSelection uses one SQL snapshot and the existing recovery ordering.
// It deliberately includes active singletons hidden by the public API filter.
func projectSelection(snapshot dvideo.SelectionSnapshot) (LoadEventAssetsOutput, error) {
	var out LoadEventAssetsOutput
	if snapshot.Removed {
		return out, nil
	}
	nodes := make(map[uuid.UUID]dvideo.SelectionNode)
	var live []dvideo.SelectionNode
	for _, node := range snapshot.Nodes {
		if node.Asset == nil || node.Asset.EventID != snapshot.EventID || node.Asset.FixtureID != snapshot.FixtureID {
			return out, fmt.Errorf("video selection: invalid snapshot asset")
		}
		nodes[node.Asset.ID] = node
		if node.Asset.SupersededBy == nil && node.Asset.ObjectReclaimedAt == nil && node.Share != nil && node.Share.State == dvideo.ShareStateActive {
			live = append(live, node)
		}
	}
	sort.Slice(live, func(i, j int) bool {
		return dvideo.CompareShares(live[i].Share, live[j].Share, live[i].Asset, live[j].Asset) < 0
	})
	selected := make(map[uuid.UUID]bool)
	for _, node := range live {
		asset := node.Asset
		selected[asset.ID] = true
		out.Assets = append(out.Assets, RestoredEventAsset{AssetID: asset.ID, MD5: hex.EncodeToString(asset.MD5),
			HashVersion: asset.FrameHashVersion, FrameHashes: asset.FrameHashes, Width: asset.Width, Height: asset.Height,
			DurationMS: asset.DurationMS, FileSizeBytes: asset.FileSizeBytes, Bitrate: asset.Bitrate, FrameRate: asset.FrameRate,
			Popularity: asset.Popularity, Verified: node.Share.TimestampVerified})
	}
	for _, node := range snapshot.Nodes {
		root := node.Asset
		visited := make(map[uuid.UUID]bool)
		for root.SupersededBy != nil {
			if visited[root.ID] {
				return out, fmt.Errorf("video selection: lineage cycle")
			}
			visited[root.ID] = true
			next, ok := nodes[*root.SupersededBy]
			if !ok {
				return out, fmt.Errorf("video selection: missing lineage target")
			}
			root = next.Asset
		}
		if selected[root.ID] {
			out.ExactAliases = append(out.ExactAliases, RestoredExactAlias{MD5: hex.EncodeToString(node.Asset.MD5), AssetID: root.ID})
		}
	}
	return out, nil
}
