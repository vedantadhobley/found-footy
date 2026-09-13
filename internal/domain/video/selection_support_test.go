// Direct-support tests separate accepted evidence, playable media and alias destinations.
package video

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// selectedSupport indexes derived scores without assuming public rank or selection order.
func selectedSupport(plan SelectionPlan) map[uuid.UUID]int {
	counts := make(map[uuid.UUID]int)
	for _, clip := range plan.Selected {
		counts[clip.AssetID] = clip.Popularity
	}
	return counts
}

// TestDirectSupportUsesAcceptanceNotMedia requires own evidence but not retained source bytes.
func TestDirectSupportUsesAcceptanceNotMedia(t *testing.T) {
	for _, mode := range []string{"known", "missing bytes", "reclaimed", "unknown", "removed", "different verification", "different hashes"} {
		t.Run(mode, func(t *testing.T) {
			s, policy, ids := selectionFixture()
			prepared := ids
			wantA, wantB := 7, 12
			switch mode {
			case "missing bytes":
				prepared = []uuid.UUID{ids[0], ids[2]}
			case "reclaimed":
				now := time.Now()
				s.Nodes[1].Asset.ObjectReclaimedAt = &now
			case "unknown":
				s.Nodes[1].Share = nil
				wantA, wantB = 2, 7
			case "removed":
				s.Nodes[1].Share.State = ShareStateRemoved
				wantA, wantB = 2, 7
			case "different verification":
				s.Nodes[1].Share.TimestampVerified = false
				wantA, wantB = 2, 7
			case "different hashes":
				s.Nodes[1].Asset.FrameHashVersion = "other-v1"
				wantA, wantB = 2, 7
			}
			plan, err := PlanSelection(s, policy, prepared)
			require.NoError(t, err)
			counts := selectedSupport(plan)
			require.Equal(t, wantA, counts[ids[0]])
			require.Equal(t, wantB, counts[ids[2]])
		})
	}
}

// TestDirectSupportRecomputesWithoutTransitiveCredit proves exact bumps and cached scores are not extra votes.
func TestDirectSupportRecomputesWithoutTransitiveCredit(t *testing.T) {
	s, policy, ids := selectionFixture()
	policy.LongMaxHamming, policy.LongMinRun = 0, 3 // Both routes match: still only one count.
	plan, err := PlanSelection(s, policy, ids)
	require.NoError(t, err)
	require.Equal(t, map[uuid.UUID]int{ids[0]: 7, ids[2]: 12}, selectedSupport(plan))
	// Apply the topology and deliberately stale derived counters. A new plan
	// must use source identities, not require or add last placement's aggregates.
	for i := range s.Nodes {
		s.Nodes[i].Asset.SupersededBySet(ids[2])
		s.Nodes[i].Asset.Popularity = 999
	}
	s.Nodes[0].Asset.SupersededBy = nil
	s.Nodes[0].Share.State = ShareStateActive
	s.Nodes[2].Asset.SupersededBy = nil
	for i := range s.Sources {
		if s.Sources[i].ObservedAssetID == ids[0] {
			s.Sources[i].CreditedAssetID = ids[0]
		}
	}
	plan, err = PlanSelection(s, policy, ids)
	require.NoError(t, err)
	require.Empty(t, plan.Restored)
	require.Equal(t, map[uuid.UUID]int{ids[0]: 7, ids[2]: 12}, selectedSupport(plan))
	// A is connected to B only through C. Another A source must not support B.
	s.Sources = append(s.Sources, SelectionSource{ID: uuid.New(), ObservedAssetID: ids[0], CreditedAssetID: ids[0]})
	plan, err = PlanSelection(s, policy, ids)
	require.NoError(t, err)
	require.Equal(t, map[uuid.UUID]int{ids[0]: 8, ids[2]: 12}, selectedSupport(plan))
	// A bridge recurrence supports both, even though its alias routes only to B.
	s.Sources = append(s.Sources, SelectionSource{ID: uuid.New(), ObservedAssetID: ids[1], CreditedAssetID: ids[2]})
	plan, err = PlanSelection(s, policy, ids)
	require.NoError(t, err)
	require.Equal(t, map[uuid.UUID]int{ids[0]: 9, ids[2]: 13}, selectedSupport(plan))
}

// TestDirectSupportExactIdentityDoesNotRequireLongHashes preserves short exact variants.
func TestDirectSupportExactIdentityDoesNotRequireLongHashes(t *testing.T) {
	s, policy, ids := selectionFixture()
	for i := range s.Nodes {
		s.Nodes[i].Asset.FrameHashes = []uint64{0}
	}
	plan, err := PlanSelection(s, policy, ids)
	require.NoError(t, err)
	require.Equal(t, map[uuid.UUID]int{ids[0]: 2, ids[1]: 5, ids[2]: 7}, selectedSupport(plan))
}
