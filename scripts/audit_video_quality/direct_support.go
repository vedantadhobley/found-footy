// Direct-support scoring experiments separate source evidence from exclusive representative ownership.
package main

import (
	"fmt"
	"slices"
	"sort"
)

// directSupportRoot compares two meanings on one fixed selected clip. Unknown
// categories stay conditional; source evidence does not require surviving media.
type directSupportRoot struct {
	AssetID     string `json:"asset_id"`
	ShareID     string `json:"share_id"`
	Verified    bool   `json:"verified"`
	Exact       int    `json:"exact_sources"`
	Assigned    int    `json:"assigned_support"`
	Known       int    `json:"direct_known_support"`
	Conditional int    `json:"direct_if_unknown_categories_confirmed"`
}

// directSupportWitness explains shared or unsupported attribution without
// exporting media/hashes again. One row represents all sources of that exact MD5.
type directSupportWitness struct {
	AssetID     string   `json:"asset_id"`
	Sources     int      `json:"sources"`
	AssignedTo  string   `json:"assigned_to"`
	Known       bool     `json:"own_acceptance_known"`
	Excluded    bool     `json:"removed_or_invalid_acceptance"`
	Matches     []string `json:"direct_selected_matches"`
	OwnerDirect bool     `json:"assigned_owner_is_direct"`
}

// directSupportEffect mirrors existing rank/filter policy, not full ranks: the
// export omits share creation timestamps needed to settle equal score/size ties.
type directSupportEffect struct {
	Visible         []string    `json:"visible_asset_ids"`
	Added           []string    `json:"newly_visible"`
	Hidden          []string    `json:"newly_hidden"`
	Reversals       [][2]string `json:"strict_order_reversals"`
	UnresolvedOrder [][2]string `json:"order_changes_with_unknown_tie"`
}

// directSupportComparison never treats per-clip support as conserved votes.
// ConditionalSupportTotal can exceed Sources when footage supports several clips.
type directSupportComparison struct {
	Policy                  string                 `json:"policy"`
	Sources                 int                    `json:"source_observations"`
	UnknownSources          int                    `json:"sources_without_own_acceptance"`
	ExcludedSources         int                    `json:"removed_or_invalid_acceptance_sources"`
	ConditionalCovered      int                    `json:"conditional_sources_with_any_direct_match"`
	ConditionalShared       int                    `json:"conditional_sources_matching_multiple_selected"`
	ConditionalSupportTotal int                    `json:"conditional_support_total_not_unique_sources"`
	Roots                   []directSupportRoot    `json:"roots"`
	Witnesses               []directSupportWitness `json:"shared_or_unsupported_sources"`
	AssignedVisible         []string               `json:"assigned_visible_asset_ids"`
	KnownEffect             directSupportEffect    `json:"known_evidence_only_effect"`
	ConditionalEffect       directSupportEffect    `json:"effect_if_unknown_categories_confirmed"`
}

// validateDirectSupportFlags gives the alternate scoring semantics an explicit
// opt-in, leaving earlier report contracts and their preserved evidence intact.
func validateDirectSupportFlags(enabled bool, others ...bool) error {
	for _, other := range others {
		if enabled && other {
			return fmt.Errorf("direct-support-json cannot be combined with other report modes")
		}
	}
	return nil
}

// compareDirectSupport consumes an already reconciled event snapshot. Every
// source contributes once per directly matching root, including its own root,
// with no traversal, popularity transfer, representative choice or quality fold.
func compareDirectSupport(items []asset) directSupportComparison {
	items = slices.Clone(items)
	sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })
	r := directSupportComparison{Policy: "direct-per-clip-v1; current-scoped-dhash; no-transitive-credit; non-additive; retained-acceptance-not-source-media-availability"}
	byID := make(map[string]asset)
	var roots []asset
	assigned, known, conditional := make(map[string]int), make(map[string]int), make(map[string]int)
	for _, a := range items {
		byID[a.id] = a
		if a.supersededBy == "" {
			roots = append(roots, a)
			assigned[a.id] = a.popularity
		}
	}
	for _, a := range items {
		r.Sources += a.observedPopularity
		hasOwn := a.shareID != "" && (a.shareState == "active" || a.shareState == "superseded")
		unknown := a.shareID == "" && a.shareState == "observed"
		if unknown {
			r.UnknownSources += a.observedPopularity
		}
		owner, _ := popularityLineageRoot(a.id, byID) // Caller already reconciled lineage.
		witness := directSupportWitness{AssetID: a.id, Sources: a.observedPopularity,
			AssignedTo: owner, Known: hasOwn, Excluded: !hasOwn && !unknown}
		if witness.Excluded {
			r.ExcludedSources += a.observedPopularity
		} else {
			for _, root := range roots {
				if a.id != root.id && !popularityDirectMatch(a, root) {
					continue
				}
				witness.Matches = append(witness.Matches, root.id)
				witness.OwnerDirect = witness.OwnerDirect || root.id == owner
				conditional[root.id] += a.observedPopularity
				if hasOwn {
					known[root.id] += a.observedPopularity
				}
			}
		}
		if len(witness.Matches) > 0 {
			r.ConditionalCovered += a.observedPopularity
		}
		if len(witness.Matches) > 1 {
			r.ConditionalShared += a.observedPopularity
		}
		if len(witness.Matches) > 1 || !witness.OwnerDirect {
			r.Witnesses = append(r.Witnesses, witness)
		}
	}
	for _, root := range roots {
		r.Roots = append(r.Roots, directSupportRoot{AssetID: root.id, ShareID: root.shareID, Verified: root.verified,
			Exact: root.observedPopularity, Assigned: root.popularity, Known: known[root.id], Conditional: conditional[root.id]})
		r.ConditionalSupportTotal += conditional[root.id]
	}
	r.AssignedVisible = popularityVisible(roots, assigned)
	r.KnownEffect = compareSupportEffect(roots, r.AssignedVisible, known)
	r.ConditionalEffect = compareSupportEffect(roots, r.AssignedVisible, conditional)
	return r
}

// compareSupportEffect separates visibility changes from provable pairwise
// reversals. Entering or leaving an unresolved tie is not a confirmed reversal.
func compareSupportEffect(roots []asset, before []string, counts map[string]int) directSupportEffect {
	effect := directSupportEffect{Visible: popularityVisible(roots, counts)}
	for _, id := range effect.Visible {
		if !slices.Contains(before, id) {
			effect.Added = append(effect.Added, id)
		}
	}
	for _, id := range before {
		if !slices.Contains(effect.Visible, id) {
			effect.Hidden = append(effect.Hidden, id)
		}
	}
	for i, a := range roots {
		if !slices.Contains(before, a.id) || !slices.Contains(effect.Visible, a.id) {
			continue
		}
		for _, b := range roots[i+1:] {
			if !slices.Contains(before, b.id) || !slices.Contains(effect.Visible, b.id) {
				continue
			}
			old, next := popularityPartialOrder(a, b, a.popularity, b.popularity), popularityPartialOrder(a, b, counts[a.id], counts[b.id])
			pair := [2]string{a.id, b.id}
			if old*next < 0 {
				effect.Reversals = append(effect.Reversals, pair)
			} else if old != next && (old == 0 || next == 0) {
				effect.UnresolvedOrder = append(effect.UnresolvedOrder, pair)
			}
		}
	}
	return effect
}
