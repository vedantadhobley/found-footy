// coverage_sweep.go — Exhaustive direct-section coverage experiments for FF-081.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

const currentHashCadenceMS = 100

type coverageTolerance struct {
	Name          string  `json:"name"`
	MaxTotalMS    int     `json:"max_total_uncovered_ms"`
	MaxFraction   float64 `json:"max_uncovered_fraction"`
	MaxInteriorMS int     `json:"max_interior_gap_ms"`
}

var coverageTolerances = []coverageTolerance{
	{Name: "full_section_span", MaxTotalMS: 0, MaxFraction: 0, MaxInteriorMS: 0},
	{Name: "tight_500ms_5pct", MaxTotalMS: 500, MaxFraction: 0.05, MaxInteriorMS: 200},
	{Name: "edge_1s_5pct", MaxTotalMS: 1000, MaxFraction: 0.05, MaxInteriorMS: 200},
	{Name: "edge_1s_10pct", MaxTotalMS: 1000, MaxFraction: 0.10, MaxInteriorMS: 200},
	{Name: "bounded_1500ms_10pct", MaxTotalMS: 1500, MaxFraction: 0.10, MaxInteriorMS: 500},
	{Name: "wide_2s_10pct", MaxTotalMS: 2000, MaxFraction: 0.10, MaxInteriorMS: 500},
}

type candidateCoverage struct {
	AssetID               string          `json:"asset_id"`
	EventID               string          `json:"event_id"`
	FixtureID             int64           `json:"fixture_id,omitempty"`
	EventLabel            string          `json:"event_label,omitempty"`
	CurrentEligible       bool            `json:"current_eligible"`
	HashCadenceMS         *int            `json:"hash_cadence_ms"`
	DirectSelectedAssets  []string        `json:"direct_selected_assets"`
	QualifiedOffsets      int             `json:"qualified_offsets"`
	TotalSamples          int             `json:"total_samples"`
	CoveredSamples        int             `json:"covered_samples"`
	UncoveredSamples      int             `json:"uncovered_samples"`
	UncoveredFraction     float64         `json:"uncovered_fraction"`
	UnsupportedPrefix     int             `json:"unsupported_prefix_samples"`
	UnsupportedSuffix     int             `json:"unsupported_suffix_samples"`
	InteriorGaps          []frameInterval `json:"interior_gaps"`
	RequiresSelectedUnion bool            `json:"requires_selected_union"`
}

type coverageSweepResult struct {
	Tolerance                      coverageTolerance `json:"tolerance"`
	SelectedRoots                  int               `json:"selected_roots"`
	CoveredRoots                   int               `json:"covered_roots"`
	CoveredByMultipleSelected      int               `json:"covered_by_multiple_selected"`
	KeptPossibleContext            int               `json:"kept_possible_context"`
	KeptUnknownCadence             int               `json:"kept_unknown_cadence"`
	SelectedCountHistogram         map[string]int    `json:"selected_count_histogram"`
	EventsOverThreeSelected        int               `json:"events_over_three_selected"`
	MaxSelectedForEvent            int               `json:"max_selected_for_event"`
	EligibleSelectedRoots          int               `json:"eligible_selected_roots"`
	EligibleCoveredRoots           int               `json:"eligible_covered_roots"`
	EligibleSelectedCountHistogram map[string]int    `json:"eligible_selected_count_histogram"`
	EligibleEventsOverThree        int               `json:"eligible_events_over_three_selected"`
	EligibleMaxSelectedForEvent    int               `json:"eligible_max_selected_for_event"`
}

type coverageSweepSummary struct {
	PlayableRoots                  int            `json:"playable_roots"`
	Events                         int            `json:"events"`
	ClearPrimaryEvents             int            `json:"clear_primary_events"`
	AmbiguousPrimaryEvents         int            `json:"ambiguous_primary_events"`
	NoPrimaryEvents                int            `json:"no_primary_events"`
	EligibleClearPrimaryEvents     int            `json:"eligible_clear_primary_events"`
	EligibleAmbiguousPrimaryEvents int            `json:"eligible_ambiguous_primary_events"`
	EligibleNoPrimaryEvents        int            `json:"eligible_no_primary_events"`
	CurrentVisibleRoots            int            `json:"current_visible_roots"`
	CurrentVisibleCountHistogram   map[string]int `json:"current_visible_count_histogram"`
	CurrentEventsOverThree         int            `json:"current_events_over_three_visible"`
	CurrentMaxVisibleForEvent      int            `json:"current_max_visible_for_event"`
}

type coverageSweepReport struct {
	Experiment              string                         `json:"experiment"`
	Scope                   string                         `json:"scope"`
	Anchor                  map[string]any                 `json:"anchor_policy"`
	Sections                map[string]any                 `json:"section_policy"`
	Summary                 coverageSweepSummary           `json:"summary"`
	Results                 []coverageSweepResult          `json:"results"`
	CoveredExamples         map[string][]candidateCoverage `json:"covered_examples"`
	EligibleCoveredExamples map[string][]candidateCoverage `json:"eligible_covered_examples"`
}

type coveragePairDirection struct {
	CandidateID string            `json:"candidate_id"`
	SelectedID  string            `json:"selected_id"`
	Coverage    candidateCoverage `json:"coverage"`
	CoveredAt   map[string]bool   `json:"covered_at"`
}

type coveragePairCase struct {
	ID                 string                `json:"id"`
	EventLabel         string                `json:"event_label"`
	Human              reviewedHumanJudgment `json:"human"`
	LeftCoveredByRight coveragePairDirection `json:"left_covered_by_right"`
	RightCoveredByLeft coveragePairDirection `json:"right_covered_by_left"`
}

type coveragePairReport struct {
	Experiment string             `json:"experiment"`
	CapturedAt string             `json:"captured_at"`
	Cases      []coveragePairCase `json:"cases"`
}

type coverageRoute struct {
	name       string
	maxHamming int
	minRun     int
	maxGaps    int
}

var exhaustiveCoverageRoutes = []coverageRoute{
	{name: "primary", maxHamming: primaryMaxHamming, minRun: primaryMinRun, maxGaps: primaryMaxGaps},
	{name: "sustained", maxHamming: longMaxHamming, minRun: longMinRun, maxGaps: longMaxGaps},
}

// validateCoverageSweepFlags keeps the exhaustive experiment independent from
// older report formats and the reviewed-pair input contract.
func validateCoverageSweepFlags(enabled bool, other ...bool) error {
	if !enabled {
		return nil
	}
	for _, value := range other {
		if value {
			return fmt.Errorf("coverage-sweep-json cannot be combined with another output mode")
		}
	}
	return nil
}

// validateCoveragePairFlags keeps reviewed JSON input distinct from CSV report
// modes so a caller cannot accidentally decode one corpus as the other.
func validateCoveragePairFlags(enabled bool, other ...bool) error {
	if !enabled {
		return nil
	}
	for _, value := range other {
		if value {
			return fmt.Errorf("coverage-pair-json cannot be combined with another output mode")
		}
	}
	return nil
}

// writeCoverageSweepJSON emits a deterministic policy comparison. It does not
// modify graph topology or claim that unsupported samples are useful content.
func writeCoverageSweepJSON(w io.Writer, assets []asset) error {
	report, err := planCoverageSweep(assets)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("write coverage sweep: %w", err)
	}
	return nil
}

// writeCoveragePairJSON applies the same exhaustive direct-section evidence to
// a frozen human-review corpus without changing its labels.
func writeCoveragePairJSON(w io.Writer, r io.Reader) error {
	const maxCorpusBytes = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(r, maxCorpusBytes+1))
	if err != nil {
		return fmt.Errorf("read pair corpus: %w", err)
	}
	if len(raw) > maxCorpusBytes {
		return fmt.Errorf("pair corpus exceeds 32 MiB")
	}
	var corpus reviewedPairCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		return fmt.Errorf("decode pair corpus: %w", err)
	}
	wantMatcher := reviewedMatcher{
		Primary:   reviewedMatchRoute{primaryMaxHamming, primaryMinRun, primaryMaxGaps},
		Sustained: reviewedMatchRoute{longMaxHamming, longMinRun, longMaxGaps},
	}
	if corpus.SchemaVersion != 1 || corpus.HashCadenceMS != currentHashCadenceMS ||
		corpus.CapturedAt == "" || corpus.Matcher != wantMatcher || len(corpus.Cases) == 0 {
		return fmt.Errorf("coverage pair corpus requires schema v1, 100-ms hashes, current matcher and nonempty cases")
	}
	report := coveragePairReport{Experiment: "exhaustive-direct-pair-coverage-v1", CapturedAt: corpus.CapturedAt}
	seen := make(map[string]bool)
	for _, pair := range corpus.Cases {
		if pair.ID == "" || seen[pair.ID] || pair.Left.EventID == "" || pair.Left.EventID != pair.Right.EventID {
			return fmt.Errorf("invalid pair identity %q", pair.ID)
		}
		seen[pair.ID] = true
		left, right := reviewedAssetForPolicy(pair.Left), reviewedAssetForPolicy(pair.Right)
		left.frameHashes, err = decodeFrameHashes(pair.Left.FrameHashes)
		if err != nil {
			return fmt.Errorf("pair %s left: %w", pair.ID, err)
		}
		right.frameHashes, err = decodeFrameHashes(pair.Right.FrameHashes)
		if err != nil {
			return fmt.Errorf("pair %s right: %w", pair.ID, err)
		}
		report.Cases = append(report.Cases, coveragePairCase{
			ID: pair.ID, EventLabel: pair.EventLabel, Human: pair.Human,
			LeftCoveredByRight: coveragePairDirectionFor(left, right),
			RightCoveredByLeft: coveragePairDirectionFor(right, left),
		})
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("write pair coverage: %w", err)
	}
	return nil
}

// coveragePairDirectionFor measures content coverage without applying a
// quality verdict; human presentation labels remain an independent field.
func coveragePairDirectionFor(candidate, selected asset) coveragePairDirection {
	profile := measureCandidateCoverage(candidate, []asset{selected})
	out := coveragePairDirection{
		CandidateID: candidate.id, SelectedID: selected.id, Coverage: profile,
		CoveredAt: make(map[string]bool, len(coverageTolerances)),
	}
	for _, tolerance := range coverageTolerances {
		out.CoveredAt[tolerance.Name] = coverageWithinTolerance(profile, tolerance)
	}
	return out
}

// planCoverageSweep compares direct coverage before and after the current
// public eligibility rule without mutating the retained graph.
func planCoverageSweep(assets []asset) (coverageSweepReport, error) {
	report := coverageSweepReport{
		Experiment: "exhaustive-direct-coverage-v1",
		Scope:      "playable_selected_roots; quality_first_primary; direct_edges_only; diagnostic_not_production_policy",
		Anchor: map[string]any{
			"primary":   map[string]int{"max_hamming": primaryMaxHamming, "min_run": primaryMinRun, "max_gaps": primaryMaxGaps},
			"sustained": map[string]int{"max_hamming": longMaxHamming, "min_run": longMinRun, "max_gaps": longMaxGaps},
			"offsets":   "every independently qualifying offset; never transitive",
		},
		Sections: map[string]any{
			"max_gap_samples": overlapGapFrames, "min_section_samples": overlapMinFrames,
			"min_section_similarity": overlapMinSimilarity, "sample_cadence_ms": currentHashCadenceMS,
		},
		CoveredExamples:         make(map[string][]candidateCoverage),
		EligibleCoveredExamples: make(map[string][]candidateCoverage),
	}
	byEvent := make(map[string][]asset)
	for _, item := range assets {
		if item.id == "" || item.eventID == "" || item.shareID == "" || item.shareState != "active" ||
			item.supersededBy != "" || item.objectReclaimedAt != "" || item.eventRemoved {
			continue
		}
		byEvent[item.eventID] = append(byEvent[item.eventID], item)
		report.Summary.PlayableRoots++
	}
	eventIDs := make([]string, 0, len(byEvent))
	for id := range byEvent {
		eventIDs = append(eventIDs, id)
	}
	sort.Strings(eventIDs)
	report.Summary.Events = len(eventIDs)
	report.Summary.CurrentVisibleCountHistogram = make(map[string]int)
	for _, eventID := range eventIDs {
		count := len(presentationCurrentOrder(byEvent[eventID]))
		report.Summary.CurrentVisibleRoots += count
		report.Summary.CurrentVisibleCountHistogram[strconv.Itoa(count)]++
		report.Summary.CurrentMaxVisibleForEvent = max(report.Summary.CurrentMaxVisibleForEvent, count)
		if count > 3 {
			report.Summary.CurrentEventsOverThree++
		}
	}
	for _, tolerance := range coverageTolerances {
		result := coverageSweepResult{
			Tolerance: tolerance, SelectedCountHistogram: make(map[string]int),
			EligibleSelectedCountHistogram: make(map[string]int),
		}
		for _, eventID := range eventIDs {
			roots := append([]asset(nil), byEvent[eventID]...)
			eligible := presentationCurrentOrder(roots)
			eligibleIDs := make(map[string]bool, len(eligible))
			for _, item := range eligible {
				eligibleIDs[item.id] = true
			}
			selected, covered, unknown, status, err := planCoverageEvent(roots, tolerance)
			if err != nil {
				return report, fmt.Errorf("event %s: %w", eventID, err)
			}
			if tolerance.Name == coverageTolerances[0].Name {
				switch status {
				case "clear":
					report.Summary.ClearPrimaryEvents++
				case "ambiguous":
					report.Summary.AmbiguousPrimaryEvents++
				default:
					report.Summary.NoPrimaryEvents++
				}
			}
			result.SelectedRoots += selected
			result.SelectedCountHistogram[strconv.Itoa(selected)]++
			result.MaxSelectedForEvent = max(result.MaxSelectedForEvent, selected)
			if selected > 3 {
				result.EventsOverThreeSelected++
			}
			result.CoveredRoots += len(covered)
			result.KeptUnknownCadence += unknown
			result.KeptPossibleContext += selected - 1
			for _, item := range covered {
				if item.RequiresSelectedUnion {
					result.CoveredByMultipleSelected++
				}
			}
			for i := range covered {
				covered[i].CurrentEligible = eligibleIDs[covered[i].AssetID]
			}
			report.CoveredExamples[tolerance.Name] = append(
				report.CoveredExamples[tolerance.Name], covered...,
			)

			eligibleSelected, eligibleCovered, _, eligibleStatus, err := planCoverageEvent(eligible, tolerance)
			if err != nil {
				return report, fmt.Errorf("eligible event %s: %w", eventID, err)
			}
			if tolerance.Name == coverageTolerances[0].Name {
				switch eligibleStatus {
				case "clear":
					report.Summary.EligibleClearPrimaryEvents++
				case "ambiguous":
					report.Summary.EligibleAmbiguousPrimaryEvents++
				default:
					report.Summary.EligibleNoPrimaryEvents++
				}
			}
			result.EligibleSelectedRoots += eligibleSelected
			result.EligibleCoveredRoots += len(eligibleCovered)
			result.EligibleSelectedCountHistogram[strconv.Itoa(eligibleSelected)]++
			result.EligibleMaxSelectedForEvent = max(result.EligibleMaxSelectedForEvent, eligibleSelected)
			if eligibleSelected > 3 {
				result.EligibleEventsOverThree++
			}
			for i := range eligibleCovered {
				eligibleCovered[i].CurrentEligible = true
			}
			report.EligibleCoveredExamples[tolerance.Name] = append(
				report.EligibleCoveredExamples[tolerance.Name], eligibleCovered...,
			)
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}

// planCoverageEvent chooses an unambiguous quality/context primary, then keeps
// or covers later Pareto layers without allowing peers to suppress each other.
func planCoverageEvent(
	roots []asset,
	tolerance coverageTolerance,
) (selectedCount int, covered []candidateCoverage, unknownCadence int, status string, err error) {
	if len(roots) == 0 {
		return 0, nil, 0, "none", fmt.Errorf("empty roots")
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].id < roots[j].id })
	tier := roots
	verified := false
	for _, item := range roots {
		verified = verified || item.verified
	}
	if verified {
		tier = nil
		for _, item := range roots {
			if item.verified {
				tier = append(tier, item)
			}
		}
	}
	primary := standaloneFrontier(tier)
	if len(primary) != 1 {
		if len(primary) > 1 {
			return len(roots), nil, 0, "ambiguous", nil
		}
		return len(roots), nil, 0, "none", nil
	}
	selected := []asset{primary[0]}
	for _, verifiedTier := range []bool{true, false} {
		candidates := make([]asset, 0, len(roots))
		for _, item := range roots {
			if item.id != primary[0].id && item.verified == verifiedTier {
				candidates = append(candidates, item)
			}
		}
		for _, layer := range technicalLayers(candidates) {
			additions := make([]asset, 0, len(layer))
			for _, candidate := range layer {
				profile := measureCandidateCoverage(candidate, coverageQualitySubstitutes(candidate, selected))
				if profile.HashCadenceMS == nil {
					unknownCadence++
					additions = append(additions, candidate)
					continue
				}
				if coverageWithinTolerance(profile, tolerance) {
					if len(profile.DirectSelectedAssets) > 1 {
						profile.RequiresSelectedUnion = true
						for _, existing := range coverageQualitySubstitutes(candidate, selected) {
							if coverageWithinTolerance(measureCandidateCoverage(candidate, []asset{existing}), tolerance) {
								profile.RequiresSelectedUnion = false
								break
							}
						}
					}
					covered = append(covered, profile)
					continue
				}
				additions = append(additions, candidate)
			}
			selected = append(selected, additions...)
		}
	}
	return len(selected), covered, unknownCadence, "clear", nil
}

// coverageQualitySubstitutes keeps content coverage and quality separate. A
// selected clip may hide a covered candidate only when it also retains every
// known technical signal within the accepted quality floor.
func coverageQualitySubstitutes(candidate asset, selected []asset) []asset {
	out := make([]asset, 0, len(selected))
	for _, existing := range selected {
		if notMateriallyWorseCadenceAware(existing, candidate) {
			out = append(out, existing)
		}
	}
	return out
}

// measureCandidateCoverage maps direct qualified sections onto one candidate's
// timeline. Callers decide which selected clips are valid quality substitutes.
func measureCandidateCoverage(candidate asset, selected []asset) candidateCoverage {
	eventLabel := ""
	if candidate.fixtureID != 0 || candidate.homeTeam != "" || candidate.awayTeam != "" || candidate.playerName != "" {
		eventLabel = fmt.Sprintf("%s-%s player=%q minute=%s", candidate.homeTeam, candidate.awayTeam,
			candidate.playerName, eventMinute(candidate))
	}
	profile := candidateCoverage{
		AssetID: candidate.id, EventID: candidate.eventID, FixtureID: candidate.fixtureID,
		EventLabel:           eventLabel,
		TotalSamples:         len(candidate.frameHashes),
		DirectSelectedAssets: []string{}, InteriorGaps: []frameInterval{},
	}
	if candidate.hashVersion == dvideo.CurrentFrameHashVersion(0.1) {
		cadence := currentHashCadenceMS
		profile.HashCadenceMS = &cadence
	}
	intervals := make([]frameInterval, 0)
	for _, existing := range selected {
		if existing.eventID != candidate.eventID || !dvideo.CompatibleFrameHashVersions(candidate.hashVersion, existing.hashVersion) {
			continue
		}
		routes := exhaustiveDirectOverlap(candidate, existing)
		if len(routes) == 0 {
			continue
		}
		profile.DirectSelectedAssets = append(profile.DirectSelectedAssets, existing.id)
		profile.QualifiedOffsets += len(routes)
		for _, route := range routes {
			for _, section := range route.Sections {
				intervals = append(intervals, section.Left)
			}
		}
	}
	sort.Strings(profile.DirectSelectedAssets)
	merged := mergeFrameIntervals(intervals, profile.TotalSamples)
	for _, interval := range merged {
		profile.CoveredSamples += interval.End - interval.Start
	}
	profile.UncoveredSamples = max(0, profile.TotalSamples-profile.CoveredSamples)
	profile.UncoveredFraction = coverage(profile.UncoveredSamples, profile.TotalSamples)
	if len(merged) == 0 {
		profile.UnsupportedPrefix = profile.TotalSamples
		return profile
	}
	profile.UnsupportedPrefix = merged[0].Start
	profile.UnsupportedSuffix = profile.TotalSamples - merged[len(merged)-1].End
	for i := 1; i < len(merged); i++ {
		if merged[i-1].End < merged[i].Start {
			profile.InteriorGaps = append(profile.InteriorGaps, frameInterval{merged[i-1].End, merged[i].Start})
		}
	}
	return profile
}

// coverageWithinTolerance accepts only explicit-cadence evidence within every
// total, fractional and interior-gap boundary.
func coverageWithinTolerance(profile candidateCoverage, tolerance coverageTolerance) bool {
	if profile.HashCadenceMS == nil || len(profile.DirectSelectedAssets) == 0 || profile.TotalSamples == 0 {
		return false
	}
	maxTotalSamples := tolerance.MaxTotalMS / currentHashCadenceMS
	maxInteriorSamples := tolerance.MaxInteriorMS / currentHashCadenceMS
	if profile.UncoveredSamples > maxTotalSamples || profile.UncoveredFraction > tolerance.MaxFraction {
		return false
	}
	for _, gap := range profile.InteriorGaps {
		if gap.End-gap.Start > maxInteriorSamples {
			return false
		}
	}
	return true
}

// exhaustiveDirectOverlap records every offset independently anchored by a
// production route. Route duplication is harmless because coverage intervals
// are merged before a policy decision.
func exhaustiveDirectOverlap(left, right asset) []routeOverlap {
	if left.id == right.id || left.eventID != right.eventID ||
		!dvideo.CompatibleFrameHashVersions(left.hashVersion, right.hashVersion) {
		return nil
	}
	var out []routeOverlap
	for _, route := range exhaustiveCoverageRoutes {
		for _, anchor := range qualifyingOffsetAlignments(
			left.frameHashes, right.frameHashes, route.maxHamming, route.minRun, route.maxGaps,
		) {
			out = append(out, segmentOffset(left.frameHashes, right.frameHashes, route.name, anchor, route.maxHamming))
		}
	}
	return out
}

// qualifyingOffsetAlignments returns the best bounded-gap window at every
// offset that independently passes one production matcher route.
func qualifyingOffsetAlignments(left, right []uint64, maxHamming, minRun, maxGaps int) []dvideo.AlignmentEvidence {
	if len(left) < minRun || len(right) < minRun {
		return nil
	}
	var out []dvideo.AlignmentEvidence
	for offset := -(len(left) - 1); offset <= len(right)-1; offset++ {
		alignment := bestAlignmentAtOffset(left, right, offset, maxHamming, maxGaps)
		if alignment.Frames >= minRun {
			out = append(out, alignment)
		}
	}
	return out
}

// bestAlignmentAtOffset finds the longest window with at most maxGaps misses
// while preserving deterministic tie-breaking.
func bestAlignmentAtOffset(left, right []uint64, offset, maxHamming, maxGaps int) dvideo.AlignmentEvidence {
	leftStart := max(0, -offset)
	leftEnd := min(len(left), len(right)-offset)
	best := dvideo.AlignmentEvidence{}
	lo, misses := 0, 0
	for hi := 0; hi < leftEnd-leftStart; hi++ {
		leftIndex := leftStart + hi
		if dvideo.Hamming(left[leftIndex], right[leftIndex+offset]) > maxHamming {
			misses++
		}
		for misses > maxGaps {
			old := leftStart + lo
			if dvideo.Hamming(left[old], right[old+offset]) > maxHamming {
				misses--
			}
			lo++
		}
		candidate := dvideo.AlignmentEvidence{
			LeftStart: leftStart + lo, RightStart: leftStart + lo + offset,
			Frames: hi - lo + 1, Gaps: misses,
		}
		if candidate.Frames > best.Frames || candidate.Frames == best.Frames &&
			(candidate.Gaps < best.Gaps || candidate.Gaps == best.Gaps && candidate.LeftStart < best.LeftStart) {
			best = candidate
		}
	}
	return best
}

// mergeFrameIntervals clips, orders and coalesces half-open spans on one
// candidate timeline.
func mergeFrameIntervals(intervals []frameInterval, total int) []frameInterval {
	valid := make([]frameInterval, 0, len(intervals))
	for _, interval := range intervals {
		interval.Start = max(0, min(total, interval.Start))
		interval.End = max(0, min(total, interval.End))
		if interval.Start < interval.End {
			valid = append(valid, interval)
		}
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].Start != valid[j].Start {
			return valid[i].Start < valid[j].Start
		}
		return valid[i].End < valid[j].End
	})
	merged := make([]frameInterval, 0, len(valid))
	for _, interval := range valid {
		if len(merged) == 0 || interval.Start > merged[len(merged)-1].End {
			merged = append(merged, interval)
			continue
		}
		merged[len(merged)-1].End = max(merged[len(merged)-1].End, interval.End)
	}
	return merged
}
