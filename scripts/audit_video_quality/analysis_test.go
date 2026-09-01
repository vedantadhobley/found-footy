// analysis_test.go — FF-081 order-sensitivity and bridge-replay regressions.
package main

import (
	"strings"
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// TestCurrentPolicyDansoCycleDependsOnArrivalOrder proves the retained
// production-shaped quality relation cannot produce one canonical keeper.
func TestCurrentPolicyDansoCycleDependsOnArrivalOrder(t *testing.T) {
	assets := []asset{
		{id: "a", durationMS: 8_900, bitrate: 1_308_000, width: 1280, height: 720},
		{id: "b", durationMS: 8_400, bitrate: 1_475_000, width: 1280, height: 720},
		{id: "c", durationMS: 10_100, bitrate: 960_000, width: 1280, height: 720},
	}
	match := completeMatchMatrix(len(assets))
	outcomes := make(map[string]struct{})
	visited := 0
	exhaustive := visitOrders(len(assets), 100, func(order []int) {
		outcomes[simulateCurrentPolicy(assets, match, order)] = struct{}{}
		visited++
	})
	if !exhaustive || visited != 6 {
		t.Fatalf("orders exhaustive=%t visited=%d, want true/6", exhaustive, visited)
	}
	if len(outcomes) < 2 {
		t.Fatalf("current-policy outcomes = %v, want arrival-order sensitivity", outcomes)
	}
	anchoredOutcomes := make(map[string]struct{})
	visitOrders(len(assets), 100, func(order []int) {
		anchoredOutcomes[simulateKeeperPolicy(assets, match, order, anchoredBandWinner)] = struct{}{}
	})
	if len(anchoredOutcomes) < 2 {
		t.Fatalf("anchored outcomes = %v, want proof that set-level bands remain non-associative", anchoredOutcomes)
	}
	if strictWinner(assets).id == anchoredBandWinner(assets).id {
		t.Fatal("strict and anchored-band comparisons should expose the Danso policy choice")
	}
}

// TestBridgeArrivalCanLeaveTwoLiveAssets proves a total quality comparator
// alone cannot make a non-transitive match graph arrival-independent.
func TestBridgeArrivalCanLeaveTwoLiveAssets(t *testing.T) {
	assets := []asset{
		{id: "a", durationMS: 10_000, bitrate: 3_000_000, width: 1280, height: 720},
		{id: "b", durationMS: 10_000, bitrate: 1_000_000, width: 1280, height: 720},
		{id: "c", durationMS: 10_000, bitrate: 2_000_000, width: 1280, height: 720},
	}
	match := [][]bool{
		{true, true, false},
		{true, true, true},
		{false, true, true},
	}
	bridgeFirst := simulateCurrentPolicy(assets, match, []int{1, 0, 2})
	bridgeLast := simulateCurrentPolicy(assets, match, []int{0, 2, 1})
	if len(strings.Split(bridgeFirst, ",")) != 2 {
		t.Fatalf("bridge-first result = %q, want two surviving endpoints", bridgeFirst)
	}
	if bridgeLast != "a" {
		t.Fatalf("bridge-last result = %q, want canonical a", bridgeLast)
	}
	anchoredBridgeFirst := simulateKeeperPolicy(assets, match, []int{1, 0, 2}, anchoredBandWinner)
	anchoredBridgeLast := simulateKeeperPolicy(assets, match, []int{0, 2, 1}, anchoredBandWinner)
	if anchoredBridgeFirst == anchoredBridgeLast {
		t.Fatalf("anchored bridge outcomes = %q/%q, want topology sensitivity to remain explicit",
			anchoredBridgeFirst, anchoredBridgeLast)
	}
	if !isBridgeNode(match, 1) {
		t.Fatal("middle asset should be classified as a bridge")
	}
}

// TestBuildPoolGraphRetainsHistoricalConnectivity keeps an old supersession
// relation in the audit component after matcher-policy drift removes its
// current dHash edge. Replay still sees the current matrix; only corpus
// reconstruction uses the union.
func TestBuildPoolGraphRetainsHistoricalConnectivity(t *testing.T) {
	assets := []asset{
		{id: "a", supersededBy: "b", frameHashes: repeatedHash(0, primaryMinRun)},
		{id: "b", frameHashes: repeatedHash(^uint64(0), primaryMinRun)},
	}
	graph := buildPoolGraph(assets)
	if graph.match[0][1] {
		t.Fatal("opposite hashes should not pass the current matcher")
	}
	if !graph.connect[0][1] {
		t.Fatal("historical supersession should retain component connectivity")
	}
	components := connectedComponents(graph.connect)
	if len(components) != 1 || len(components[0]) != 2 {
		t.Fatalf("components = %v, want one two-asset component", components)
	}
}

// TestLongestAuditWindowMatchesGapPolicy pins the diagnostic used to explain
// production bridge edges without exposing domain internals solely for an
// audit command.
func TestDomainAlignmentEvidenceMatchesGapPolicy(t *testing.T) {
	a := []uint64{0, 0, 0, 0, 0}
	b := []uint64{0, 0, ^uint64(0), 0, 0}
	if got := dvideo.BestAlignment(a, b, 0, 0).Frames; got != 2 {
		t.Fatalf("window without gaps = %d, want 2", got)
	}
	if got := dvideo.BestAlignment(a, b, 0, 1).Frames; got != 5 {
		t.Fatalf("window with one gap = %d, want 5", got)
	}
}

// completeMatchMatrix builds an all-to-all symmetric test graph.
func completeMatchMatrix(size int) [][]bool {
	matrix := make([][]bool, size)
	for i := range matrix {
		matrix[i] = make([]bool, size)
		for j := range matrix[i] {
			matrix[i][j] = true
		}
	}
	return matrix
}

// repeatedHash builds the smallest matcher-shaped sequence for graph tests.
func repeatedHash(value uint64, size int) []uint64 {
	hashes := make([]uint64, size)
	for i := range hashes {
		hashes[i] = value
	}
	return hashes
}
