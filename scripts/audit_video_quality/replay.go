// replay.go — Arrival-order simulations for FF-081 keeper policies.
package main

import (
	"math/rand"
	"sort"
	"strings"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// simulateCurrentPolicy replays EventWorkflow's live-set reducer for one asset
// arrival order over a precomputed match graph.
func simulateCurrentPolicy(assets []asset, match [][]bool, order []int) string {
	var live []int
	for _, candidate := range order {
		var matchedPositions []int
		for position, incumbent := range live {
			if match[candidate][incumbent] {
				matchedPositions = append(matchedPositions, position)
			}
		}
		if len(matchedPositions) == 0 {
			live = append(live, candidate)
			continue
		}
		bestPosition := matchedPositions[0]
		for _, position := range matchedPositions[1:] {
			if dvideo.IsUpgrade(assets[live[position]].quality(), assets[live[bestPosition]].quality()) {
				bestPosition = position
			}
		}
		candidateWins := dvideo.IsUpgrade(assets[candidate].quality(), assets[live[bestPosition]].quality())
		drop := make(map[int]struct{}, len(matchedPositions))
		for _, position := range matchedPositions {
			if !candidateWins && position == bestPosition {
				continue
			}
			drop[position] = struct{}{}
		}
		kept := live[:0]
		for position, incumbent := range live {
			if _, found := drop[position]; !found {
				kept = append(kept, incumbent)
			}
		}
		live = kept
		if candidateWins {
			live = append(live, candidate)
		}
	}
	ids := make([]string, len(live))
	for i, index := range live {
		ids[i] = assets[index].id
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// simulateKeeperPolicy replays the same online direct-match topology while a
// deterministic set-level selector replaces the pairwise incumbent fold. It
// deliberately does not close the full dHash graph transitively.
func simulateKeeperPolicy(
	assets []asset,
	match [][]bool,
	order []int,
	selectKeeper func([]asset) asset,
) string {
	var live []int
	for _, candidate := range order {
		var matchedPositions []int
		members := []asset{assets[candidate]}
		memberIndexes := []int{candidate}
		for position, incumbent := range live {
			if !match[candidate][incumbent] {
				continue
			}
			matchedPositions = append(matchedPositions, position)
			members = append(members, assets[incumbent])
			memberIndexes = append(memberIndexes, incumbent)
		}
		if len(matchedPositions) == 0 {
			live = append(live, candidate)
			continue
		}

		winnerID := selectKeeper(members).id
		winnerIndex := candidate
		for i, item := range members {
			if item.id == winnerID {
				winnerIndex = memberIndexes[i]
				break
			}
		}
		drop := make(map[int]struct{}, len(matchedPositions))
		for _, position := range matchedPositions {
			drop[position] = struct{}{}
		}
		kept := live[:0]
		for position, incumbent := range live {
			if _, found := drop[position]; !found {
				kept = append(kept, incumbent)
			}
		}
		live = append(kept, winnerIndex)
	}
	ids := make([]string, len(live))
	for i, index := range live {
		ids[i] = assets[index].id
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// simulateSubstitutionPolicy replays the experimental pairwise policy without
// closing a perceptual graph transitively. A candidate hidden by any live
// substitute cannot itself retire another incumbent; otherwise it retires only
// incumbents for which it is a direct substitute and coexists with tradeoffs.
func simulateSubstitutionPolicy(
	assets []asset,
	match [][]bool,
	evidence [][]matcherEvidence,
	order []int,
) string {
	return simulateDirectionalPolicy(assets, match, evidence, order, evaluateSubstitution)
}

func simulateStableOffsetPolicy(
	assets []asset,
	match [][]bool,
	evidence [][]matcherEvidence,
	order []int,
) string {
	return simulateDirectionalPolicy(assets, match, evidence, order, evaluateStableOffsetSubstitution)
}

func simulateDirectionalPolicy(
	assets []asset,
	match [][]bool,
	evidence [][]matcherEvidence,
	order []int,
	evaluate func(asset, asset, matcherEvidence) substitutionDecision,
) string {
	var live []int
	for _, candidate := range order {
		blocked := false
		var replaced map[int]struct{}
		for position, incumbent := range live {
			if !match[candidate][incumbent] {
				continue
			}
			decision := evaluate(assets[candidate], assets[incumbent], evidence[candidate][incumbent])
			if decision.rightSubstitutesLeft {
				blocked = true
				break
			}
			if decision.leftSubstitutesRight {
				if replaced == nil {
					replaced = make(map[int]struct{})
				}
				replaced[position] = struct{}{}
			}
		}
		if blocked {
			continue
		}
		kept := live[:0]
		for position, incumbent := range live {
			if _, drop := replaced[position]; !drop {
				kept = append(kept, incumbent)
			}
		}
		live = append(kept, candidate)
	}
	ids := make([]string, len(live))
	for i, index := range live {
		ids[i] = assets[index].id
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// visitOrders visits every permutation when bounded by max; larger components
// receive chronological, reverse, and deterministic shuffled samples.
func visitOrders(size, max int, visit func([]int)) bool {
	if factorialAtMost(size, max) {
		order := make([]int, size)
		used := make([]bool, size)
		var walk func(int)
		walk = func(depth int) {
			if depth == size {
				visit(append([]int(nil), order...))
				return
			}
			for candidate := 0; candidate < size; candidate++ {
				if used[candidate] {
					continue
				}
				used[candidate] = true
				order[depth] = candidate
				walk(depth + 1)
				used[candidate] = false
			}
		}
		walk(0)
		return true
	}

	base := make([]int, size)
	for i := range base {
		base[i] = i
	}
	visit(base)
	reverse := append([]int(nil), base...)
	for left, right := 0, len(reverse)-1; left < right; left, right = left+1, right-1 {
		reverse[left], reverse[right] = reverse[right], reverse[left]
	}
	if max > 1 {
		visit(reverse)
	}
	rng := rand.New(rand.NewSource(int64(size)*7919 + 81)) //nolint:gosec // deterministic audit sampling
	for sampled := 2; sampled < max; sampled++ {
		order := append([]int(nil), base...)
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		visit(order)
	}
	return false
}

// factorialAtMost reports whether size! can be enumerated within limit.
func factorialAtMost(size, limit int) bool {
	value := 1
	for factor := 2; factor <= size; factor++ {
		if value > limit/factor {
			return false
		}
		value *= factor
	}
	return value <= limit
}
