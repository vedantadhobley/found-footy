// Selection graph validation keeps evidence, lineage and exact-source accounting separate.
package video

import (
	"encoding/hex"
	"fmt"
	"slices"
	"sort"

	"github.com/google/uuid"
)

// selectionGraph caches only direct comparisons within this immutable snapshot.
type selectionGraph struct {
	nodes    []SelectionNode
	index    map[uuid.UUID]int
	roots    []int
	eligible []bool
	selected []int
	policy   SelectionPolicy
	cache    map[[2]int]bool
}

// newSelectionGraph validates the complete input before deriving any selection.
func newSelectionGraph(s SelectionSnapshot, policy SelectionPolicy, prepared []uuid.UUID) (*selectionGraph, error) {
	if s.EventID == uuid.Nil || s.FixtureID <= 0 {
		return nil, fmt.Errorf("invalid selection scope")
	}
	g := &selectionGraph{nodes: slices.Clone(s.Nodes), index: make(map[uuid.UUID]int), policy: policy, cache: make(map[[2]int]bool)}
	for _, n := range g.nodes {
		if n.Asset == nil {
			return nil, fmt.Errorf("nil selection asset")
		}
	}
	sort.Slice(g.nodes, func(i, j int) bool {
		a, b := g.nodes[i].Asset, g.nodes[j].Asset
		if !a.FirstSeenAt.Equal(b.FirstSeenAt) {
			return a.FirstSeenAt.Before(b.FirstSeenAt)
		}
		return a.ID.String() < b.ID.String()
	})
	md5s := make(map[string]bool)
	for i, n := range g.nodes {
		a := n.Asset
		if err := a.ValidateInvariants(); err != nil {
			return nil, err
		}
		if a.EventID != s.EventID || a.FixtureID != s.FixtureID {
			return nil, fmt.Errorf("selection asset scope mismatch")
		}
		if _, exists := g.index[a.ID]; exists {
			return nil, fmt.Errorf("duplicate selection asset")
		}
		md5 := hex.EncodeToString(a.MD5)
		if md5s[md5] {
			return nil, fmt.Errorf("duplicate selection MD5")
		}
		md5s[md5], g.index[a.ID] = true, i
		if n.Share != nil && (n.Share.AssetID != a.ID || n.Share.EventID != s.EventID || !n.Share.State.Valid()) {
			return nil, fmt.Errorf("selection share scope or state mismatch")
		}
		if n.Validation != nil {
			if err := n.Validation.Validate(); err != nil {
				return nil, err
			}
			if n.Validation.EventID != s.EventID || n.Validation.FixtureID != s.FixtureID || n.Validation.MD5 != md5 {
				return nil, fmt.Errorf("selection validation scope mismatch")
			}
		}
	}
	ready := make(map[uuid.UUID]bool)
	for _, id := range prepared {
		if _, exists := g.index[id]; !exists {
			return nil, fmt.Errorf("prepared asset outside selection snapshot")
		}
		ready[id] = true
	}
	g.roots = make([]int, len(g.nodes))
	g.eligible = make([]bool, len(g.nodes))
	for i, n := range g.nodes {
		a := n.Asset
		known, _, _ := selectionAcceptance(n)
		g.eligible[i] = known && ready[a.ID] && a.ObjectReclaimedAt == nil
		if a.SupersededBy == nil {
			if n.Share == nil || n.Share.State != ShareStateActive || !g.eligible[i] {
				return nil, ErrSelectionMedia
			}
			g.selected = append(g.selected, i)
		} else if n.Share != nil && n.Share.State == ShareStateActive {
			return nil, fmt.Errorf("hidden asset has active share")
		}
		root, err := g.root(i)
		if err != nil {
			return nil, err
		}
		g.roots[i] = root
	}
	if err := g.validateCredits(s.Sources); err != nil {
		return nil, err
	}
	return g, nil
}

// root follows recorded ownership only; it never manufactures a perceptual edge.
func (g *selectionGraph) root(i int) (int, error) {
	seen := make(map[int]bool)
	for g.nodes[i].Asset.SupersededBy != nil {
		if seen[i] {
			return 0, fmt.Errorf("selection lineage cycle")
		}
		seen[i] = true
		next, exists := g.index[*g.nodes[i].Asset.SupersededBy]
		if !exists {
			return 0, fmt.Errorf("selection lineage outside event")
		}
		i = next
	}
	return i, nil
}

// validateCredits refuses missing source identities and inconsistent routing.
// Stored popularity is a derived, non-additive score, never a source-count checksum.
func (g *selectionGraph) validateCredits(sources []SelectionSource) error {
	seen := make(map[uuid.UUID]bool)
	observed := make([]int, len(g.nodes))
	for _, source := range sources {
		i, exists := g.index[source.ObservedAssetID]
		owner, ownerExists := g.index[source.CreditedAssetID]
		if source.ID == uuid.Nil || seen[source.ID] || !exists || !ownerExists || g.roots[i] != owner {
			return ErrSelectionCredits
		}
		seen[source.ID] = true
		observed[i]++
	}
	for i := range g.nodes {
		if observed[i] == 0 {
			return ErrSelectionCredits
		}
	}
	return nil
}

// matches evaluates retained acceptance/hash evidence, independently of playable
// media. Reclaimed bytes cannot be restored but do not erase accepted sightings.
func (g *selectionGraph) matches(i, j int) bool {
	if i == j {
		return true
	}
	key := [2]int{min(i, j), max(i, j)}
	if match, exists := g.cache[key]; exists {
		return match
	}
	a, b := g.nodes[i], g.nodes[j]
	ak, av, _ := selectionAcceptance(a)
	bk, bv, _ := selectionAcceptance(b)
	match := false
	if ak && bk && av == bv && NormalizeFrameHashVersion(a.Asset.FrameHashVersion) == NormalizeFrameHashVersion(b.Asset.FrameHashVersion) {
		p := g.policy
		match = Match(a.Asset.FrameHashes, b.Asset.FrameHashes, p.MaxHamming, p.MinRun, p.MaxGaps) ||
			p.LongMinRun > 0 && Match(a.Asset.FrameHashes, b.Asset.FrameHashes, p.LongMaxHamming, p.LongMinRun, p.LongMaxGaps)
	}
	g.cache[key] = match
	return match
}

// supported accepts only self-selection or a direct selected match, never a hidden intermediary.
func (g *selectionGraph) supported(observed int, selected []int) bool {
	for _, keeper := range selected {
		if g.matches(observed, keeper) {
			return true
		}
	}
	return false
}
