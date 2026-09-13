// Report regressions keep restoration evidence deterministic, scoped and explicitly offline.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// TestRestorationReportPinsRealBridge compares every arrival order and makes
// input sorting and unknown own-share metadata visible without inferring votes.
func TestRestorationReportPinsRealBridge(t *testing.T) {
	assets := readMastantuonoRestorationAssets(t)
	assets[2].shareID = ""
	var first, repeat bytes.Buffer
	if err := writeRestorationJSON(&first, assets, 6); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(assets)
	if err := writeRestorationJSON(&repeat, assets, 6); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), repeat.Bytes()) {
		t.Fatal("input order changed report")
	}
	var report restorationReport
	if err := json.Unmarshal(bytes.TrimSpace(first.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	if report.Orders.Visited != 6 || !report.Orders.Exhaustive || report.Orders.Changed != 2 ||
		report.Orders.BaselineUnsupported != 2 || len(report.Orders.RestoredFinalSets) != 1 ||
		report.Orders.RestoredFinalSets["A,B"] != 6 || !slices.Equal(report.WithoutOwnShare, []string{"C"}) {
		t.Fatalf("unexpected real bridge report: %+v", report)
	}
	if report.Experiment != "direct-restoration-v1" || report.Scope != "selected_roots_before_visibility_not_a_repair_plan" {
		t.Fatal("report lost experimental boundary")
	}
	for _, item := range report.Assets {
		if item.FrameHashes != "" {
			t.Fatal("unnecessarily copied the raw hash corpus into the report")
		}
	}
}

// TestRestorationReportSeparatesPools and singleton handling ensure an asset
// from another event, verification bucket or hash version cannot cover a clip.
func TestRestorationReportSeparatesPools(t *testing.T) {
	a := overlapTestAsset(overlapTrace(80))
	a.id, a.eventID, a.firstSeenAt = "a", "event", "2026-09-12T00:00:00Z"
	b, c, d, e := a, a, a, a
	b.id = "b"
	c.id, c.verified = "c", true
	d.id, d.eventID = "d", "other"
	e.id, e.hashVersion = "e", dvideo.LegacyFrameHashVersion
	var output bytes.Buffer
	if err := writeRestorationJSON(&output, []asset{a, b, c, d, e}, 6); err != nil {
		t.Fatal(err)
	}
	var report restorationReport
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Assets) != 2 || report.Assets[0].AssetID != "a" || report.Assets[1].AssetID != "b" {
		t.Fatalf("scope crossed: %+v", report.Assets)
	}
}

// TestRestorationReportRejectsInvalidEvidence keeps partial reports from
// resembling successful analyses and checks all new command-mode boundaries.
func TestRestorationReportRejectsInvalidEvidence(t *testing.T) {
	for _, mutate := range []func([]asset){
		func(a []asset) { a[1].id = a[0].id },
		func(a []asset) { a[1].hashVersion = "unknown" },
		func(a []asset) { a[1].firstSeenAt = "" },
		func(a []asset) { a[1].frameHashes = nil },
	} {
		assets := readMastantuonoRestorationAssets(t)
		mutate(assets)
		var output bytes.Buffer
		if err := writeRestorationJSON(&output, assets, 6); err == nil || output.Len() != 0 {
			t.Fatal("invalid evidence produced a successful or partial report")
		}
	}
	assets := readMastantuonoRestorationAssets(t)
	for _, limit := range []int{0, -1, 100_001} {
		if err := writeRestorationJSON(io.Discard, assets, limit); err == nil {
			t.Fatal("invalid work bound accepted")
		}
	}
	if err := writeRestorationJSON(overlapFailWriter{}, assets, 6); err == nil {
		t.Fatal("writer failure ignored")
	}
	for _, flags := range [][3]bool{{true, false, false}, {false, true, false}, {false, false, true}} {
		if err := validateRestorationFlags(true, flags[0], flags[1], flags[2]); err == nil {
			t.Fatal("conflicting output mode accepted")
		}
	}
	if err := validateRestorationFlags(true, false, false, false); err != nil {
		t.Fatal(err)
	}
}
