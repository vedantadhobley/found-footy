// presentation_test.go — Quality-frontier, visibility and direct-gap audit regressions.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

func presentationTestAsset(id string, verified bool, support, width, height, bitrate int, fps float64, hash uint64) asset {
	hashes := make([]uint64, 60)
	for i := range hashes {
		hashes[i] = hash
	}
	return asset{
		id: id, eventID: "event", fixtureID: 42, playerName: "Player", minute: 25,
		homeTeam: "Home", awayTeam: "Away", hashVersion: dvideo.CurrentFrameHashVersion(0.1),
		frameHashes: hashes, width: width, height: height, durationMS: 12_000,
		fileSizeBytes: int64(bitrate), bitrate: bitrate, frameRate: fps,
		popularity: support, observedPopularity: support, verified: verified,
		shareState: "active", shareID: "s_" + id, firstSeenAt: id, shareCreatedAt: id,
	}
}

// TestPresentationRaphinhaShape keeps a hidden technical upgrade independent
// from a popular lower-quality clip and sends unanchored unverified media to review.
func TestPresentationRaphinhaShape(t *testing.T) {
	popular := presentationTestAsset("popular-720", true, 20, 1280, 720, 1_930_000, 60, 0)
	upgrade := presentationTestAsset("hidden-upgrade", true, 1, 1466, 828, 2_980_000, 60, ^uint64(0))
	long1080 := presentationTestAsset("long-1080", false, 1, 1920, 1080, 11_380_000, 30, 0xaaaaaaaaaaaaaaaa)
	popular.durationMS = 32_500
	long1080.durationMS = 55_400

	report, err := planPresentationReport([]asset{popular, upgrade, long1080})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.EventsAffectedBySingletonRule != 1 || report.Summary.HiddenRoots != 2 ||
		report.Summary.HiddenWithoutDirectSubstitute != 2 || report.Summary.CurrentPrimaryTechnicalLosses != 1 {
		t.Fatalf("summary = %+v", report.Summary)
	}
	event := report.Events[0]
	if event.PrimaryStatus != "ambiguous" || event.ProposedPrimary != "" ||
		!slices.Equal(event.CurrentVisibleOrder, []string{popular.id}) || len(event.ProposedCandidateLayers) != 0 ||
		!slices.Equal(event.PrimaryTechnicalFrontier, []string{upgrade.id}) ||
		!slices.Equal(event.PrimaryStandaloneFrontier, []string{upgrade.id, popular.id}) {
		t.Fatalf("event = %+v", event)
	}
	if len(event.ManualReview) != 1 || event.ManualReview[0].AssetID != long1080.id ||
		event.ManualReview[0].Reason != "unverified_without_direct_visual_anchor" {
		t.Fatalf("manual review = %+v", event.ManualReview)
	}
}

// TestPresentationSuppressesOnlyDirectCoveredMedia proves support alone is not
// the suppression edge; a selected clip must directly cover the singleton.
func TestPresentationSuppressesOnlyDirectCoveredMedia(t *testing.T) {
	primary := presentationTestAsset("primary", true, 3, 1920, 1080, 6_000_000, 60, 0)
	duplicate := presentationTestAsset("duplicate", true, 1, 1280, 720, 1_500_000, 30, 0)

	report, err := planPresentationReport([]asset{duplicate, primary})
	if err != nil {
		t.Fatal(err)
	}
	event := report.Events[0]
	if event.ProposedPrimary != primary.id || len(event.ProposedCandidateLayers) != 0 || len(event.Suppressed) != 1 ||
		event.Suppressed[0].AssetID != duplicate.id || len(event.HiddenWithoutSubstitute) != 0 {
		t.Fatalf("event = %+v", event)
	}
	if len(event.Suppressed[0].DirectSubstitutes) != 1 || event.Suppressed[0].MaxCandidateCoverage != 1 {
		t.Fatalf("suppression evidence = %+v", event.Suppressed[0])
	}
}

// TestPresentationLeavesCadenceResolutionTradeoffAmbiguous refuses a fitted
// scalar when 1080p30 and 720p60 occupy different technical frontiers.
func TestPresentationLeavesCadenceResolutionTradeoffAmbiguous(t *testing.T) {
	highResolution := presentationTestAsset("1080p30", true, 2, 1920, 1080, 6_000_000, 30, 0)
	highCadence := presentationTestAsset("720p60", true, 2, 1280, 720, 2_000_000, 60, ^uint64(0))

	report, err := planPresentationReport([]asset{highResolution, highCadence})
	if err != nil {
		t.Fatal(err)
	}
	event := report.Events[0]
	if event.PrimaryStatus != "ambiguous" || len(event.PrimaryTechnicalFrontier) != 2 ||
		len(event.PrimaryStandaloneFrontier) != 2 ||
		len(event.ProposedCandidateLayers) != 0 {
		t.Fatalf("event = %+v", event)
	}
}

// TestPresentationLayersIndependentVerifiedContext ensures a popular primary
// cannot suppress a verified root that it does not directly cover. A layer is
// unordered because metadata does not establish a preference between peers.
func TestPresentationLayersIndependentVerifiedContext(t *testing.T) {
	primary := presentationTestAsset("primary", true, 3, 1920, 1080, 6_000_000, 60, 0)
	independent := presentationTestAsset("independent", true, 1, 1280, 720, 1_500_000, 30, ^uint64(0))

	report, err := planPresentationReport([]asset{independent, primary})
	if err != nil {
		t.Fatal(err)
	}
	event := report.Events[0]
	if event.ProposedPrimary != primary.id || len(event.ProposedCandidateLayers) != 1 ||
		!slices.Equal(event.ProposedCandidateLayers[0], []string{independent.id}) || len(event.Suppressed) != 0 {
		t.Fatalf("event = %+v", event)
	}
}

// TestPresentationOutputAndFlags keep the mode deterministic, exclusive and
// honest about writer failures.
func TestPresentationOutputAndFlags(t *testing.T) {
	if validatePresentationFlags(true, false, false, false, false, false, false) != nil ||
		validatePresentationFlags(true, false, true) == nil || validatePresentationFlags(false, true) != nil {
		t.Fatal("presentation mode validation failed")
	}
	a := presentationTestAsset("a", true, 1, 1280, 720, 2_000_000, 60, 0)
	var left, right bytes.Buffer
	if err := writePresentationJSON(&left, []asset{a}); err != nil {
		t.Fatal(err)
	}
	if err := writePresentationJSON(&right, []asset{a}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left.Bytes(), right.Bytes()) {
		t.Fatal("presentation output is not deterministic")
	}
	var report presentationReport
	if err := json.Unmarshal(left.Bytes(), &report); err != nil || report.Experiment != "quality-first-presentation-v1" {
		t.Fatalf("unexpected report: %v", err)
	}
	if err := writePresentationJSON(presentationFailWriter{}, []asset{a}); err == nil {
		t.Fatal("writer failure ignored")
	}
}

type presentationFailWriter struct{}

func (presentationFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
