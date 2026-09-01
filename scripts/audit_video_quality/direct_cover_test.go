// direct_cover_test.go — Deterministic direct-substitution set regressions.
package main

import (
	"math/bits"
	"math/rand"
	"reflect"
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

func TestBuildSubstitutionMatrixWritesBothDirections(t *testing.T) {
	assets := []asset{
		{id: "left", width: 1280, height: 720, bitrate: 2_000_000, frameHashes: make([]uint64, 100)},
		{id: "right", width: 1280, height: 720, bitrate: 2_000_000, frameHashes: make([]uint64, 100)},
	}
	match := [][]bool{{true, true}, {true, true}}
	evidence := [][]matcherEvidence{
		{{}, {primary: dvideo.AlignmentEvidence{Frames: 100}}},
		{{primary: dvideo.AlignmentEvidence{Frames: 100}}, {}},
	}
	matrix := buildSubstitutionMatrix(assets, match, evidence, evaluateCadenceAwareSubstitution)
	if !matrix[0][1] || !matrix[1][0] {
		t.Fatalf("matrix = %v, want mutual direct substitution", matrix)
	}
}

func TestMinimumDirectCoverUsesDirectBridgeEvidence(t *testing.T) {
	assets := []asset{{id: "a"}, {id: "bridge"}, {id: "c"}}
	substitutes := [][]bool{
		{true, false, false},
		{true, true, true},
		{false, false, true},
	}
	result := minimumDirectCover(assets, substitutes)
	if !result.exact || !reflect.DeepEqual(coverIDs(assets, result.selected), []string{"bridge"}) {
		t.Fatalf("cover = %+v ids=%v, want direct bridge", result, coverIDs(assets, result.selected))
	}
}

func TestMinimumDirectCoverNeverHidesByTransitiveReachability(t *testing.T) {
	assets := []asset{{id: "a"}, {id: "b"}, {id: "c"}}
	substitutes := [][]bool{
		{true, true, false},
		{false, true, true},
		{false, false, true},
	}
	result := minimumDirectCover(assets, substitutes)
	if len(result.selected) != 2 {
		t.Fatalf("selected = %v, want two direct representatives", coverIDs(assets, result.selected))
	}
	if isDirectCover(1, substitutes) {
		t.Fatal("a alone must not cover c through b")
	}
}

func TestMinimumDirectCoverTiebreaksOnExactObservationsThenID(t *testing.T) {
	assets := []asset{{id: "a", observedPopularity: 1}, {id: "b", observedPopularity: 3}}
	substitutes := [][]bool{{true, true}, {true, true}}
	result := minimumDirectCover(assets, substitutes)
	if !reflect.DeepEqual(coverIDs(assets, result.selected), []string{"b"}) || result.alternatives != 2 {
		t.Fatalf("cover = %v alternatives=%d, want popular b across two minima",
			coverIDs(assets, result.selected), result.alternatives)
	}
	assets[0].observedPopularity = 3
	result = minimumDirectCover(assets, substitutes)
	if !reflect.DeepEqual(coverIDs(assets, result.selected), []string{"a"}) {
		t.Fatalf("equal-vote cover = %v, want stable lexical a", coverIDs(assets, result.selected))
	}
}

func TestMinimumDirectCoverFailsVisibleAboveExactBound(t *testing.T) {
	assets := make([]asset, maxExactCoverAssets+1)
	substitutes := make([][]bool, len(assets))
	for i := range assets {
		assets[i].id = string(rune('a' + i))
		substitutes[i] = make([]bool, len(assets))
		substitutes[i][i] = true
	}
	result := minimumDirectCover(assets, substitutes)
	if result.exact || len(result.selected) != len(assets) {
		t.Fatalf("bounded cover = exact=%t selected=%d, want every asset visible",
			result.exact, len(result.selected))
	}
}

func TestMinimumDirectCoverIsMinimalAcrossRandomRelations(t *testing.T) {
	rng := rand.New(rand.NewSource(81)) //nolint:gosec // deterministic test relation
	for trial := 0; trial < 200; trial++ {
		size := 1 + rng.Intn(8)
		assets := make([]asset, size)
		substitutes := make([][]bool, size)
		for left := 0; left < size; left++ {
			assets[left].id = string(rune('a' + left))
			substitutes[left] = make([]bool, size)
			for right := 0; right < size; right++ {
				substitutes[left][right] = left == right || rng.Intn(4) == 0
			}
		}
		result := minimumDirectCover(assets, substitutes)
		mask := uint64(0)
		for _, selected := range result.selected {
			mask |= uint64(1) << selected
		}
		if !result.exact || !isDirectCover(mask, substitutes) {
			t.Fatalf("trial %d invalid result %+v", trial, result)
		}
		for candidate := uint64(1); candidate < uint64(1)<<size; candidate++ {
			if bits.OnesCount64(candidate) < len(result.selected) && isDirectCover(candidate, substitutes) {
				t.Fatalf("trial %d selected %d but smaller mask %b covers", trial, len(result.selected), candidate)
			}
		}
	}
}
