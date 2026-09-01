// report.go — human-readable FF-081 corpus and policy-comparison output.
package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// printReport writes the bounded audit summary and highest-priority component
// evidence. It deliberately exposes policy candidates as comparisons, not as
// an accepted production recommendation.
func printReport(w io.Writer, result auditResult, detailLimit int) {
	fmt.Fprintf(w, "FF-081 retained quality corpus\n")
	fmt.Fprintf(w, "assets=%d events=%d pools=%d current_match_edges=%d\n",
		result.assetCount, result.eventCount, result.poolCount, result.matchEdgeCount)
	fmt.Fprintf(w, "matched_components=%d matched_assets=%d bridge_components=%d bridge_nodes=%d\n",
		result.matchedComponentCount, result.matchedAssetCount,
		result.bridgeComponentCount, result.bridgeNodeCount)
	fmt.Fprintf(w, "quality_cycle_components=%d quality_cycle_triples=%d order_sensitive_components=%d anchored_order_sensitive_components=%d\n",
		result.qualityCycleComponentCount, result.qualityCycleTripleCount,
		result.orderSensitiveComponentCount, result.anchoredOrderSensitiveCount)
	fmt.Fprintf(w, "experimental_coverage threshold=%.2f quality_floor=%.2f equivalent=%d left_contains_right=%d right_contains_left=%d partial_overlap=%d\n",
		wholeClipCoverage, qualityRetentionFloor,
		result.coverageClasses[string(coverageEquivalent)],
		result.coverageClasses[string(coverageLeftContainsRight)],
		result.coverageClasses[string(coverageRightContainsLeft)],
		result.coverageClasses[string(coveragePartial)])
	fmt.Fprintf(w, "experimental_actions collapse_equivalent=%d collapse_left=%d collapse_right=%d keep_both=%d order_sensitive_components=%d\n",
		result.experimentalActions["collapse_equivalent"], result.experimentalActions["collapse_left"],
		result.experimentalActions["collapse_right"], result.experimentalActions["keep_both"],
		result.experimentalOrderSensitive)
	fmt.Fprintf(w, "stable_offset_coverage coverage_threshold=%.2f similarity_threshold=%.2f quality_floor=%.2f equivalent=%d left_contains_right=%d right_contains_left=%d partial_overlap=%d\n",
		stableOffsetCoverage, stableOffsetSimilarity, qualityRetentionFloor,
		result.stableCoverageClasses[string(coverageEquivalent)],
		result.stableCoverageClasses[string(coverageLeftContainsRight)],
		result.stableCoverageClasses[string(coverageRightContainsLeft)],
		result.stableCoverageClasses[string(coveragePartial)])
	fmt.Fprintf(w, "stable_offset_actions collapse_equivalent=%d collapse_left=%d collapse_right=%d keep_both=%d order_sensitive_components=%d\n",
		result.stableActions["collapse_equivalent"], result.stableActions["collapse_left"],
		result.stableActions["collapse_right"], result.stableActions["keep_both"],
		result.stableOrderSensitive)
	fmt.Fprintf(w, "cadence_aware_actions collapse_equivalent=%d collapse_left=%d collapse_right=%d keep_both=%d\n",
		result.cadenceActions["collapse_equivalent"], result.cadenceActions["collapse_left"],
		result.cadenceActions["collapse_right"], result.cadenceActions["keep_both"])
	fmt.Fprintf(w, "direct_cover selected_assets=%d historical_terminal_assets=%d different_components=%d ambiguous_minima=%d inexact_components=%d\n",
		result.directCoverSelectedAssets, result.directCoverTerminalAssets,
		result.directCoverDifferentComponents, result.directCoverAmbiguousComponents,
		result.directCoverInexactComponents)
	fmt.Fprintf(w, "supersession_edges=%d edges_not_current_match=%d edges_across_pool=%d persisted_cycles=%d\n",
		result.historicalSupersessionEdges, result.historicalEdgesNotCurrent,
		result.historicalEdgesAcrossPools, len(result.supersessionCycles))
	fmt.Fprintf(w, "experimental_historical current_match_edges=%d prior_supersessions_rejected=%d\n",
		result.historicalPolicyEvaluated, result.historicalPolicyChanges)
	fmt.Fprintf(w, "stable_offset_historical current_match_edges=%d prior_supersessions_rejected=%d\n",
		result.historicalPolicyEvaluated, result.stableHistoricalChanges)
	fmt.Fprintf(w, "cadence_aware_historical current_match_edges=%d prior_supersessions_rejected=%d\n",
		result.historicalPolicyEvaluated, result.cadenceHistoricalChanges)
	fmt.Fprintf(w, "policy_differences strict_vs_band=%d bucket_vs_band=%d strict_not_terminal=%d band_not_terminal=%d\n",
		result.strictDiffersFromBandCount, result.bucketDiffersFromBandCount,
		result.strictDiffersFromTerminalCount, result.bandDiffersFromTerminalCount)
	fmt.Fprintln(w, "limitation=historical frame_rate is unknown; current keeper density is bitrate/(width*height), not per-frame quality")
	fmt.Fprintln(w, "limitation=clips discarded before asset creation are absent; the corpus covers every retained active/superseded asset")
	for _, policy := range result.policyComparisons {
		fmt.Fprintf(w,
			"total_order=%s density_weight=%.2f pixel_weight=%.2f acyclic_edge_reversals=%d band_winner_diffs=%d terminal_misses=%d\n",
			policy.name, policy.densityWeight, policy.pixelWeight, policy.acyclicEdgeReversals,
			policy.bandWinnerDiffs, policy.terminalMisses)
	}

	for _, cycle := range result.supersessionCycles {
		if len(cycle) == 0 {
			continue
		}
		fmt.Fprintf(w, "\nPERSISTED_CYCLE fixture=%d event=%s player=%q minute=%d assets=",
			cycle[0].fixtureID, cycle[0].eventID, cycle[0].playerName, cycle[0].minute)
		for i, item := range cycle {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprint(w, item.id)
		}
		fmt.Fprintln(w)
	}

	if detailLimit < 0 || detailLimit > len(result.findings) {
		detailLimit = len(result.findings)
	}
	for index, finding := range result.findings[:detailLimit] {
		printFinding(w, index+1, finding)
	}
}

// printFinding writes one reconstructed match component and its member
// metadata in chronological order.
func printFinding(w io.Writer, number int, finding componentFinding) {
	fmt.Fprintf(w, "\nCOMPONENT %d %s\n", number, componentName(finding))
	fmt.Fprintf(w, "size=%d current_edges=%d historical_edges=%d bridge_nodes=%d quality_cycles=%d permutations=%d exhaustive=%t outcomes=%d\n",
		len(finding.assets), finding.edges, finding.historicalEdges, finding.bridgeNodes, finding.qualityCycles,
		finding.permutations, finding.exhaustive, len(finding.outcomes))
	fmt.Fprintf(w, "terminal=%s chronological=%s anchored_chronological=%s experimental_chronological=%s stable_chronological=%s strict=%s bucket=%s anchored_band=%s best_grid=%s\n",
		shortIDs(finding.terminalIDs), outcomeLabel(finding.chronologicalResult), outcomeLabel(finding.anchoredChronologicalResult),
		outcomeLabel(finding.experimentalChronological), outcomeLabel(finding.stableChronological), shortID(finding.strictWinnerID),
		shortID(finding.bucketWinnerID), shortID(finding.bandWinnerID), shortID(finding.bestGridWinnerID))
	fmt.Fprintf(w, "direct_cover=%s alternatives=%d exact=%t\n",
		shortIDs(finding.directCoverIDs), finding.directCoverAlternatives, finding.directCoverExact)

	type outcomeCount struct {
		outcome string
		count   int
	}
	outcomes := make([]outcomeCount, 0, len(finding.outcomes))
	for outcome, count := range finding.outcomes {
		outcomes = append(outcomes, outcomeCount{outcome: outcome, count: count})
	}
	sort.Slice(outcomes, func(i, j int) bool {
		if outcomes[i].count != outcomes[j].count {
			return outcomes[i].count > outcomes[j].count
		}
		return outcomes[i].outcome < outcomes[j].outcome
	})
	for _, outcome := range outcomes {
		fmt.Fprintf(w, "outcome count=%d live=%s\n", outcome.count, outcomeLabel(outcome.outcome))
	}
	anchoredOutcomes := make([]outcomeCount, 0, len(finding.anchoredOutcomes))
	for outcome, count := range finding.anchoredOutcomes {
		anchoredOutcomes = append(anchoredOutcomes, outcomeCount{outcome: outcome, count: count})
	}
	sort.Slice(anchoredOutcomes, func(i, j int) bool {
		if anchoredOutcomes[i].count != anchoredOutcomes[j].count {
			return anchoredOutcomes[i].count > anchoredOutcomes[j].count
		}
		return anchoredOutcomes[i].outcome < anchoredOutcomes[j].outcome
	})
	for _, outcome := range anchoredOutcomes {
		fmt.Fprintf(w, "anchored_outcome count=%d live=%s\n", outcome.count, outcomeLabel(outcome.outcome))
	}
	experimentalOutcomes := make([]outcomeCount, 0, len(finding.experimentalOutcomes))
	for outcome, count := range finding.experimentalOutcomes {
		experimentalOutcomes = append(experimentalOutcomes, outcomeCount{outcome: outcome, count: count})
	}
	sort.Slice(experimentalOutcomes, func(i, j int) bool {
		if experimentalOutcomes[i].count != experimentalOutcomes[j].count {
			return experimentalOutcomes[i].count > experimentalOutcomes[j].count
		}
		return experimentalOutcomes[i].outcome < experimentalOutcomes[j].outcome
	})
	for _, outcome := range experimentalOutcomes {
		fmt.Fprintf(w, "experimental_outcome count=%d live=%s\n", outcome.count, outcomeLabel(outcome.outcome))
	}
	stableOutcomes := make([]outcomeCount, 0, len(finding.stableOutcomes))
	for outcome, count := range finding.stableOutcomes {
		stableOutcomes = append(stableOutcomes, outcomeCount{outcome: outcome, count: count})
	}
	sort.Slice(stableOutcomes, func(i, j int) bool {
		if stableOutcomes[i].count != stableOutcomes[j].count {
			return stableOutcomes[i].count > stableOutcomes[j].count
		}
		return stableOutcomes[i].outcome < stableOutcomes[j].outcome
	})
	for _, outcome := range stableOutcomes {
		fmt.Fprintf(w, "stable_outcome count=%d live=%s\n", outcome.count, outcomeLabel(outcome.outcome))
	}
	assets := make(map[string]asset, len(finding.assets))
	for _, item := range finding.assets {
		assets[item.id] = item
	}
	for _, edge := range finding.matchEdges {
		route, alignment := edge.evidence.strongest()
		decision := evaluateSubstitution(assets[edge.leftID], assets[edge.rightID], edge.evidence)
		stable := measureStableOffset(assets[edge.leftID], assets[edge.rightID], edge.evidence)
		stableDecision := evaluateStableOffsetSubstitution(
			assets[edge.leftID], assets[edge.rightID], edge.evidence,
		)
		cadenceDecision := evaluateCadenceAwareSubstitution(
			assets[edge.leftID], assets[edge.rightID], edge.evidence,
		)
		fmt.Fprintf(w, "edge=%s-%s primary_window=%d long_window=%d route=%s aligned=%d starts=%d/%d gaps=%d coverage=%.3f/%.3f class=%s action=%s stable_route=%s stable_starts=%d/%d stable_overlap=%d stable_similar=%d stable_similarity=%.3f stable_coverage=%.3f/%.3f stable_class=%s stable_action=%s\n",
			shortID(edge.leftID), shortID(edge.rightID), edge.evidence.primary.Frames,
			edge.evidence.long.Frames, route, alignment.Frames, alignment.LeftStart,
			alignment.RightStart, alignment.Gaps, decision.leftCoverage,
			decision.rightCoverage, decision.coverageClass, decision.action(), stable.route,
			stable.leftStart, stable.rightStart, stable.overlapFrames, stable.similarFrames,
			stable.similarity, stableDecision.leftCoverage, stableDecision.rightCoverage,
			stableDecision.coverageClass, stableDecision.action())
		fmt.Fprintf(w, "edge_cadence=%s-%s action=%s\n",
			shortID(edge.leftID), shortID(edge.rightID), cadenceDecision.action())
	}
	for _, item := range finding.assets {
		target := "terminal"
		if item.supersededBy != "" {
			target = "->" + shortID(item.supersededBy)
		}
		fmt.Fprintf(w,
			"asset=%s asset_id=%s %s duration=%.3fs density=%.4f resolution=%dx%d size=%.2fMiB popularity=%d exact_observations=%d share=%s share_id=%s first=%s\n",
			shortID(item.id), item.id, target, float64(item.durationMS)/1000, item.spatialBitrateDensity(),
			item.width, item.height, float64(item.fileSizeBytes)/(1024*1024),
			item.popularity, item.observedPopularity, item.shareState, item.shareID, item.firstSeenAt)
	}
}

// shortIDs renders a stable comma-separated list of abbreviated identifiers.
func shortIDs(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	shortened := make([]string, len(ids))
	for i, id := range ids {
		shortened[i] = shortID(id)
	}
	return strings.Join(shortened, ",")
}
