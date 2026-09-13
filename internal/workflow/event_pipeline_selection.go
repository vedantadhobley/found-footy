// Committed selection replaces both keeper metadata and exact aliases before the next candidate.
package workflow

import (
	"fmt"

	"github.com/google/uuid"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
)

// replaceSelection preserves surviving incumbent tournament order, then the
// incoming winner and restored roots. Receipt order never replaces current state.
func (p *pipeline) replaceSelection(out *videoactivity.PlacementSelectionOutput, incoming uuid.UUID) error {
	assets := make(map[uuid.UUID]videoactivity.RestoredEventAsset)
	md5s := make(map[string]bool)
	for _, asset := range out.State.Assets {
		if asset.AssetID == uuid.Nil || asset.MD5 == "" || asset.Popularity < 1 || md5s[asset.MD5] {
			return fmt.Errorf("invalid committed selection asset")
		}
		if _, exists := assets[asset.AssetID]; exists {
			return fmt.Errorf("repeated committed selection asset")
		}
		assets[asset.AssetID], md5s[asset.MD5] = asset, true
	}
	for _, alias := range out.State.ExactAliases {
		if _, exists := assets[alias.AssetID]; !exists || alias.MD5 == "" {
			return fmt.Errorf("invalid committed selection alias")
		}
	}
	var ordered []videoactivity.RestoredEventAsset
	appendID := func(id uuid.UUID) {
		if asset, exists := assets[id]; exists {
			ordered = append(ordered, asset)
			delete(assets, id)
		}
	}
	for _, old := range p.assets {
		appendID(old.assetID)
	}
	appendID(incoming)
	for _, id := range out.Restored {
		appendID(id)
	}
	// A retry may observe later independent commits; retain their current roots
	// too, in the same deterministic evidence order used by failed-run recovery.
	for _, asset := range out.State.Assets {
		appendID(asset.AssetID)
	}
	p.assets = nil
	p.exactRoots = make(map[string]uuid.UUID)
	p.restoreAssets(ordered, out.State.ExactAliases)
	return nil
}

// recordSelectedPlacement keeps historical processing counters separate from
// the live keeper count and the database's conserved source totals.
func (p *pipeline) recordSelectedPlacement(c clip, newWinner bool, losers int) {
	p.duplicates += len(c.exactFollowers)
	p.superseded += losers
	if !newWinner {
		p.duplicates++
	} else if c.verified {
		p.verified++
	} else {
		p.unverified++
	}
}
