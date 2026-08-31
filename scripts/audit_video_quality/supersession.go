// supersession.go — Historical supersession-graph diagnostics for FF-081.
package main

import "sort"

// findSupersessionCycles returns each persisted directed cycle exactly once.
func findSupersessionCycles(assets []asset, byID map[string]asset) [][]asset {
	state := make(map[string]int, len(assets))
	var cycles [][]asset
	for _, start := range assets {
		if state[start.id] != 0 {
			continue
		}
		var path []string
		position := make(map[string]int)
		current := start.id
		for current != "" && state[current] == 0 {
			if at, found := position[current]; found {
				cycle := make([]asset, 0, len(path)-at)
				for _, id := range path[at:] {
					cycle = append(cycle, byID[id])
				}
				sort.Slice(cycle, func(i, j int) bool { return cycle[i].id < cycle[j].id })
				cycles = append(cycles, cycle)
				break
			}
			position[current] = len(path)
			path = append(path, current)
			next, found := byID[current]
			if !found {
				break
			}
			current = next.supersededBy
		}
		for _, id := range path {
			state[id] = 2
		}
	}
	sort.Slice(cycles, func(i, j int) bool { return cycles[i][0].id < cycles[j][0].id })
	return cycles
}

// pairKey returns one order-independent asset-pair key.
func pairKey(left, right string) string {
	if left > right {
		left, right = right, left
	}
	return left + "|" + right
}
