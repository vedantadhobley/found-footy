// Offline FF-092 placement replay with a separate, match-only restoration experiment.
package main

import (
	"fmt"
	"slices"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// restorationStep describes selected roots before popularity visibility, not a
// database mutation. Missing direct support is not proof of different footage.
type restorationStep struct {
	Candidate string   `json:"candidate"`
	Selected  []string `json:"selected"`
	Restored  []string `json:"restored"`
	Uncovered []string `json:"without_selected_direct_match"`
}

// restorationReplay keeps prefix failures separate from final-set differences.
type restorationReplay struct {
	Selected         []string          `json:"selected"`
	RestorationCount int               `json:"restoration_operations"`
	UnsupportedSteps int               `json:"unsupported_prefixes"`
	FinalUncovered   []string          `json:"final_without_selected_direct_match"`
	Steps            []restorationStep `json:"steps,omitempty"`
}

// replayDirectRestoration preserves the production FF-092 comparator and
// losing-candidate rule. Its optional second phase only adds unsupported nodes;
// it never uses a hidden intermediary, future arrival, or popularity tiebreak.
// A direct dHash match is only a necessary support check, not an approved
// directional content/quality substitution rule.
func replayDirectRestoration(assets []asset, match [][]bool, order []int, restore, trace bool) (restorationReplay, error) {
	var result restorationReplay
	if err := validateRestorationGraph(assets, match, order); err != nil {
		return result, err
	}
	var seen, selected []int
	for _, candidate := range order {
		// A repeated exact variant is already owned. The real workflow credits
		// its current root; it does not rerun quality selection for those bytes.
		if slices.Contains(seen, candidate) {
			continue
		}
		seen = append(seen, candidate)
		selected = placeFF092(assets, match, selected, candidate)
		var restored []int
		if restore {
			selected, restored = restoreUnsupported(seen, selected, match)
		}
		uncovered := unsupportedNodes(seen, selected, match)
		if restore && len(uncovered) != 0 {
			return result, fmt.Errorf("restoration left unsupported nodes after %s", assets[candidate].id)
		}
		if len(uncovered) != 0 {
			result.UnsupportedSteps++
		}
		result.RestorationCount += len(restored)
		result.FinalUncovered = coverIDs(assets, uncovered)
		if trace {
			result.Steps = append(result.Steps, restorationStep{
				Candidate: assets[candidate].id, Selected: coverIDs(assets, selected),
				Restored: coverIDs(assets, restored), Uncovered: coverIDs(assets, uncovered),
			})
		}
	}
	result.Selected = coverIDs(assets, selected)
	return result, nil
}

// placeFF092 mirrors the deployed reducer explicitly. The older audit's
// simulatePreFF092Policy preserves pre-FF-092 behavior for historical reports.
func placeFF092(assets []asset, match [][]bool, selected []int, candidate int) []int {
	best := -1
	for _, incumbent := range selected {
		if match[candidate][incumbent] && (best < 0 || dvideo.IsUpgrade(assets[incumbent].quality(), assets[best].quality())) {
			best = incumbent
		}
	}
	if best < 0 {
		return append(selected, candidate)
	}
	if !dvideo.IsUpgrade(assets[candidate].quality(), assets[best].quality()) {
		return selected // FF-092: a losing candidate cannot retire another keeper.
	}
	kept := selected[:0]
	for _, incumbent := range selected {
		if !match[candidate][incumbent] {
			kept = append(kept, incumbent)
		}
	}
	return append(kept, candidate)
}

// restoreUnsupported keeps existing selected nodes, then restores unsupported
// observations in first-observation order. Edges mean substitute -> replaced.
// The diagnostic supplies direct matches, not semantic substitution evidence.
// This is neither a minimum cover nor an arrival-independent selector.
func restoreUnsupported(seen, selected []int, substitutes [][]bool) ([]int, []int) {
	selected = append([]int(nil), selected...)
	var restored []int
	for _, observed := range seen {
		if !hasSelectedSupport(observed, selected, substitutes) {
			selected = append(selected, observed)
			restored = append(restored, observed)
		}
	}
	return selected, restored
}

// unsupportedNodes reports only observations without a currently selected
// direct representative. A path through another hidden node never qualifies.
func unsupportedNodes(seen, selected []int, substitutes [][]bool) []int {
	var unsupported []int
	for _, observed := range seen {
		if !hasSelectedSupport(observed, selected, substitutes) {
			unsupported = append(unsupported, observed)
		}
	}
	return unsupported
}

// hasSelectedSupport accepts self-selection or one direct, directional edge.
func hasSelectedSupport(observed int, selected []int, substitutes [][]bool) bool {
	for _, keeper := range selected {
		if observed == keeper || substitutes[keeper][observed] {
			return true
		}
	}
	return false
}

// validateRestorationGraph rejects malformed or mixed-scope replay inputs.
func validateRestorationGraph(assets []asset, match [][]bool, order []int) error {
	if len(assets) == 0 || len(match) != len(assets) {
		return fmt.Errorf("restoration requires a nonempty square match graph")
	}
	ids := make(map[string]bool)
	for i, item := range assets {
		if item.id == "" || ids[item.id] || len(match[i]) != len(assets) {
			return fmt.Errorf("invalid restoration identity or matrix row %d", i)
		}
		ids[item.id] = true
		if item.eventID != assets[0].eventID || item.verified != assets[0].verified || item.hashVersion != assets[0].hashVersion {
			return fmt.Errorf("restoration cannot cross event, verification, or hash-version pools")
		}
	}
	for _, index := range order {
		if index < 0 || index >= len(assets) {
			return fmt.Errorf("invalid restoration arrival index %d", index)
		}
	}
	for i := range match {
		if !match[i][i] {
			return fmt.Errorf("restoration match graph lacks self identity %d", i)
		}
		for j := range match {
			if match[i][j] != match[j][i] {
				return fmt.Errorf("restoration replay requires symmetric direct dHash evidence")
			}
		}
	}
	return nil
}
