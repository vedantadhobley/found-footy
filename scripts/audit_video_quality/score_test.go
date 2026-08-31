// score_test.go — deterministic FF-081 score and fit-order regressions.
package main

import "testing"

// TestWeightedBetterIsTotal pins stable identity as the final metadata tie.
func TestWeightedBetterIsTotal(t *testing.T) {
	a := asset{id: "a", durationMS: 10_000, bitrate: 1_000_000, width: 1280, height: 720}
	b := a
	b.id = "b"
	if !weightedBetter(a, b, 1, 0.5) || weightedBetter(b, a, 1, 0.5) {
		t.Fatal("metadata ties should resolve once by stable asset identity")
	}
}

// TestBetterFitPrioritizesRetainedDecisions prevents cosmetic winner agreement
// from hiding a score that reverses more acyclic production supersessions.
func TestBetterFitPrioritizesRetainedDecisions(t *testing.T) {
	left := policyComparison{acyclicEdgeReversals: 1, bandWinnerDiffs: 10}
	right := policyComparison{acyclicEdgeReversals: 2, bandWinnerDiffs: 0}
	if !betterFit(left, right) {
		t.Fatal("fewer acyclic edge reversals should win the diagnostic fit")
	}
}
