// Historical source-support diagnostics keep ownership sensitivity separate from keeper quality.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// popularityRoot reports counts, not a new quality score. Ranges vary only
// known-own-share ambiguous variants and are not jointly attainable maxima.
type popularityRoot struct {
	AssetID  string `json:"asset_id"`
	ShareID  string `json:"share_id"`
	State    string `json:"share_state"`
	Verified bool   `json:"verified"`
	Exact    int    `json:"exact_observations"`
	Assigned int    `json:"recorded_popularity"`
	Minimum  *int   `json:"minimum_assigned"`
	Maximum  *int   `json:"maximum_assigned"`
}

// popularityBridge is a hidden MD5 with multiple directly matching roots.
// OwnShare is evidence availability, not proof of media availability or content.
type popularityBridge struct {
	AssetID           string   `json:"asset_id"`
	Exact             int      `json:"exact_observations"`
	Owner             string   `json:"lineage_owner"`
	OwnerDirect       bool     `json:"owner_is_direct"`
	OwnShare          bool     `json:"has_own_acceptance_share"`
	UnknownAcceptance bool     `json:"own_acceptance_unknown"`
	Reclaimed         bool     `json:"media_recorded_reclaimed"`
	PossibleOwners    []string `json:"direct_selected_owners"`
}

// popularityMove changes one MD5's owner, conserving every observation. It is
// a counterfactual, not a proposal to reassign every ambiguous historical source.
type popularityMove struct {
	AssetID            string         `json:"asset_id"`
	From               string         `json:"from"`
	To                 string         `json:"to"`
	Counts             map[string]int `json:"assigned_counts"`
	Visible            []string       `json:"visible_asset_ids"`
	VisibilityChanged  bool           `json:"visibility_changed"`
	StrictRankReversed bool           `json:"strict_rank_reversed"`
}

// popularityReport exposes aggregate reconciliation limits before offering any
// ownership counterfactual. Per-source provenance is absent from this export.
type popularityReport struct {
	Experiment        string                   `json:"experiment"`
	EventID           string                   `json:"event_id"`
	EventLabel        string                   `json:"event_label"`
	Assets            int                      `json:"assets"`
	MissingExact      int                      `json:"missing_exact_assets"`
	ExactTotal        int                      `json:"exact_total"`
	RootTotal         int                      `json:"root_total"`
	Excluded          []string                 `json:"score_exclusions"`
	UnsupportedHidden int                      `json:"hidden_without_direct_owner"`
	Roots             []popularityRoot         `json:"roots"`
	Bridges           []popularityBridge       `json:"ambiguous_variants"`
	Visible           []string                 `json:"recorded_visible_asset_ids"`
	Moves             []popularityMove         `json:"single_variant_counterfactuals"`
	ConditionalMoves  []popularityMove         `json:"conditional_moves_if_missing_own_acceptance_confirms_category"`
	Restoration       *popularityProjection    `json:"restoration_projection,omitempty"`
	DirectSupport     *directSupportComparison `json:"direct_support_comparison,omitempty"`
}

// validatePopularityFlags prevents mixing independent experiment output contracts.
func validatePopularityFlags(enabled bool, others ...bool) error {
	for _, other := range others {
		if enabled && other {
			return fmt.Errorf("popularity-json cannot be combined with other report modes")
		}
	}
	return nil
}

// writePopularityJSON validates the complete corpus before emitting sorted
// event reports. It needs no production connection, media access or new model.
func writePopularityJSON(w io.Writer, assets []asset) error {
	return writeSupportReport(w, assets, false)
}

// writeSupportReport preserves the earlier report mode while the explicit
// direct-support mode adds a different, non-additive scoring experiment.
func writeSupportReport(w io.Writer, assets []asset, direct bool) error {
	byEvent := make(map[string][]asset)
	ids := make(map[string]bool)
	md5s := make(map[string]bool)
	for _, a := range assets {
		if a.id == "" || a.eventID == "" || ids[a.id] || len(a.frameHashes) == 0 ||
			a.observedPopularity < 0 || a.popularity < 1 {
			return fmt.Errorf("popularity audit requires unique assets, hashes and nonnegative observations")
		}
		if a.hashVersion != dvideo.LegacyFrameHashVersion && a.hashVersion != dvideo.CurrentFrameHashVersion(0.1) {
			return fmt.Errorf("unsupported popularity hash version %q", a.hashVersion)
		}
		ids[a.id] = true
		key := a.eventID + ":" + a.md5
		if direct && (a.md5 == "" || md5s[key]) {
			return fmt.Errorf("direct support requires one retained row per event/MD5")
		}
		md5s[key] = true
		byEvent[a.eventID] = append(byEvent[a.eventID], a)
	}
	if len(assets) == 0 {
		return fmt.Errorf("empty popularity corpus")
	}
	var events []string
	for id := range byEvent {
		events = append(events, id)
	}
	slices.Sort(events)
	var reports []popularityReport
	for _, event := range events {
		members := byEvent[event]
		sort.Slice(members, func(i, j int) bool { return members[i].id < members[j].id })
		report := analyzePopularity(members)
		if len(report.Excluded) == 0 {
			projection, err := projectPopularityRestorationMode(members, direct)
			if err != nil {
				return fmt.Errorf("project restoration for %s: %w", event, err)
			}
			report.Restoration = &projection
			if direct {
				comparison := compareDirectSupport(members)
				report.DirectSupport = &comparison
			}
		}
		if direct {
			report.Experiment = "assigned-vs-direct-support-v1"
		}
		reports = append(reports, report)
	}
	encoder := json.NewEncoder(w)
	for _, report := range reports {
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("write popularity report: %w", err)
		}
	}
	return nil
}

// analyzePopularity holds the recorded selected set fixed. It does not infer
// fresh promotion eligibility or simulate unavailable historical validations.
func analyzePopularity(assets []asset) popularityReport {
	r := popularityReport{Experiment: "recorded-root-support-v1", EventID: assets[0].eventID,
		EventLabel: fmt.Sprintf("fixture=%d %s-%s player=%q minute=%d extra=%q",
			assets[0].fixtureID, assets[0].homeTeam, assets[0].awayTeam,
			assets[0].playerName, assets[0].minute, assets[0].extra), Assets: len(assets)}
	byID := make(map[string]asset)
	var roots []asset
	reasons := make(map[string]bool)
	for _, a := range assets {
		byID[a.id] = a
		if a.eventRemoved {
			reasons["removed_event"] = true
		}
		r.ExactTotal += a.observedPopularity
		if a.observedPopularity == 0 {
			r.MissingExact++
			reasons["missing_exact_attribution"] = true
		}
		if a.supersededBy == "" {
			roots = append(roots, a)
			r.RootTotal += a.popularity
			if a.shareID == "" || a.shareState != "active" {
				reasons["nonpublic_recorded_root"] = true
			}
			if a.objectReclaimedAt != "" {
				reasons["reclaimed_recorded_root"] = true
			}
		}
	}
	owners := make(map[string]string)
	lineageCounts := make(map[string]int)
	for _, a := range assets {
		owner, ok := popularityLineageRoot(a.id, byID)
		if !ok {
			reasons["missing_or_cyclic_lineage"] = true
			continue
		}
		owners[a.id] = owner
		lineageCounts[owner] += a.observedPopularity
	}
	for _, a := range roots {
		if lineageCounts[a.id] != a.popularity {
			reasons["per_root_count_mismatch"] = true
		}
		r.Roots = append(r.Roots, popularityRoot{AssetID: a.id, ShareID: a.shareID, State: a.shareState,
			Verified: a.verified, Exact: a.observedPopularity, Assigned: a.popularity})
	}
	for _, a := range assets {
		if a.supersededBy == "" {
			continue // Selected variants always own their exact observations.
		}
		bridge := popularityBridge{AssetID: a.id, Exact: a.observedPopularity, Owner: owners[a.id],
			OwnShare: a.shareID != "" && a.shareState == "superseded", Reclaimed: a.objectReclaimedAt != "",
			UnknownAcceptance: a.shareID == "" && a.shareState == "observed"}
		for _, root := range roots {
			if root.shareState != "active" || root.shareID == "" || root.objectReclaimedAt != "" || root.eventRemoved {
				continue
			}
			if popularityDirectMatch(a, root) {
				bridge.PossibleOwners = append(bridge.PossibleOwners, root.id)
				bridge.OwnerDirect = bridge.OwnerDirect || root.id == bridge.Owner
			}
		}
		if !bridge.OwnerDirect {
			r.UnsupportedHidden++
		}
		if len(bridge.PossibleOwners) > 1 {
			r.Bridges = append(r.Bridges, bridge)
		}
	}
	for reason := range reasons {
		r.Excluded = append(r.Excluded, reason)
	}
	slices.Sort(r.Excluded)
	if len(r.Excluded) > 0 {
		return r
	}
	for i := range r.Roots {
		minimum, maximum := r.Roots[i].Assigned, r.Roots[i].Assigned
		r.Roots[i].Minimum, r.Roots[i].Maximum = &minimum, &maximum
	}
	counts := make(map[string]int)
	for _, root := range roots {
		counts[root.id] = root.popularity
	}
	r.Visible = popularityVisible(roots, counts)
	for _, bridge := range r.Bridges {
		if bridge.Reclaimed || !bridge.OwnerDirect || (!bridge.OwnShare && !bridge.UnknownAcceptance) {
			continue // Do not manufacture eligibility or silently repair topology.
		}
		for i := range r.Roots {
			root := &r.Roots[i]
			if !bridge.OwnShare {
				continue // Known-evidence ranges exclude conditional alternatives.
			}
			if root.AssetID == bridge.Owner {
				*root.Minimum -= bridge.Exact
			} else if slices.Contains(bridge.PossibleOwners, root.AssetID) {
				*root.Maximum += bridge.Exact
			}
		}
		for _, next := range bridge.PossibleOwners {
			if next == bridge.Owner {
				continue
			}
			counts[bridge.Owner] -= bridge.Exact
			counts[next] += bridge.Exact
			visible := popularityVisible(roots, counts)
			move := popularityMove{AssetID: bridge.AssetID, From: bridge.Owner, To: next,
				Counts: make(map[string]int), Visible: visible,
				VisibilityChanged:  !slices.Equal(r.Visible, visible),
				StrictRankReversed: popularityRankReversed(roots, r.Visible, visible, counts)}
			for id, n := range counts {
				move.Counts[id] = n
			}
			if bridge.OwnShare {
				r.Moves = append(r.Moves, move)
			} else {
				r.ConditionalMoves = append(r.ConditionalMoves, move)
			}
			counts[bridge.Owner] += bridge.Exact
			counts[next] -= bridge.Exact
		}
	}
	return r
}

// popularityLineageRoot detects broken/cyclic ownership, including edges to
// another event (absent from this event-local map).
func popularityLineageRoot(id string, byID map[string]asset) (string, bool) {
	seen := make(map[string]bool)
	for {
		a, ok := byID[id]
		if !ok || seen[id] {
			return "", false
		}
		if a.supersededBy == "" {
			return id, true
		}
		seen[id] = true
		id = a.supersededBy
	}
}

// popularityDirectMatch uses the production routes without equating a path
// through hidden clips with direct matching or full-content substitution.
func popularityDirectMatch(a, b asset) bool {
	return a.eventID == b.eventID && a.verified == b.verified && a.hashVersion == b.hashVersion &&
		(dvideo.Match(a.frameHashes, b.frameHashes, primaryMaxHamming, primaryMinRun, primaryMaxGaps) ||
			dvideo.Match(a.frameHashes, b.frameHashes, longMaxHamming, longMinRun, longMaxGaps))
}

// popularityVisible mirrors FF-078's asymmetric filter and returns ID order,
// never a fabricated public rank based on missing share creation timestamps.
func popularityVisible(roots []asset, counts map[string]int) []string {
	verifiedThreshold, unverifiedThreshold := false, false
	for _, root := range roots {
		if counts[root.id] >= dvideo.PublicVisibilityPopularityThreshold {
			verifiedThreshold = verifiedThreshold || root.verified
			unverifiedThreshold = unverifiedThreshold || !root.verified
		}
	}
	var visible []string
	for _, root := range roots {
		hidden := counts[root.id] == dvideo.PublicVisibilitySingletonPopularity &&
			(verifiedThreshold || !root.verified && unverifiedThreshold)
		if !hidden {
			visible = append(visible, root.id)
		}
	}
	slices.Sort(visible)
	return visible
}

// popularityRankReversed counts only strict, provable order reversals between
// clips visible before and after. Missing share timestamps leave full ties unknown.
func popularityRankReversed(roots []asset, before, after []string, counts map[string]int) bool {
	for i, a := range roots {
		if !slices.Contains(before, a.id) || !slices.Contains(after, a.id) {
			continue
		}
		for _, b := range roots[i+1:] {
			if !slices.Contains(before, b.id) || !slices.Contains(after, b.id) {
				continue
			}
			old := popularityPartialOrder(a, b, a.popularity, b.popularity)
			next := popularityPartialOrder(a, b, counts[a.id], counts[b.id])
			if old*next < 0 {
				return true
			}
		}
	}
	return false
}

// popularityPartialOrder stops before the missing CreatedAt tiebreak, so zero
// means unresolvable from this export, not equal public rank.
func popularityPartialOrder(a, b asset, aCount, bCount int) int {
	if a.verified != b.verified {
		if a.verified {
			return -1
		}
		return 1
	}
	if aCount != bCount {
		if aCount > bCount {
			return -1
		}
		return 1
	}
	if a.fileSizeBytes > b.fileSizeBytes {
		return -1
	}
	if a.fileSizeBytes < b.fileSizeBytes {
		return 1
	}
	return 0
}
