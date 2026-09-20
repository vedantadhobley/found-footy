// coverage_sweep_test.go — Exhaustive offsets, union coverage and fail-open policy regressions.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// coverageSweepAsset constructs one current-cadence playable root for focused
// timeline-policy tests.
func coverageSweepAsset(id string, hashes []uint64) asset {
	return asset{
		id: id, eventID: "event", hashVersion: dvideo.CurrentFrameHashVersion(0.1),
		frameHashes: hashes, width: 1280, height: 720, durationMS: len(hashes) * currentHashCadenceMS,
		bitrate: 2_000_000, frameRate: 30, popularity: 1, observedPopularity: 1,
		shareID: "s_" + id, shareState: "active", verified: true,
	}
}

// TestExhaustiveCoverageCombinesDistinctSelectedSections proves two direct
// clips may jointly cover a candidate without introducing a transitive edge.
func TestExhaustiveCoverageCombinesDistinctSelectedSections(t *testing.T) {
	leftHashes := make([]uint64, 70)
	for i := range leftHashes {
		leftHashes[i] = uint64(i + 1000)
	}
	candidate := coverageSweepAsset("candidate", leftHashes)
	first := coverageSweepAsset("first", append([]uint64(nil), leftHashes[:35]...))
	second := coverageSweepAsset("second", append([]uint64(nil), leftHashes[35:]...))
	profile := measureCandidateCoverage(candidate, []asset{first, second})
	if profile.UncoveredSamples != 0 || profile.CoveredSamples != 70 ||
		!slices.Equal(profile.DirectSelectedAssets, []string{"first", "second"}) {
		t.Fatalf("profile = %+v", profile)
	}
	if !coverageWithinTolerance(profile, coverageTolerances[0]) {
		t.Fatal("full section-span union coverage did not qualify")
	}
}

// TestCoverageToleranceSeparatesEdgeTailsFromInteriorGaps keeps a small edge
// allowance from erasing an equally sized unsupported interior edit.
func TestCoverageToleranceSeparatesEdgeTailsFromInteriorGaps(t *testing.T) {
	tolerance := coverageTolerance{MaxTotalMS: 1000, MaxFraction: 0.10, MaxInteriorMS: 200}
	edge := candidateCoverage{HashCadenceMS: intPointer(100), DirectSelectedAssets: []string{"selected"},
		TotalSamples: 100, CoveredSamples: 92, UncoveredSamples: 8, UncoveredFraction: .08,
		UnsupportedPrefix: 4, UnsupportedSuffix: 4, InteriorGaps: []frameInterval{}}
	if !coverageWithinTolerance(edge, tolerance) {
		t.Fatal("bounded edge tails should qualify")
	}
	edge.InteriorGaps = []frameInterval{{Start: 40, End: 48}}
	if coverageWithinTolerance(edge, tolerance) {
		t.Fatal("material interior gap should fail open")
	}
}

// TestCoverageUnknownCadenceFailsOpen prevents legacy hash counts from being
// presented as measured time.
func TestCoverageUnknownCadenceFailsOpen(t *testing.T) {
	profile := candidateCoverage{DirectSelectedAssets: []string{"selected"}, TotalSamples: 50, CoveredSamples: 50}
	if coverageWithinTolerance(profile, coverageTolerances[0]) {
		t.Fatal("unknown cadence was automatically covered")
	}
}

// TestCoverageEligibilityPrecedesCoverage preserves the accepted FF-078
// boundary: graph coverage can suppress an eligible clip but cannot make a
// weak singleton public merely because its footage differs.
func TestCoverageEligibilityPrecedesCoverage(t *testing.T) {
	primary := coverageSweepAsset("primary", make([]uint64, 40))
	primary.popularity = 3
	hidden := coverageSweepAsset("hidden", make([]uint64, 40))
	for i := range hidden.frameHashes {
		hidden.frameHashes[i] = ^uint64(0)
	}
	visible := presentationCurrentOrder([]asset{primary, hidden})
	if len(visible) != 1 || visible[0].id != primary.id {
		t.Fatalf("visible = %+v, want only threshold clip", presentationIDs(visible))
	}
	selected, covered, _, _, err := planCoverageEvent(visible, coverageTolerances[0])
	if err != nil {
		t.Fatal(err)
	}
	if selected != 1 || len(covered) != 0 {
		t.Fatalf("selected=%d covered=%d, want 1/0", selected, len(covered))
	}
}

// TestCoverageQualityPrecedesSuppression prevents a selected presentation from
// hiding a sharper copy merely because their footage aligns.
func TestCoverageQualityPrecedesSuppression(t *testing.T) {
	hashes := make([]uint64, 40)
	candidate := coverageSweepAsset("candidate", hashes)
	candidate.width, candidate.height = 1920, 1080
	selected := coverageSweepAsset("selected", hashes)
	selected.width, selected.height = 640, 360
	if got := coverageQualitySubstitutes(candidate, []asset{selected}); len(got) != 0 {
		t.Fatalf("quality substitutes = %v, want none", presentationIDs(got))
	}
	profile := measureCandidateCoverage(candidate, coverageQualitySubstitutes(candidate, []asset{selected}))
	if coverageWithinTolerance(profile, coverageTolerances[0]) {
		t.Fatal("lower-quality selected clip suppressed the sharper candidate")
	}
}

// TestCoveragePairReportPreservesHumanLabels keeps the threshold experiment
// diagnostic and pinned to the accepted matcher contract.
func TestCoveragePairReportPreservesHumanLabels(t *testing.T) {
	raw, err := os.ReadFile("testdata/reviewed-pairs.json")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writeCoveragePairJSON(&output, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	var report coveragePairReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) == 0 || report.Cases[0].Human.DedupDecision == "" ||
		report.Cases[0].LeftCoveredByRight.CandidateID == "" {
		t.Fatalf("incomplete pair report: %+v", report)
	}
	for _, id := range []string{"raphinha-14-distinct-broadcasts", "raphinha-37-tactical-analysis"} {
		pair := coveragePairByID(t, report, id)
		if pair.Human.DedupDecision != "keep_both" ||
			pair.LeftCoveredByRight.CoveredAt[coverageTolerances[0].Name] ||
			pair.RightCoveredByLeft.CoveredAt[coverageTolerances[0].Name] {
			t.Fatalf("keep-both pair %s was covered: %+v", id, pair)
		}
	}
	if err := validateCoveragePairFlags(true, false, true); err == nil {
		t.Fatal("combined coverage pair mode was accepted")
	}
}

// TestCoveragePairReportKeepsContentSeparateFromQuality pins the Adams case:
// the longer left copy contains the right copy's footage, while the accepted
// human quality winner is still the shorter right presentation.
func TestCoveragePairReportKeepsContentSeparateFromQuality(t *testing.T) {
	raw, err := os.ReadFile("testdata/cadence-pairs-2026-09-08.json")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writeCoveragePairJSON(&output, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	var report coveragePairReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	pair := coveragePairByID(t, report, "adams-82")
	fullSpan := coverageTolerances[0].Name
	if pair.Human.QualityWinner != "right" || pair.LeftCoveredByRight.CoveredAt[fullSpan] ||
		!pair.RightCoveredByLeft.CoveredAt[fullSpan] {
		t.Fatalf("Adams coverage/quality boundary drifted: %+v", pair)
	}
}

// coveragePairByID resolves one stable reviewed case for focused assertions.
func coveragePairByID(t *testing.T, report coveragePairReport, id string) coveragePairCase {
	t.Helper()
	for _, pair := range report.Cases {
		if pair.ID == id {
			return pair
		}
	}
	t.Fatalf("missing coverage pair %q", id)
	return coveragePairCase{}
}

// intPointer supplies explicit cadence in compact policy test fixtures.
func intPointer(value int) *int { return &value }
