// analysis.go — FF-081 match-graph reconstruction, order replay, and policy comparison.
package main

import (
	"fmt"
	"sort"
	"strings"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

const (
	primaryMaxHamming = 12
	primaryMinRun     = 30
	primaryMaxGaps    = 3
	longMaxHamming    = 16
	longMinRun        = 50
	longMaxGaps       = 5
	durationCapMS     = 60_000
)

// auditResult is the complete read-only analysis result.
type auditResult struct {
	assetCount                     int
	eventCount                     int
	poolCount                      int
	matchEdgeCount                 int
	matchedComponentCount          int
	matchedAssetCount              int
	bridgeComponentCount           int
	bridgeNodeCount                int
	qualityCycleComponentCount     int
	qualityCycleTripleCount        int
	orderSensitiveComponentCount   int
	anchoredOrderSensitiveCount    int
	historicalSupersessionEdges    int
	historicalEdgesNotCurrent      int
	historicalEdgesAcrossPools     int
	supersessionCycles             [][]asset
	findings                       []componentFinding
	strictDiffersFromBandCount     int
	bucketDiffersFromBandCount     int
	bandDiffersFromTerminalCount   int
	strictDiffersFromTerminalCount int
	policyComparisons              []policyComparison
}

// componentFinding captures one connected current-match component and every
// signal needed to inspect its keeper behavior.
type componentFinding struct {
	assets                      []asset
	edges                       int
	matchEdges                  []matchEdge
	historicalEdges             int
	bridgeNodes                 int
	qualityCycles               int
	permutations                int
	exhaustive                  bool
	outcomes                    map[string]int
	anchoredOutcomes            map[string]int
	terminalIDs                 []string
	strictWinnerID              string
	bucketWinnerID              string
	bandWinnerID                string
	bestGridWinnerID            string
	chronologicalResult         string
	anchoredChronologicalResult string
}

// matchEdge records the strongest current matcher evidence between two
// retained assets. The windows are measured independently for the primary and
// sustained Hamming policies so a visual review can locate bridge evidence.
type matchEdge struct {
	leftID, rightID           string
	primaryWindow, longWindow int
}

// poolKey is the production perceptual-dedup scope.
type poolKey struct {
	eventID     string
	verified    bool
	hashVersion dvideo.FrameHashVersion
}

// poolGraph holds one scope's chronological assets and pairwise current-policy
// match matrix.
type poolGraph struct {
	assets  []asset
	match   [][]bool
	connect [][]bool
}

// analyze reconstructs the current match graph, persisted supersession graph,
// and arrival-order outcomes for every retained event pool.
func analyze(assets []asset, maxPermutations int) auditResult {
	result := auditResult{assetCount: len(assets)}
	if maxPermutations < 1 {
		maxPermutations = 1
	}

	events := make(map[string]struct{})
	pools := make(map[poolKey][]asset)
	byID := make(map[string]asset, len(assets))
	for _, item := range assets {
		events[item.eventID] = struct{}{}
		key := poolKey{eventID: item.eventID, verified: item.verified, hashVersion: item.hashVersion}
		pools[key] = append(pools[key], item)
		byID[item.id] = item
	}
	result.eventCount = len(events)
	result.poolCount = len(pools)

	pairMatches := make(map[string]bool)
	for _, members := range pools {
		sort.Slice(members, func(i, j int) bool {
			if members[i].firstSeenAt != members[j].firstSeenAt {
				return members[i].firstSeenAt < members[j].firstSeenAt
			}
			return members[i].id < members[j].id
		})
		graph := buildPoolGraph(members)
		for i := range graph.assets {
			for j := i + 1; j < len(graph.assets); j++ {
				if graph.match[i][j] {
					result.matchEdgeCount++
					pairMatches[pairKey(graph.assets[i].id, graph.assets[j].id)] = true
				}
			}
		}

		for _, indexes := range connectedComponents(graph.connect) {
			if len(indexes) < 2 {
				continue
			}
			finding := analyzeComponent(graph, indexes, maxPermutations)
			result.findings = append(result.findings, finding)
			result.matchedComponentCount++
			result.matchedAssetCount += len(finding.assets)
			result.bridgeNodeCount += finding.bridgeNodes
			if finding.bridgeNodes > 0 {
				result.bridgeComponentCount++
			}
			result.qualityCycleTripleCount += finding.qualityCycles
			if finding.qualityCycles > 0 {
				result.qualityCycleComponentCount++
			}
			if len(finding.outcomes) > 1 {
				result.orderSensitiveComponentCount++
			}
			if len(finding.anchoredOutcomes) > 1 {
				result.anchoredOrderSensitiveCount++
			}
			if finding.strictWinnerID != finding.bandWinnerID {
				result.strictDiffersFromBandCount++
			}
			if finding.bucketWinnerID != finding.bandWinnerID {
				result.bucketDiffersFromBandCount++
			}
			if !containsString(finding.terminalIDs, finding.bandWinnerID) {
				result.bandDiffersFromTerminalCount++
			}
			if !containsString(finding.terminalIDs, finding.strictWinnerID) {
				result.strictDiffersFromTerminalCount++
			}
		}
	}

	for _, item := range assets {
		if item.supersededBy == "" {
			continue
		}
		result.historicalSupersessionEdges++
		target, found := byID[item.supersededBy]
		if !found || target.eventID != item.eventID || target.verified != item.verified ||
			!dvideo.CompatibleFrameHashVersions(target.hashVersion, item.hashVersion) {
			result.historicalEdgesAcrossPools++
			continue
		}
		if !pairMatches[pairKey(item.id, target.id)] {
			result.historicalEdgesNotCurrent++
		}
	}
	result.supersessionCycles = findSupersessionCycles(assets, byID)
	result.policyComparisons = compareWeightedPolicies(assets, result.findings, byID, result.supersessionCycles)
	bestGrid := result.policyComparisons[len(result.policyComparisons)-1]
	for i := range result.findings {
		result.findings[i].bestGridWinnerID = weightedWinner(
			result.findings[i].assets, bestGrid.densityWeight, bestGrid.pixelWeight,
		).id
	}

	sort.Slice(result.findings, func(i, j int) bool {
		left, right := result.findings[i], result.findings[j]
		leftScore := findingPriority(left)
		rightScore := findingPriority(right)
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		if len(left.assets) != len(right.assets) {
			return len(left.assets) > len(right.assets)
		}
		return left.assets[0].eventID < right.assets[0].eventID
	})
	return result
}

// buildPoolGraph evaluates the exact production matcher for every pair in one
// event/category/hash-version pool.
func buildPoolGraph(assets []asset) poolGraph {
	match := make([][]bool, len(assets))
	connect := make([][]bool, len(assets))
	for i := range match {
		match[i] = make([]bool, len(assets))
		connect[i] = make([]bool, len(assets))
		match[i][i] = true
		connect[i][i] = true
	}
	for i := range assets {
		for j := i + 1; j < len(assets); j++ {
			matched := dvideo.Match(
				assets[i].frameHashes, assets[j].frameHashes,
				primaryMaxHamming, primaryMinRun, primaryMaxGaps,
			) || dvideo.Match(
				assets[i].frameHashes, assets[j].frameHashes,
				longMaxHamming, longMinRun, longMaxGaps,
			)
			match[i][j], match[j][i] = matched, matched
			connect[i][j], connect[j][i] = matched, matched
		}
	}
	indexes := make(map[string]int, len(assets))
	for i, item := range assets {
		indexes[item.id] = i
	}
	for i, item := range assets {
		if target, found := indexes[item.supersededBy]; found {
			connect[i][target], connect[target][i] = true, true
		}
	}
	return poolGraph{assets: assets, match: match, connect: connect}
}

// connectedComponents returns every component in an undirected match matrix.
func connectedComponents(match [][]bool) [][]int {
	seen := make([]bool, len(match))
	var components [][]int
	for start := range match {
		if seen[start] {
			continue
		}
		seen[start] = true
		queue := []int{start}
		var component []int
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			component = append(component, current)
			for next := range match[current] {
				if next == current || seen[next] || !match[current][next] {
					continue
				}
				seen[next] = true
				queue = append(queue, next)
			}
		}
		components = append(components, component)
	}
	return components
}

// analyzeComponent measures bridge topology, quality cycles, all bounded
// arrival-order outcomes, and three candidate deterministic keeper policies.
func analyzeComponent(graph poolGraph, indexes []int, maxPermutations int) componentFinding {
	assets := make([]asset, len(indexes))
	match := make([][]bool, len(indexes))
	for i, source := range indexes {
		assets[i] = graph.assets[source]
		match[i] = make([]bool, len(indexes))
		for j, target := range indexes {
			match[i][j] = graph.match[source][target]
		}
	}
	finding := componentFinding{
		assets: assets, outcomes: make(map[string]int), anchoredOutcomes: make(map[string]int),
	}
	memberIDs := make(map[string]struct{}, len(assets))
	for _, item := range assets {
		memberIDs[item.id] = struct{}{}
	}
	for i := range assets {
		for j := i + 1; j < len(assets); j++ {
			if match[i][j] {
				finding.edges++
				finding.matchEdges = append(finding.matchEdges, matchEdge{
					leftID: assets[i].id, rightID: assets[j].id,
					primaryWindow: longestAuditWindow(
						assets[i].frameHashes, assets[j].frameHashes,
						primaryMaxHamming, primaryMaxGaps,
					),
					longWindow: longestAuditWindow(
						assets[i].frameHashes, assets[j].frameHashes,
						longMaxHamming, longMaxGaps,
					),
				})
			}
		}
		if isBridgeNode(match, i) {
			finding.bridgeNodes++
		}
		if _, found := memberIDs[assets[i].supersededBy]; found {
			finding.historicalEdges++
		}
	}
	finding.qualityCycles = countQualityCycles(assets)

	visit := func(order []int) {
		outcome := simulateCurrentPolicy(assets, match, order)
		finding.outcomes[outcome]++
		anchoredOutcome := simulateKeeperPolicy(assets, match, order, anchoredBandWinner)
		finding.anchoredOutcomes[anchoredOutcome]++
		finding.permutations++
	}
	finding.exhaustive = visitOrders(len(assets), maxPermutations, visit)
	chronological := make([]int, len(assets))
	for i := range chronological {
		chronological[i] = i
	}
	finding.chronologicalResult = simulateCurrentPolicy(assets, match, chronological)
	finding.anchoredChronologicalResult = simulateKeeperPolicy(
		assets, match, chronological, anchoredBandWinner,
	)
	for _, item := range assets {
		if item.supersededBy == "" {
			finding.terminalIDs = append(finding.terminalIDs, item.id)
		}
	}
	sort.Strings(finding.terminalIDs)
	finding.strictWinnerID = strictWinner(assets).id
	finding.bucketWinnerID = bucketWinner(assets).id
	finding.bandWinnerID = anchoredBandWinner(assets).id
	return finding
}

// longestAuditWindow reproduces the domain matcher's unexported diagnostic:
// the longest aligned window with at most maxGaps over-threshold frames.
func longestAuditWindow(a, b []uint64, maxHamming, maxGaps int) int {
	best := 0
	for offset := -(len(a) - 1); offset <= len(b)-1; offset++ {
		var aligned []bool
		for i := range a {
			j := i + offset
			if j < 0 || j >= len(b) {
				continue
			}
			aligned = append(aligned, dvideo.Hamming(a[i], b[j]) <= maxHamming)
		}
		left, misses := 0, 0
		for right, matched := range aligned {
			if !matched {
				misses++
			}
			for misses > maxGaps {
				if !aligned[left] {
					misses--
				}
				left++
			}
			if width := right - left + 1; width > best {
				best = width
			}
		}
	}
	return best
}

// isBridgeNode reports whether node connects at least two neighbors that do
// not directly match each other.
func isBridgeNode(match [][]bool, node int) bool {
	var neighbors []int
	for other := range match[node] {
		if other != node && match[node][other] {
			neighbors = append(neighbors, other)
		}
	}
	for i := range neighbors {
		for j := i + 1; j < len(neighbors); j++ {
			if !match[neighbors[i]][neighbors[j]] {
				return true
			}
		}
	}
	return false
}

// countQualityCycles counts three-member rock-paper-scissors relations under
// the current thresholded pairwise comparator.
func countQualityCycles(assets []asset) int {
	cycles := 0
	for i := 0; i < len(assets); i++ {
		for j := i + 1; j < len(assets); j++ {
			for k := j + 1; k < len(assets); k++ {
				iBeatsJ := dvideo.IsUpgrade(assets[i].quality(), assets[j].quality())
				jBeatsK := dvideo.IsUpgrade(assets[j].quality(), assets[k].quality())
				kBeatsI := dvideo.IsUpgrade(assets[k].quality(), assets[i].quality())
				jBeatsI := dvideo.IsUpgrade(assets[j].quality(), assets[i].quality())
				kBeatsJ := dvideo.IsUpgrade(assets[k].quality(), assets[j].quality())
				iBeatsK := dvideo.IsUpgrade(assets[i].quality(), assets[k].quality())
				if (iBeatsJ && jBeatsK && kBeatsI) || (jBeatsI && kBeatsJ && iBeatsK) {
					cycles++
				}
			}
		}
	}
	return cycles
}

// containsString reports set membership in a small string slice.
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// findingPriority puts correctness evidence before routine policy differences.
func findingPriority(finding componentFinding) int {
	priority := 0
	if finding.qualityCycles > 0 {
		priority += 1_000_000
	}
	if len(finding.outcomes) > 1 {
		priority += 100_000
	}
	if finding.bridgeNodes > 0 {
		priority += 10_000
	}
	if !containsString(finding.terminalIDs, finding.bandWinnerID) {
		priority += 1_000
	}
	if finding.bestGridWinnerID != "" && !containsString(finding.terminalIDs, finding.bestGridWinnerID) {
		priority += 5_000
	}
	priority += len(finding.assets)*10 + finding.edges
	return priority
}

// outcomeLabel shortens a comma-separated final live set for compact reports.
func outcomeLabel(outcome string) string {
	parts := strings.Split(outcome, ",")
	for i, part := range parts {
		parts[i] = shortID(part)
	}
	return strings.Join(parts, ",")
}

// shortID renders enough UUID prefix for one report to remain readable.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// componentName returns the human match label for one finding.
func componentName(finding componentFinding) string {
	item := finding.assets[0]
	extra := ""
	if item.extra != "" {
		extra = "+" + item.extra
	}
	return fmt.Sprintf("fixture=%d %s-%s player=%q minute=%d%s event=%s verified=%t",
		item.fixtureID, item.homeTeam, item.awayTeam, item.playerName,
		item.minute, extra, item.eventID, item.verified)
}
