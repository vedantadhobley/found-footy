// substitution_test.go — Experimental coverage/substitution policy regressions.
package main

import (
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

func TestEvaluateSubstitutionSeparatesCoverageFromQuality(t *testing.T) {
	base := asset{
		width: 1280, height: 720, bitrate: 2_000_000, frameRate: 30,
		frameHashes: make([]uint64, 100),
	}
	tests := []struct {
		name       string
		left       asset
		right      asset
		frames     int
		wantClass  coverageClass
		wantAction string
	}{
		{
			name: "equivalent coverage and quality collapses",
			left: base, right: base, frames: 95,
			wantClass: coverageEquivalent, wantAction: "collapse_equivalent",
		},
		{
			name: "comparable superset replaces subset",
			left: func() asset {
				item := base
				item.frameHashes = make([]uint64, 200)
				return item
			}(),
			right: base, frames: 95,
			wantClass: coverageLeftContainsRight, wantAction: "collapse_right",
		},
		{
			name: "lower quality superset coexists",
			left: func() asset {
				item := base
				item.width, item.height = 640, 360
				item.frameHashes = make([]uint64, 200)
				return item
			}(),
			right: base, frames: 95,
			wantClass: coverageLeftContainsRight, wantAction: "keep_both",
		},
		{
			name: "partial overlap coexists",
			left: base, right: base, frames: 60,
			wantClass: coveragePartial, wantAction: "keep_both",
		},
		{
			name: "technical tradeoff coexists",
			left: func() asset {
				item := base
				item.width, item.height, item.bitrate = 1920, 1080, 2_100_000
				return item
			}(),
			right: base, frames: 95,
			wantClass: coverageEquivalent, wantAction: "keep_both",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			measured := matcherEvidence{primary: dvideo.AlignmentEvidence{Frames: test.frames}}
			decision := evaluateSubstitution(test.left, test.right, measured)
			if decision.coverageClass != test.wantClass || decision.action() != test.wantAction {
				t.Fatalf("decision = class %s action %s, want %s/%s",
					decision.coverageClass, decision.action(), test.wantClass, test.wantAction)
			}
		})
	}
}

func TestSimulateSubstitutionPolicyDoesNotCollapseThroughBridge(t *testing.T) {
	assets := []asset{
		{id: "a", width: 1280, height: 720, bitrate: 2_000_000, frameHashes: make([]uint64, 100)},
		{id: "bridge", width: 1280, height: 720, bitrate: 2_000_000, frameHashes: make([]uint64, 200)},
		{id: "c", width: 1280, height: 720, bitrate: 2_000_000, frameHashes: make([]uint64, 100)},
	}
	match := [][]bool{{true, true, false}, {true, true, true}, {false, true, true}}
	evidence := make([][]matcherEvidence, 3)
	for i := range evidence {
		evidence[i] = make([]matcherEvidence, 3)
	}
	ab := matcherEvidence{primary: dvideo.AlignmentEvidence{Frames: 60}}
	bc := matcherEvidence{primary: dvideo.AlignmentEvidence{Frames: 60}}
	evidence[0][1], evidence[1][0] = ab, ab.swapped()
	evidence[1][2], evidence[2][1] = bc, bc.swapped()

	if got := simulateSubstitutionPolicy(assets, match, evidence, []int{0, 2, 1}); got != "a,bridge,c" {
		t.Fatalf("bridge replay = %q, want all partial-overlap presentations", got)
	}
}

func TestStableOffsetAggregationRecoversSplitSharedTimeline(t *testing.T) {
	leftHashes := make([]uint64, 100)
	rightHashes := make([]uint64, 100)
	for index := 0; index < len(rightHashes); index += 4 {
		rightHashes[index] = ^uint64(0)
	}
	left := asset{
		width: 1280, height: 720, bitrate: 2_000_000, frameRate: 30, frameHashes: leftHashes,
	}
	right := left
	right.frameHashes = rightHashes
	measured := matcherEvidence{primary: dvideo.AlignmentEvidence{Frames: 30}}

	contiguous := evaluateSubstitution(left, right, measured)
	if contiguous.coverageClass != coveragePartial || contiguous.action() != "keep_both" {
		t.Fatalf("contiguous decision = %s/%s, want partial/keep_both",
			contiguous.coverageClass, contiguous.action())
	}
	aggregate := measureStableOffset(left, right, measured)
	if aggregate.overlapFrames != 100 || aggregate.similarFrames != 75 || aggregate.similarity != 0.75 {
		t.Fatalf("stable evidence = %+v, want 100 overlap / 75 similar / 0.75", aggregate)
	}
	stable := evaluateStableOffsetSubstitution(left, right, measured)
	if stable.coverageClass != coverageEquivalent || stable.action() != "collapse_equivalent" {
		t.Fatalf("stable decision = %s/%s, want equivalent/collapse",
			stable.coverageClass, stable.action())
	}
}

func TestStableOffsetAggregationRejectsWeakTimeline(t *testing.T) {
	leftHashes := make([]uint64, 100)
	rightHashes := make([]uint64, 100)
	for index := 0; index < 26; index++ {
		rightHashes[index] = ^uint64(0)
	}
	left := asset{
		width: 1280, height: 720, bitrate: 2_000_000, frameRate: 30, frameHashes: leftHashes,
	}
	right := left
	right.frameHashes = rightHashes
	measured := matcherEvidence{primary: dvideo.AlignmentEvidence{Frames: 30}}
	decision := evaluateStableOffsetSubstitution(left, right, measured)
	if decision.coverageClass != coveragePartial || decision.action() != "keep_both" {
		t.Fatalf("stable decision = %s/%s, want weak timeline retained separately",
			decision.coverageClass, decision.action())
	}
}
