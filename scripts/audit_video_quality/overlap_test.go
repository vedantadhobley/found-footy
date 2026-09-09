// overlap_test.go — Containment evidence must expose edges, holes, and alignment limits.
package main

import (
	"math/rand"
	"reflect"
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// TestOverlapContainsShortCut preserves both source coordinates and directional coverage.
func TestOverlapContainsShortCut(t *testing.T) {
	short := overlapTestAsset(overlapTrace(80))
	long := overlapTestAsset(append(append(repeatedHash(0, 30), short.frameHashes...), repeatedHash(0, 30)...))
	routes := measureOverlap(short, long, measureMatcherEvidence(short, long))
	if len(routes) != 2 {
		t.Fatalf("qualified routes = %d, want both", len(routes))
	}
	for _, route := range routes {
		if len(route.Sections) != 1 || route.OffsetFrames != 30 || route.SupportedFrames != 80 ||
			route.Left.SupportedCoverage != 1 || route.Right.SupportedCoverage != float64(80)/140 ||
			route.Right.UnsupportedPrefixFrames != 30 || route.Right.UnsupportedSuffixFrames != 30 {
			t.Fatalf("containment evidence: %+v", route)
		}
		if route.Sections[0].alignedInterval != intervalAt(0, 80, 30) {
			t.Fatal("lost cut coordinates")
		}
	}
}

// TestOverlapExposesInteriorHole distinguishes 100% aligned span from supported samples.
func TestOverlapExposesInteriorHole(t *testing.T) {
	left := overlapTestAsset(overlapTrace(105))
	right := overlapTestAsset(append([]uint64(nil), left.frameHashes...))
	for i := 40; i < 65; i++ {
		right.frameHashes[i] = ^right.frameHashes[i]
	}
	evidence := measureMatcherEvidence(left, right)
	baseline := evaluateStableOffsetSubstitution(left, right, evidence)
	if baseline.coverageClass != coverageEquivalent || baseline.leftCoverage != 1 {
		t.Fatalf("control should expose old full-span classification: %+v", baseline)
	}
	for _, route := range measureOverlap(left, right, evidence) {
		if len(route.Sections) != 2 || route.SupportedFrames != 80 || route.SectionSpanFrames != 80 ||
			!reflect.DeepEqual(route.Left.InteriorGaps, []frameInterval{{40, 65}}) ||
			route.Left.SupportedCoverage != float64(80)/105 {
			t.Fatalf("interior mismatch swallowed: %+v", route)
		}
	}
}

// TestOverlapRetainsBriefMisses allows local hash noise without crediting it as shared footage.
func TestOverlapRetainsBriefMisses(t *testing.T) {
	left, right := overlapTrace(100), overlapTrace(100)
	right[40], right[41] = ^right[40], ^right[41]
	got := segmentOffset(left, right, "primary", dvideo.AlignmentEvidence{}, 12)
	if len(got.Sections) != 1 || got.SupportedFrames != 98 || got.SectionSpanFrames != 100 ||
		got.Left.SupportedCoverage != .98 || got.Left.SectionSpanCoverage != 1 ||
		!reflect.DeepEqual(got.Sections[0].ToleratedGaps, []alignedInterval{intervalAt(40, 42, 0)}) {
		t.Fatalf("brief gap evidence: %+v", got)
	}
}

// TestOverlapKeepsUnmatchedEdges prevents intros/outros from becoming disposable by position.
func TestOverlapKeepsUnmatchedEdges(t *testing.T) {
	core := overlapTrace(100)
	left := append(append(repeatedHash(0, 20), core...), repeatedHash(0, 10)...)
	right := append(append(repeatedHash(^uint64(0), 5), core...), repeatedHash(^uint64(0), 30)...)
	got := segmentOffset(left, right, "primary", dvideo.AlignmentEvidence{LeftStart: 20, RightStart: 5}, 12)
	if len(got.Sections) != 1 || got.Left.UnsupportedPrefixFrames != 20 || got.Left.UnsupportedSuffixFrames != 10 ||
		got.Right.UnsupportedPrefixFrames != 5 || got.Right.UnsupportedSuffixFrames != 30 {
		t.Fatalf("edge evidence: %+v", got)
	}
}

// TestOverlapDoesNotJoinChangedOffsets leaves an insertion's second timeline unsupported.
func TestOverlapDoesNotJoinChangedOffsets(t *testing.T) {
	left := overlapTrace(100)
	right := append(append(append([]uint64(nil), left[:50]...), repeatedHash(0, 10)...), left[50:]...)
	got := segmentOffset(left, right, "primary", dvideo.AlignmentEvidence{}, 12)
	if len(got.Sections) != 1 || got.SupportedFrames != 50 || got.Left.UnsupportedSuffixFrames != 50 {
		t.Fatalf("different offsets combined: %+v", got)
	}
}

// TestOverlapDiscardsIsolatedAndSparseHits reports weak evidence without enlarging sections.
func TestOverlapDiscardsIsolatedAndSparseHits(t *testing.T) {
	left, right := overlapTrace(100), overlapTrace(100)
	for i := 50; i < len(right); i++ {
		right[i] = ^right[i]
	}
	right[90] = left[90]
	got := segmentOffset(left, right, "primary", dvideo.AlignmentEvidence{}, 12)
	if got.SupportedFrames != 50 || got.RawSimilarFrames != 51 || got.UnassignedSimilarFrames != 1 ||
		got.Left.UnsupportedSuffixFrames != 50 {
		t.Fatalf("isolated hit became a section: %+v", got)
	}
	for i := range right {
		right[i] = left[i]
		if i%3 != 0 {
			right[i] = ^right[i]
		}
	}
	got = segmentOffset(left, right, "primary", dvideo.AlignmentEvidence{}, 12)
	if len(got.Sections) != 0 || got.SupportedFrames != 0 || got.Left.UnsupportedPrefixFrames != len(left) {
		t.Fatalf("sparse matches became supported coverage: %+v", got)
	}
}

// TestOverlapRequiresQualifiedRoute keeps the new evidence behind the existing match gate.
func TestOverlapRequiresQualifiedRoute(t *testing.T) {
	a := overlapTestAsset(overlapTrace(29))
	if got := measureOverlap(a, a, measureMatcherEvidence(a, a)); len(got) != 0 {
		t.Fatalf("short clip invented a qualified match: %+v", got)
	}
	a = overlapTestAsset(overlapTrace(50))
	b := overlapTestAsset(append([]uint64(nil), a.frameHashes...))
	for i := range b.frameHashes {
		b.frameHashes[i] ^= (1 << 14) - 1
	}
	got := measureOverlap(a, b, measureMatcherEvidence(a, b))
	if len(got) != 1 || got[0].Route != "sustained" || got[0].MaxHamming != 16 {
		t.Fatalf("routes combined or relabelled: %+v", got)
	}
}

// TestOverlapConservesSamplesAndSwaps checks accounting across varied fragmented timelines.
func TestOverlapConservesSamplesAndSwaps(t *testing.T) {
	rng := rand.New(rand.NewSource(8181))
	for trial := 0; trial < 100; trial++ {
		left := overlapTrace(150)
		right := append(repeatedHash(0, 7), left...)
		for i := range right {
			if rng.Intn(5) == 0 {
				right[i] = ^right[i]
			}
		}
		forward := segmentOffset(left, right, "primary", dvideo.AlignmentEvidence{RightStart: 7}, 12)
		backward := segmentOffset(right, left, "primary", dvideo.AlignmentEvidence{LeftStart: 7}, 12)
		if !reflect.DeepEqual(forward.Left, backward.Right) || !reflect.DeepEqual(forward.Right, backward.Left) ||
			forward.SupportedFrames != backward.SupportedFrames || forward.OffsetFrames != -backward.OffsetFrames {
			t.Fatal("swapping changed evidence")
		}
		supported, span, misses := 0, 0, 0
		for _, section := range forward.Sections {
			supported += section.SimilarFrames
			span += section.Left.End - section.Left.Start
			for _, gap := range section.ToleratedGaps {
				misses += gap.Left.End - gap.Left.Start
			}
		}
		if supported != forward.SupportedFrames || span != forward.SectionSpanFrames || span != supported+misses ||
			forward.RawSimilarFrames != supported+forward.UnassignedSimilarFrames {
			t.Fatal("sample accounting drift")
		}
		for _, side := range []overlapSide{forward.Left, forward.Right} {
			total := side.UnsupportedPrefixFrames + side.UnsupportedSuffixFrames + span
			for _, gap := range side.InteriorGaps {
				total += gap.End - gap.Start
			}
			if total != side.TotalFrames {
				t.Fatal("timeline partition lost samples")
			}
		}
	}
}

// overlapTrace gives synthetic frames distinct identities to prevent constant-hash alignments.
func overlapTrace(count int) []uint64 {
	rng := rand.New(rand.NewSource(81083))
	out := make([]uint64, count)
	for i := range out {
		out[i] = rng.Uint64()
	}
	return out
}

// overlapTestAsset isolates coverage from quality-floor differences.
func overlapTestAsset(hashes []uint64) asset {
	return asset{frameHashes: hashes, width: 1280, height: 720, bitrate: 2_000_000,
		frameRate: 30, durationMS: len(hashes) * 100, hashVersion: dvideo.CurrentFrameHashVersion(.1)}
}

// BenchmarkOverlapEvidence separates existing alignment cost from added linear segmentation.
func BenchmarkOverlapEvidence(b *testing.B) {
	left := overlapTestAsset(overlapTrace(600))
	right := overlapTestAsset(append(repeatedHash(0, 30), left.frameHashes...))
	evidence := measureMatcherEvidence(left, right)
	b.Run("existing_two_route_alignment", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			measureMatcherEvidence(left, right)
		}
	})
	b.Run("added_section_evidence", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			measureOverlap(left, right, evidence)
		}
	})
}
