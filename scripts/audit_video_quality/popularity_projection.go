// Offline restoration projections use recorded aggregate observations without inventing source provenance.
package main

import (
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// popularityProjection applies the actual local planner under an explicit media
// assumption. It is not an executable repair: source IDs and credit rows were
// not exported, media is not fetched, and never-public validation stays unknown.
type popularityProjection struct {
	Assumption       string                   `json:"assumption"`
	Restored         []string                 `json:"restored_asset_ids"`
	Roots            []popularityRoot         `json:"roots"`
	Bridges          []popularityBridge       `json:"ambiguous_variants"`
	Visible          []string                 `json:"visible_asset_ids"`
	Moves            []popularityMove         `json:"single_variant_counterfactuals"`
	ConditionalMoves []popularityMove         `json:"conditional_moves_if_missing_own_acceptance_confirms_category"`
	DirectSupport    *directSupportComparison `json:"direct_support_comparison,omitempty"`
}

// projectPopularityRestoration converts reconciled aggregate counts into
// synthetic source identities for the pure planner. Counts themselves are real;
// complete candidate provenance and current media availability remain unproven.
func projectPopularityRestoration(items []asset) (popularityProjection, error) {
	return projectPopularityRestorationMode(items, false)
}

// projectPopularityRestorationMode optionally scores the fixed planner result
// with direct support. It never lets the alternative score change selection.
func projectPopularityRestorationMode(items []asset, direct bool) (popularityProjection, error) {
	result := popularityProjection{Assumption: "recorded_own_share_media_available; synthetic_source_ids_and_lineage_credits; never_public_acceptance_unknown"}
	s := dvideo.SelectionSnapshot{EventID: popularityIdentity(items[0].eventID, uuid.NameSpaceOID), FixtureID: items[0].fixtureID}
	ids := make(map[string]uuid.UUID)
	originalIDs := make(map[uuid.UUID]string)
	byID := make(map[string]asset)
	for _, a := range items {
		id := popularityIdentity(a.id, s.EventID)
		ids[a.id], originalIDs[id], byID[a.id] = id, a.id, a
	}
	var prepared []uuid.UUID
	for _, a := range items {
		md5, err := hex.DecodeString(a.md5)
		if err != nil {
			return result, err
		}
		observed, err := popularityObservationTime(a.firstSeenAt)
		if err != nil {
			return result, err
		}
		stored := dvideo.NewAsset(s.EventID, s.FixtureID, "offline-not-fetched", "offline-not-fetched",
			md5, a.hashVersion, a.frameHashes, a.width, a.height, a.durationMS, a.fileSizeBytes, observed)
		stored.ID, stored.Popularity, stored.Bitrate = ids[a.id], a.popularity, a.quality().Bitrate
		if a.supersededBy != "" {
			stored.SupersededBySet(ids[a.supersededBy])
		}
		if a.objectReclaimedAt != "" {
			stored.ObjectReclaimedAt = &observed // Presence is sufficient; never reclaims or restores real media.
		}
		node := dvideo.SelectionNode{Asset: stored}
		if a.shareID != "" {
			node.Share = &dvideo.Share{ID: a.shareID, AssetID: stored.ID, EventID: s.EventID,
				State: dvideo.ShareState(a.shareState), TimestampVerified: a.verified}
			if a.shareState != "removed" && a.objectReclaimedAt == "" {
				prepared = append(prepared, stored.ID)
			}
		}
		s.Nodes = append(s.Nodes, node)
		root, ok := popularityLineageRoot(a.id, byID)
		if !ok {
			return result, fmt.Errorf("invalid projected lineage")
		}
		for i := 0; i < a.observedPopularity; i++ {
			s.Sources = append(s.Sources, dvideo.SelectionSource{
				ID:              uuid.NewSHA1(stored.ID, []byte(fmt.Sprintf("synthetic-observation-%d", i))),
				ObservedAssetID: stored.ID, CreditedAssetID: ids[root]})
		}
	}
	policy := dvideo.SelectionPolicy{MaxHamming: primaryMaxHamming, MinRun: primaryMinRun, MaxGaps: primaryMaxGaps,
		LongMaxHamming: longMaxHamming, LongMinRun: longMinRun, LongMaxGaps: longMaxGaps}
	plan, err := dvideo.PlanSelection(s, policy, prepared)
	if err != nil {
		return result, err
	}
	owners := make(map[string]string)
	counts := make(map[string]int)
	for _, owner := range plan.Owners {
		owners[originalIDs[owner.AssetID]] = originalIDs[owner.KeeperID]
	}
	// Preserve the historical assigned-policy baseline even after the runtime
	// planner adopts direct support. Routing is still real planner output; only
	// this audit's counterfactual scores are reconstructed from exclusive owners.
	for _, a := range items {
		counts[owners[a.id]] += a.observedPopularity
	}
	projected := slices.Clone(items)
	for i := range projected {
		a := &projected[i]
		a.supersededBy = owners[a.id]
		if a.supersededBy == a.id {
			a.supersededBy, a.shareState, a.popularity = "", "active", counts[a.id]
		}
	}
	r := analyzePopularity(projected)
	if len(r.Excluded) != 0 {
		return result, fmt.Errorf("projection did not conserve root attribution: %v", r.Excluded)
	}
	for _, restored := range plan.Restored {
		result.Restored = append(result.Restored, originalIDs[restored])
	}
	slices.Sort(result.Restored)
	result.Roots, result.Bridges, result.Visible, result.Moves = r.Roots, r.Bridges, r.Visible, r.Moves
	result.ConditionalMoves = r.ConditionalMoves
	if direct {
		comparison := compareDirectSupport(projected)
		result.DirectSupport = &comparison
	}
	return result, nil
}

// popularityIdentity preserves real event/asset UUIDs and their stable tie
// order. Named synthetic test nodes use reproducible UUIDs only within the audit.
func popularityIdentity(raw string, namespace uuid.UUID) uuid.UUID {
	if id, err := uuid.Parse(raw); err == nil {
		return id
	}
	return uuid.NewSHA1(namespace, []byte(raw))
}

// popularityObservationTime accepts the two retained export formats, never
// inventing arrival order from CSV position or a missing timestamp.
func popularityObservationTime(raw string) (time.Time, error) {
	for _, format := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07"} {
		if parsed, err := time.Parse(format, raw); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid observation time %q", raw)
}
