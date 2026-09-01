// direct_cover.go — Order-independent public-set experiment over direct
// pairwise substitution evidence; no perceptual transitive closure.
package main

import (
	"math/bits"
	"sort"
	"strings"
)

const maxExactCoverAssets = 20

type directCoverResult struct {
	selected     []int
	alternatives int
	exact        bool
}

func buildSubstitutionMatrix(
	assets []asset,
	match [][]bool,
	evidence [][]matcherEvidence,
	evaluate func(asset, asset, matcherEvidence) substitutionDecision,
) [][]bool {
	substitutes := make([][]bool, len(assets))
	for left := range assets {
		substitutes[left] = make([]bool, len(assets))
		substitutes[left][left] = true
	}
	for left := range assets {
		for right := left + 1; right < len(assets); right++ {
			if !match[left][right] {
				continue
			}
			decision := evaluate(assets[left], assets[right], evidence[left][right])
			substitutes[left][right] = decision.leftSubstitutesRight
			substitutes[right][left] = decision.rightSubstitutesLeft
		}
	}
	return substitutes
}

// minimumDirectCover selects the smallest set for which every hidden asset has
// a selected direct substitute. It never follows substitution transitively.
// Equal-size covers prefer more exact-variant observations, then stable asset
// IDs. Components above the explicit exact bound stay fully visible.
func minimumDirectCover(assets []asset, substitutes [][]bool) directCoverResult {
	if len(assets) == 0 {
		return directCoverResult{exact: true}
	}
	if len(assets) > maxExactCoverAssets {
		selected := make([]int, len(assets))
		for i := range selected {
			selected[i] = i
		}
		return directCoverResult{selected: selected}
	}

	var bestMask uint64
	bestSize := len(assets) + 1
	alternatives := 0
	limit := uint64(1) << len(assets)
	for mask := uint64(1); mask < limit; mask++ {
		size := bits.OnesCount64(mask)
		if size > bestSize || !isDirectCover(mask, substitutes) {
			continue
		}
		switch {
		case size < bestSize:
			bestMask, bestSize, alternatives = mask, size, 1
		case size == bestSize:
			alternatives++
			if betterCover(mask, bestMask, assets) {
				bestMask = mask
			}
		}
	}
	return directCoverResult{
		selected: indexesFromMask(bestMask, len(assets)), alternatives: alternatives, exact: true,
	}
}

func isDirectCover(mask uint64, substitutes [][]bool) bool {
	for hidden := range substitutes {
		covered := false
		for selected := range substitutes {
			if mask&(uint64(1)<<selected) != 0 && substitutes[selected][hidden] {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func betterCover(candidate, incumbent uint64, assets []asset) bool {
	candidateVotes, incumbentVotes := 0, 0
	for index, item := range assets {
		if candidate&(uint64(1)<<index) != 0 {
			candidateVotes += item.observedPopularity
		}
		if incumbent&(uint64(1)<<index) != 0 {
			incumbentVotes += item.observedPopularity
		}
	}
	if candidateVotes != incumbentVotes {
		return candidateVotes > incumbentVotes
	}
	return coverKey(candidate, assets) < coverKey(incumbent, assets)
}

func coverKey(mask uint64, assets []asset) string {
	ids := make([]string, 0, bits.OnesCount64(mask))
	for index, item := range assets {
		if mask&(uint64(1)<<index) != 0 {
			ids = append(ids, item.id)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func indexesFromMask(mask uint64, size int) []int {
	var indexes []int
	for index := 0; index < size; index++ {
		if mask&(uint64(1)<<index) != 0 {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func coverIDs(assets []asset, selected []int) []string {
	ids := make([]string, len(selected))
	for i, index := range selected {
		ids[i] = assets[index].id
	}
	sort.Strings(ids)
	return ids
}
