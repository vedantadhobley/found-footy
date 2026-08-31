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
	fmt.Fprintf(w, "supersession_edges=%d edges_not_current_match=%d edges_across_pool=%d persisted_cycles=%d\n",
		result.historicalSupersessionEdges, result.historicalEdgesNotCurrent,
		result.historicalEdgesAcrossPools, len(result.supersessionCycles))
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
	fmt.Fprintf(w, "terminal=%s chronological=%s anchored_chronological=%s strict=%s bucket=%s anchored_band=%s best_grid=%s\n",
		shortIDs(finding.terminalIDs), outcomeLabel(finding.chronologicalResult), outcomeLabel(finding.anchoredChronologicalResult),
		shortID(finding.strictWinnerID), shortID(finding.bucketWinnerID), shortID(finding.bandWinnerID), shortID(finding.bestGridWinnerID))

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
	if len(finding.outcomes) > 1 || finding.qualityCycles > 0 {
		for _, edge := range finding.matchEdges {
			fmt.Fprintf(w, "edge=%s-%s primary_window=%d long_window=%d\n",
				shortID(edge.leftID), shortID(edge.rightID), edge.primaryWindow, edge.longWindow)
		}
	}
	for _, item := range finding.assets {
		target := "terminal"
		if item.supersededBy != "" {
			target = "->" + shortID(item.supersededBy)
		}
		fmt.Fprintf(w,
			"asset=%s asset_id=%s %s duration=%.3fs density=%.4f resolution=%dx%d size=%.2fMiB popularity=%d share=%s share_id=%s first=%s\n",
			shortID(item.id), item.id, target, float64(item.durationMS)/1000, item.spatialBitrateDensity(),
			item.width, item.height, float64(item.fileSizeBytes)/(1024*1024),
			item.popularity, item.shareState, item.shareID, item.firstSeenAt)
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
