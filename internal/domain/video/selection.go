// Reversible selection restores unsupported variants without changing keeper quality or visibility.
package video

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/google/uuid"

	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// SelectionVersion identifies restoration with non-additive direct source support.
const SelectionVersion = "direct-restoration-support-v2"

var (
	ErrSelectionStale   = errors.New("video: selection snapshot changed")
	ErrSelectionRemoved = errors.New("video: selection event removed")
	ErrSelectionCredits = errors.New("video: incomplete or inconsistent source attribution")
	ErrSelectionMedia   = errors.New("video: selected media not prepared")
)

// SelectionPolicy carries both recorded matcher routes; it does not tune their thresholds.
type SelectionPolicy struct {
	MaxHamming, MinRun, MaxGaps             int
	LongMaxHamming, LongMinRun, LongMaxGaps int
}

// Validate refuses malformed policy rather than silently normalizing a new decision.
func (p SelectionPolicy) Validate() error {
	valid := func(h, n, g int) bool { return h >= 0 && h <= 64 && n > 0 && g >= 0 && g < n }
	if !valid(p.MaxHamming, p.MinRun, p.MaxGaps) ||
		(p.LongMinRun != 0 && !valid(p.LongMaxHamming, p.LongMinRun, p.LongMaxGaps)) ||
		(p.LongMinRun == 0 && (p.LongMaxHamming != 0 || p.LongMaxGaps != 0)) {
		return fmt.Errorf("invalid selection matcher policy")
	}
	return nil
}

// SelectionNode carries its own acceptance: a share snapshot takes precedence;
// otherwise the repository supplies the earliest retained accepted evaluation.
type SelectionNode struct {
	Asset      *Asset
	Share      *Share
	Validation *dvision.Evidence
}

// SelectionSource is one distinct accepted source; zero IDs mean missing legacy attribution.
type SelectionSource struct {
	ID              uuid.UUID
	ObservedAssetID uuid.UUID
	CreditedAssetID uuid.UUID
}

// SelectionSnapshot is one consistent event read, before any proposed mutation.
type SelectionSnapshot struct {
	EventID   uuid.UUID
	FixtureID int64
	Removed   bool
	Nodes     []SelectionNode
	Sources   []SelectionSource
}

// Fingerprint protects planning/media preparation from changes to included inputs.
// Input order is not state, so SQL ordering cannot change the token.
func (s SelectionSnapshot) Fingerprint() (string, error) {
	s.Nodes = slices.Clone(s.Nodes)
	s.Sources = slices.Clone(s.Sources)
	for _, n := range s.Nodes {
		if n.Asset == nil {
			return "", fmt.Errorf("nil selection asset")
		}
	}
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].Asset.ID.String() < s.Nodes[j].Asset.ID.String() })
	sort.Slice(s.Sources, func(i, j int) bool { return s.Sources[i].ID.String() < s.Sources[j].ID.String() })
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// SelectionOwner supplies one destination for exact aliases and legacy credit pointers.
// Routing does not allocate popularity: one source can support several selected clips.
// Direct is false only for self ownership or preserved, ineligible legacy lineage.
type SelectionOwner struct {
	AssetID  uuid.UUID
	KeeperID uuid.UUID
	Direct   bool
}

// SelectedClip is a current root with direct source support and its own display evidence.
// Popularity counts a source once per clip, not once across the selected set.
type SelectedClip struct {
	AssetID         uuid.UUID
	Popularity      int
	Verified        bool
	ExtractedMinute *int
}

// SelectionPlan is complete for one snapshot. Restored roots are additive;
// this operation never retires a selected incumbent or exempts a singleton.
// Selected iteration order is neither public rank nor a new quality-tournament order.
type SelectionPlan struct {
	Version    string
	Owners     []SelectionOwner
	Selected   []SelectedClip
	Restored   []uuid.UUID
	Ineligible []uuid.UUID
}

// PlanSelection repairs topology after ordinary FF-092 placement. Prepared
// assets must have been checked in object storage by the caller; SQL metadata
// alone never proves availability. No I/O or caller-owned state is mutated.
func PlanSelection(s SelectionSnapshot, policy SelectionPolicy, prepared []uuid.UUID) (SelectionPlan, error) {
	plan := SelectionPlan{Version: SelectionVersion}
	if s.Removed {
		return plan, ErrSelectionRemoved
	}
	if err := policy.Validate(); err != nil {
		return plan, err
	}
	g, err := newSelectionGraph(s, policy, prepared)
	if err != nil {
		return plan, err
	}
	selected := slices.Clone(g.selected)
	for i, n := range g.nodes {
		if !g.eligible[i] {
			plan.Ineligible = append(plan.Ineligible, n.Asset.ID)
			continue
		}
		if !g.supported(i, selected) {
			selected = append(selected, i)
			plan.Restored = append(plan.Restored, n.Asset.ID)
		}
	}
	for i, n := range g.nodes {
		owner := g.roots[i]
		switch {
		case slices.Contains(selected, i):
			owner = i
		case !g.eligible[i]: // Keep unknown historical attribution on its existing root.
		case slices.Contains(selected, owner) && g.matches(i, owner):
		default:
			owner = -1
			for _, candidate := range selected {
				if g.matches(i, candidate) && (owner < 0 || IsUpgrade(selectionQuality(g.nodes[candidate]), selectionQuality(g.nodes[owner]))) {
					owner = candidate
				}
			}
		}
		if owner < 0 {
			return plan, fmt.Errorf("no selected owner for %s", n.Asset.ID)
		}
		plan.Owners = append(plan.Owners, SelectionOwner{AssetID: n.Asset.ID, KeeperID: g.nodes[owner].Asset.ID,
			Direct: i != owner && g.eligible[i] && g.matches(i, owner)})
	}
	// Aggregate exact observations first; compare each MD5/selected pair once.
	// Alias ownership, retired popularity and media availability are not votes.
	exact := make([]int, len(g.nodes))
	for _, source := range s.Sources {
		exact[g.index[source.ObservedAssetID]]++
	}
	for _, i := range selected {
		count := 0
		for observed, sources := range exact {
			if g.matches(observed, i) {
				count += sources
			}
		}
		if count < 1 {
			return plan, ErrSelectionCredits
		}
		_, verified, minute := selectionAcceptance(g.nodes[i])
		plan.Selected = append(plan.Selected, SelectedClip{AssetID: g.nodes[i].Asset.ID, Popularity: count,
			Verified: verified, ExtractedMinute: minute})
	}
	return plan, nil
}

// selectionQuality uses precisely the existing metadata comparator, not popularity or FPS.
func selectionQuality(n SelectionNode) ClipQuality {
	a := n.Asset
	return ClipQuality{DurationMS: a.DurationMS, Width: a.Width, Height: a.Height, Bitrate: a.Bitrate}
}

// selectionAcceptance never borrows a replacement's verification or timestamp.
func selectionAcceptance(n SelectionNode) (bool, bool, *int) {
	if n.Share != nil {
		return n.Share.State != ShareStateRemoved, n.Share.TimestampVerified, n.Share.ExtractedMinute
	}
	if n.Validation != nil {
		return true, n.Validation.Evaluation.Outcome == dvision.OutcomeVerified, n.Validation.Evaluation.MatchedMinute
	}
	return false, false, nil
}
