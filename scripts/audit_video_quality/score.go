// score.go — FF-081 diagnostic total-order fitting against retained decisions.
package main

import (
	"math"
	"sort"
)

// policyComparison measures one explanatory log-linear total order. These are
// audit candidates, not accepted production scores.
type policyComparison struct {
	name                 string
	densityWeight        float64
	pixelWeight          float64
	acyclicEdgeReversals int
	bandWinnerDiffs      int
	terminalMisses       int
}

// compareWeightedPolicies evaluates three interpretable scores and the best
// bounded grid point against retained acyclic supersession decisions.
func compareWeightedPolicies(
	assets []asset,
	findings []componentFinding,
	byID map[string]asset,
	cycles [][]asset,
) []policyComparison {
	cycleEdges := make(map[string]struct{})
	for _, cycle := range cycles {
		members := make(map[string]struct{}, len(cycle))
		for _, item := range cycle {
			members[item.id] = struct{}{}
		}
		for _, item := range cycle {
			if _, found := members[item.supersededBy]; found {
				cycleEdges[pairKey(item.id, item.supersededBy)] = struct{}{}
			}
		}
	}

	evaluate := func(name string, densityWeight, pixelWeight float64) policyComparison {
		out := policyComparison{
			name: name, densityWeight: densityWeight, pixelWeight: pixelWeight,
		}
		for _, loser := range assets {
			if loser.supersededBy == "" {
				continue
			}
			winner, found := byID[loser.supersededBy]
			if !found {
				continue
			}
			if _, cyclic := cycleEdges[pairKey(loser.id, winner.id)]; cyclic {
				continue
			}
			if weightedBetter(loser, winner, densityWeight, pixelWeight) {
				out.acyclicEdgeReversals++
			}
		}
		for _, finding := range findings {
			winner := weightedWinner(finding.assets, densityWeight, pixelWeight)
			if winner.id != finding.bandWinnerID {
				out.bandWinnerDiffs++
			}
			if !containsString(finding.terminalIDs, winner.id) {
				out.terminalMisses++
			}
		}
		return out
	}

	comparisons := []policyComparison{
		evaluate("duration_x_density", 1, 0),
		evaluate("balanced_information", 1, 0.5),
		evaluate("capped_total_bits", 1, 1),
	}
	best := policyComparison{acyclicEdgeReversals: math.MaxInt, bandWinnerDiffs: math.MaxInt, terminalMisses: math.MaxInt}
	for densityStep := 0; densityStep <= 40; densityStep++ {
		densityWeight := float64(densityStep) * 0.05
		for pixelStep := 0; pixelStep <= 30; pixelStep++ {
			pixelWeight := float64(pixelStep) * 0.05
			candidate := evaluate("best_grid", densityWeight, pixelWeight)
			if betterFit(candidate, best) {
				best = candidate
			}
		}
	}
	comparisons = append(comparisons, best)
	return comparisons
}

// betterFit orders diagnostic fits by historical edge agreement, then the
// anchored-band and terminal corpus checks, then simpler/smaller weights.
func betterFit(left, right policyComparison) bool {
	if left.acyclicEdgeReversals != right.acyclicEdgeReversals {
		return left.acyclicEdgeReversals < right.acyclicEdgeReversals
	}
	if left.bandWinnerDiffs != right.bandWinnerDiffs {
		return left.bandWinnerDiffs < right.bandWinnerDiffs
	}
	if left.terminalMisses != right.terminalMisses {
		return left.terminalMisses < right.terminalMisses
	}
	if left.densityWeight != right.densityWeight {
		return left.densityWeight < right.densityWeight
	}
	return left.pixelWeight < right.pixelWeight
}

// weightedWinner returns the stable maximum of one log-linear score.
func weightedWinner(assets []asset, densityWeight, pixelWeight float64) asset {
	ordered := append([]asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool {
		return weightedBetter(ordered[i], ordered[j], densityWeight, pixelWeight)
	})
	return ordered[0]
}

// weightedBetter compares capped duration, encoding density, and resolution
// in log space. Asset identity makes the relation total when metadata ties.
func weightedBetter(left, right asset, densityWeight, pixelWeight float64) bool {
	leftScore := weightedScore(left, densityWeight, pixelWeight)
	rightScore := weightedScore(right, densityWeight, pixelWeight)
	if leftScore != rightScore {
		return leftScore > rightScore
	}
	return left.id < right.id
}

// weightedScore is log(duration) + b·log(density) + p·log(pixel count).
func weightedScore(item asset, densityWeight, pixelWeight float64) float64 {
	score := math.Log(float64(cappedDuration(item)))
	if densityWeight != 0 {
		density := item.spatialBitrateDensity()
		if density <= 0 {
			return math.Inf(-1)
		}
		score += densityWeight * math.Log(density)
	}
	if pixelWeight != 0 {
		pixels := item.width * item.height
		if pixels <= 0 {
			return math.Inf(-1)
		}
		score += pixelWeight * math.Log(float64(pixels))
	}
	return score
}
