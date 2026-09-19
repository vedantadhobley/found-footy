// presentation.go — Offline quality-first primary and direct gap-candidate audit.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

const technicalMaterialMargin = 1.10

// presentationPolicy records experimental boundaries instead of presenting
// them as production or human quality judgments.
type presentationPolicy struct {
	PrimaryReliability      string  `json:"primary_reliability"`
	TechnicalRelation       string  `json:"technical_relation"`
	PrimaryContext          string  `json:"primary_context"`
	CandidateOrder          string  `json:"candidate_order"`
	MaterialMargin          float64 `json:"material_margin"`
	ReplacementEvidence     string  `json:"replacement_evidence"`
	UnverifiedWithoutAnchor string  `json:"unverified_without_anchor"`
	SupportRole             string  `json:"support_role"`
}

type presentationSummary struct {
	EventsWithPlayableRoots       int `json:"events_with_playable_roots"`
	MultiRootEvents               int `json:"multi_root_events"`
	EventsAffectedBySingletonRule int `json:"events_affected_by_singleton_rule"`
	HiddenRoots                   int `json:"hidden_roots"`
	HiddenVerifiedRoots           int `json:"hidden_verified_roots"`
	HiddenUnverifiedRoots         int `json:"hidden_unverified_roots"`
	HiddenWithoutDirectSubstitute int `json:"hidden_without_direct_substitute"`
	CurrentPrimaryTechnicalLosses int `json:"current_primary_technical_losses"`
	ClearPrimaryEvents            int `json:"clear_primary_events"`
	ClearPrimaryChangeCandidates  int `json:"clear_primary_change_candidates"`
	AmbiguousPrimaryEvents        int `json:"ambiguous_primary_events"`
	ManualReviewCandidates        int `json:"manual_review_candidates"`
}

type presentationAsset struct {
	AssetID                            string  `json:"asset_id"`
	ShareID                            string  `json:"share_id"`
	Verified                           bool    `json:"verified"`
	Width                              int     `json:"width"`
	Height                             int     `json:"height"`
	DurationMS                         int     `json:"duration_ms"`
	Bitrate                            int     `json:"bitrate"`
	FrameRate                          float64 `json:"frame_rate"`
	SpatialBitrateDensity              float64 `json:"spatial_bitrate_density"`
	DirectSupport                      int     `json:"direct_support"`
	ExactObservations                  int     `json:"exact_observations"`
	SourceTweetURL                     string  `json:"source_tweet_url,omitempty"`
	CurrentVisible                     bool    `json:"current_visible"`
	CurrentRank                        *int    `json:"current_rank,omitempty"`
	PrimaryTechnicalFrontier           bool    `json:"primary_technical_frontier"`
	PrimaryStandaloneFrontier          bool    `json:"primary_standalone_frontier"`
	TechnicallyDominatesCurrentPrimary bool    `json:"technically_dominates_current_primary"`
	DirectMatchCurrentPrimary          bool    `json:"direct_match_current_primary"`
}

type presentationDisposition struct {
	AssetID              string   `json:"asset_id"`
	Reason               string   `json:"reason"`
	DirectMatches        []string `json:"direct_matches,omitempty"`
	DirectSubstitutes    []string `json:"direct_substitutes,omitempty"`
	MaxCandidateCoverage float64  `json:"max_candidate_coverage"`
}

type presentationEvent struct {
	EventID                       string                    `json:"event_id"`
	FixtureID                     int64                     `json:"fixture_id"`
	EventLabel                    string                    `json:"event_label"`
	CurrentVisibleOrder           []string                  `json:"current_visible_order"`
	CurrentHidden                 []string                  `json:"current_hidden"`
	HiddenWithoutSubstitute       []string                  `json:"hidden_without_direct_substitute"`
	PrimaryStatus                 string                    `json:"primary_status"`
	PrimaryTechnicalFrontier      []string                  `json:"primary_technical_frontier"`
	PrimaryStandaloneFrontier     []string                  `json:"primary_standalone_frontier"`
	ProposedPrimary               string                    `json:"proposed_primary,omitempty"`
	CurrentPrimaryTechnicalLosses []string                  `json:"current_primary_technical_losses,omitempty"`
	ProposedCandidateLayers       [][]string                `json:"proposed_candidate_layers,omitempty"`
	Suppressed                    []presentationDisposition `json:"suppressed,omitempty"`
	ManualReview                  []presentationDisposition `json:"manual_review,omitempty"`
	Assets                        []presentationAsset       `json:"assets"`
}

type presentationReport struct {
	Experiment string              `json:"experiment"`
	Scope      string              `json:"scope"`
	Policy     presentationPolicy  `json:"policy"`
	Summary    presentationSummary `json:"summary"`
	Events     []presentationEvent `json:"events"`
}

// validatePresentationFlags keeps the event-level report independent of older
// pair, restoration and support experiments.
func validatePresentationFlags(enabled bool, other ...bool) error {
	if !enabled {
		return nil
	}
	for _, value := range other {
		if value {
			return fmt.Errorf("presentation-json cannot be combined with another output mode")
		}
	}
	return nil
}

// writePresentationJSON emits one deterministic report. It never changes the
// selected graph or claims that unsupported hashes prove useful new footage.
func writePresentationJSON(w io.Writer, assets []asset) error {
	report, err := planPresentationReport(assets)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("write presentation report: %w", err)
	}
	return nil
}

func planPresentationReport(assets []asset) (presentationReport, error) {
	report := presentationReport{
		Experiment: "quality-first-presentation-v1",
		Scope:      "playable_selected_roots; diagnostic_not_production_policy",
		Policy: presentationPolicy{
			PrimaryReliability:      "verified tier first; unverified only when no verified root exists",
			TechnicalRelation:       "pareto dominance over pixel area, encoded cadence and spatial bitrate density",
			PrimaryContext:          "capped duration is an independent Pareto signal; it cannot compensate for a material technical loss",
			CandidateOrder:          "technical Pareto layers after a clear primary; members of one layer are intentionally unordered",
			MaterialMargin:          technicalMaterialMargin,
			ReplacementEvidence:     "direct current dHash relation recomputed across selected roots plus cadence-aware stable-offset containment; never transitive and never verification inheritance",
			UnverifiedWithoutAnchor: "manual review; metadata alone cannot establish event identity",
			SupportRole:             "describe source recurrence and current visibility; never choose technical quality",
		},
	}
	byEvent := make(map[string][]asset)
	for _, item := range assets {
		if item.id == "" || item.eventID == "" || item.shareID == "" || item.shareState != "active" ||
			item.supersededBy != "" || item.objectReclaimedAt != "" || item.eventRemoved {
			continue
		}
		if len(item.frameHashes) == 0 || item.width <= 0 || item.height <= 0 || item.durationMS <= 0 || item.popularity < 1 {
			return report, fmt.Errorf("invalid playable presentation asset %q", item.id)
		}
		byEvent[item.eventID] = append(byEvent[item.eventID], item)
	}
	eventIDs := make([]string, 0, len(byEvent))
	for id := range byEvent {
		eventIDs = append(eventIDs, id)
	}
	sort.Strings(eventIDs)
	for _, id := range eventIDs {
		event, err := planPresentationEvent(byEvent[id])
		if err != nil {
			return report, fmt.Errorf("event %s: %w", id, err)
		}
		report.Events = append(report.Events, event)
		report.Summary.EventsWithPlayableRoots++
		if len(event.Assets) > 1 {
			report.Summary.MultiRootEvents++
		}
		if len(event.CurrentHidden) > 0 {
			report.Summary.EventsAffectedBySingletonRule++
		}
		report.Summary.HiddenRoots += len(event.CurrentHidden)
		for _, item := range event.Assets {
			if item.CurrentVisible {
				continue
			}
			if item.Verified {
				report.Summary.HiddenVerifiedRoots++
			} else {
				report.Summary.HiddenUnverifiedRoots++
			}
		}
		report.Summary.HiddenWithoutDirectSubstitute += len(event.HiddenWithoutSubstitute)
		if len(event.CurrentPrimaryTechnicalLosses) > 0 {
			report.Summary.CurrentPrimaryTechnicalLosses++
		}
		switch event.PrimaryStatus {
		case "clear":
			report.Summary.ClearPrimaryEvents++
			if len(event.CurrentPrimaryTechnicalLosses) > 0 && len(event.CurrentVisibleOrder) > 0 &&
				event.ProposedPrimary != event.CurrentVisibleOrder[0] {
				report.Summary.ClearPrimaryChangeCandidates++
			}
		case "ambiguous":
			report.Summary.AmbiguousPrimaryEvents++
		}
		report.Summary.ManualReviewCandidates += len(event.ManualReview)
	}
	return report, nil
}

func planPresentationEvent(roots []asset) (presentationEvent, error) {
	if len(roots) == 0 {
		return presentationEvent{}, fmt.Errorf("empty roots")
	}
	seen := make(map[string]bool, len(roots))
	for _, item := range roots {
		if seen[item.id] || item.eventID != roots[0].eventID {
			return presentationEvent{}, fmt.Errorf("duplicate or cross-event root %q", item.id)
		}
		seen[item.id] = true
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].id < roots[j].id })
	current := presentationCurrentOrder(roots)
	visible := make(map[string]int, len(current))
	for rank, item := range current {
		visible[item.id] = rank + 1
	}
	hidden := make([]string, 0, len(roots)-len(current))
	for _, item := range roots {
		if _, ok := visible[item.id]; !ok {
			hidden = append(hidden, item.id)
		}
	}

	tier := roots
	if slices.ContainsFunc(roots, func(item asset) bool { return item.verified }) {
		tier = nil
		for _, item := range roots {
			if item.verified {
				tier = append(tier, item)
			}
		}
	}
	technical := technicalFrontier(tier)
	standalone := standaloneFrontier(tier)
	event := presentationEvent{
		EventID: roots[0].eventID, FixtureID: roots[0].fixtureID,
		EventLabel: fmt.Sprintf("fixture=%d %s-%s player=%q minute=%s event=%s",
			roots[0].fixtureID, roots[0].homeTeam, roots[0].awayTeam, roots[0].playerName,
			eventMinute(roots[0]), roots[0].eventID),
		CurrentVisibleOrder: presentationIDs(current), CurrentHidden: hidden,
		PrimaryTechnicalFrontier:  presentationIDs(technical),
		PrimaryStandaloneFrontier: presentationIDs(standalone),
	}
	switch len(standalone) {
	case 1:
		event.PrimaryStatus, event.ProposedPrimary = "clear", standalone[0].id
	case 0:
		event.PrimaryStatus = "none"
	default:
		event.PrimaryStatus = "ambiguous"
	}
	if len(current) > 0 {
		for _, item := range tier {
			if technicalDominates(item, current[0]) {
				event.CurrentPrimaryTechnicalLosses = append(event.CurrentPrimaryTechnicalLosses, item.id)
			}
		}
		sort.Strings(event.CurrentPrimaryTechnicalLosses)
	}

	for _, hiddenID := range hidden {
		hiddenAsset := presentationAssetByID(roots, hiddenID)
		covered := false
		for _, shown := range current {
			if presentationSubstitutes(shown, hiddenAsset) {
				covered = true
				break
			}
		}
		if !covered {
			event.HiddenWithoutSubstitute = append(event.HiddenWithoutSubstitute, hiddenID)
		}
	}

	technicalSet := make(map[string]bool, len(technical))
	for _, item := range technical {
		technicalSet[item.id] = true
	}
	standaloneSet := make(map[string]bool, len(standalone))
	for _, item := range standalone {
		standaloneSet[item.id] = true
	}
	currentPrimary := asset{}
	if len(current) > 0 {
		currentPrimary = current[0]
	}
	for _, item := range roots {
		entry := presentationAsset{
			AssetID: item.id, ShareID: item.shareID, Verified: item.verified,
			Width: item.width, Height: item.height, DurationMS: item.durationMS,
			Bitrate: item.bitrate, FrameRate: item.frameRate,
			SpatialBitrateDensity: item.spatialBitrateDensity(), DirectSupport: item.popularity,
			ExactObservations: item.observedPopularity, SourceTweetURL: item.sourceTweetURL,
			PrimaryTechnicalFrontier:  technicalSet[item.id],
			PrimaryStandaloneFrontier: standaloneSet[item.id],
		}
		if rank, ok := visible[item.id]; ok {
			entry.CurrentVisible = true
			rankCopy := rank
			entry.CurrentRank = &rankCopy
		}
		if currentPrimary.id != "" {
			entry.TechnicallyDominatesCurrentPrimary = technicalDominates(item, currentPrimary)
			entry.DirectMatchCurrentPrimary = presentationDirectMatch(item, currentPrimary)
		}
		event.Assets = append(event.Assets, entry)
	}

	if event.PrimaryStatus == "clear" {
		selected := []asset{standalone[0]}
		for _, verified := range []bool{true, false} {
			candidates := make([]asset, 0, len(roots))
			for _, item := range roots {
				if item.id != standalone[0].id && item.verified == verified {
					candidates = append(candidates, item)
				}
			}
			for _, layer := range technicalLayers(candidates) {
				var additions []asset
				for _, candidate := range layer {
					disposition := presentationRelation(candidate, selected)
					switch {
					case len(disposition.DirectSubstitutes) > 0:
						disposition.Reason = "directly_covered_by_selected_clip"
						event.Suppressed = append(event.Suppressed, disposition)
					case candidate.verified:
						disposition.Reason = "verified_possible_uncovered_context"
						additions = append(additions, candidate)
					case len(disposition.DirectMatches) > 0:
						disposition.Reason = "unverified_direct_context"
						additions = append(additions, candidate)
					default:
						disposition.Reason = "unverified_without_direct_visual_anchor"
						event.ManualReview = append(event.ManualReview, disposition)
					}
				}
				selected = append(selected, additions...)
				if len(additions) > 0 {
					event.ProposedCandidateLayers = append(event.ProposedCandidateLayers, presentationIDs(additions))
				}
			}
		}
	} else {
		verifiedRoots := make([]asset, 0, len(roots))
		for _, item := range roots {
			if item.verified {
				verifiedRoots = append(verifiedRoots, item)
			}
		}
		for _, candidate := range roots {
			if candidate.verified {
				continue
			}
			disposition := presentationRelation(candidate, verifiedRoots)
			if len(disposition.DirectMatches) == 0 {
				disposition.Reason = "unverified_without_direct_visual_anchor"
				event.ManualReview = append(event.ManualReview, disposition)
			}
		}
	}
	return event, nil
}

func presentationCurrentOrder(roots []asset) []asset {
	verifiedThreshold, unverifiedThreshold := false, false
	for _, item := range roots {
		if item.popularity >= dvideo.PublicVisibilityPopularityThreshold {
			verifiedThreshold = verifiedThreshold || item.verified
			unverifiedThreshold = unverifiedThreshold || !item.verified
		}
	}
	visible := make([]asset, 0, len(roots))
	for _, item := range roots {
		hidden := item.popularity == dvideo.PublicVisibilitySingletonPopularity &&
			(verifiedThreshold || !item.verified && unverifiedThreshold)
		if !hidden {
			visible = append(visible, item)
		}
	}
	sort.Slice(visible, func(i, j int) bool {
		a, b := visible[i], visible[j]
		if a.verified != b.verified {
			return a.verified
		}
		if a.popularity != b.popularity {
			return a.popularity > b.popularity
		}
		if a.fileSizeBytes != b.fileSizeBytes {
			return a.fileSizeBytes > b.fileSizeBytes
		}
		if a.shareCreatedAt != b.shareCreatedAt {
			return a.shareCreatedAt < b.shareCreatedAt
		}
		return a.shareID < b.shareID
	})
	return visible
}

func technicalFrontier(items []asset) []asset {
	frontier := make([]asset, 0, len(items))
	for _, candidate := range items {
		dominated := false
		for _, other := range items {
			if candidate.id != other.id && technicalDominates(other, candidate) {
				dominated = true
				break
			}
		}
		if !dominated {
			frontier = append(frontier, candidate)
		}
	}
	sort.Slice(frontier, func(i, j int) bool { return frontier[i].id < frontier[j].id })
	return frontier
}

// standaloneFrontier keeps technical presentation and bounded duration as
// separate Pareto signals. A short sharp cut therefore does not silently beat
// a longer contextual clip, and a padded long clip cannot trade away quality.
func standaloneFrontier(items []asset) []asset {
	frontier := make([]asset, 0, len(items))
	for _, candidate := range items {
		dominated := false
		for _, other := range items {
			if candidate.id != other.id && standaloneDominates(other, candidate) {
				dominated = true
				break
			}
		}
		if !dominated {
			frontier = append(frontier, candidate)
		}
	}
	sort.Slice(frontier, func(i, j int) bool { return frontier[i].id < frontier[j].id })
	return frontier
}

func technicalLayers(items []asset) [][]asset {
	remaining := append([]asset(nil), items...)
	var layers [][]asset
	for len(remaining) > 0 {
		frontier := technicalFrontier(remaining)
		layers = append(layers, frontier)
		frontierIDs := make(map[string]bool, len(frontier))
		for _, item := range frontier {
			frontierIDs[item.id] = true
		}
		next := remaining[:0]
		for _, item := range remaining {
			if !frontierIDs[item.id] {
				next = append(next, item)
			}
		}
		remaining = next
	}
	return layers
}

// technicalDominates requires near-parity in every known signal and one
// material gain. Duration is intentionally absent: it is coverage, not quality.
func technicalDominates(candidate, incumbent asset) bool {
	if !notMateriallyWorseCadenceAware(candidate, incumbent) {
		return false
	}
	return materiallyBetter(float64(candidate.width*candidate.height), float64(incumbent.width*incumbent.height)) ||
		materiallyBetter(candidate.frameRate, incumbent.frameRate) ||
		materiallyBetter(candidate.spatialBitrateDensity(), incumbent.spatialBitrateDensity())
}

func standaloneDominates(candidate, incumbent asset) bool {
	if !notMateriallyWorseCadenceAware(candidate, incumbent) ||
		!retainsSignal(float64(cappedDuration(candidate)), float64(cappedDuration(incumbent))) {
		return false
	}
	return technicalDominates(candidate, incumbent) ||
		materiallyBetter(float64(cappedDuration(candidate)), float64(cappedDuration(incumbent)))
}

func materiallyBetter(candidate, incumbent float64) bool {
	return incumbent > 0 && candidate >= incumbent*technicalMaterialMargin
}

func presentationDirectMatch(left, right asset) bool {
	return left.id != right.id && left.eventID == right.eventID &&
		dvideo.CompatibleFrameHashVersions(left.hashVersion, right.hashVersion) &&
		measureMatcherEvidence(left, right).matches()
}

func presentationSubstitutes(selected, candidate asset) bool {
	if !presentationDirectMatch(selected, candidate) {
		return false
	}
	return evaluateCadenceAwareSubstitution(selected, candidate, measureMatcherEvidence(selected, candidate)).leftSubstitutesRight
}

func presentationRelation(candidate asset, selected []asset) presentationDisposition {
	out := presentationDisposition{AssetID: candidate.id}
	for _, existing := range selected {
		if !presentationDirectMatch(existing, candidate) {
			continue
		}
		out.DirectMatches = append(out.DirectMatches, existing.id)
		decision := evaluateCadenceAwareSubstitution(existing, candidate, measureMatcherEvidence(existing, candidate))
		if decision.rightCoverage > out.MaxCandidateCoverage {
			out.MaxCandidateCoverage = decision.rightCoverage
		}
		if decision.leftSubstitutesRight {
			out.DirectSubstitutes = append(out.DirectSubstitutes, existing.id)
		}
	}
	sort.Strings(out.DirectMatches)
	sort.Strings(out.DirectSubstitutes)
	return out
}

func presentationAssetByID(items []asset, id string) asset {
	for _, item := range items {
		if item.id == id {
			return item
		}
	}
	return asset{}
}

func presentationIDs(items []asset) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.id
	}
	return ids
}
