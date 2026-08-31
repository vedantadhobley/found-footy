// review_test.go — Direct-pair review-manifest contract tests.
package main

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
)

func TestWriteReviewCSVSeparatesEvidenceFromLabels(t *testing.T) {
	left := asset{
		id: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", eventID: "event", fixtureID: 42,
		homeTeam: "Home", awayTeam: "Away", playerName: "Player", minute: 67,
		shareID: "s_left", shareState: "active", sourceTweetURL: "https://x.com/a/status/1",
		width: 1920, height: 1080, durationMS: 10_000, bitrate: 4_000_000, frameRate: 60,
		popularity: 3, verified: true,
	}
	right := asset{
		id: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", eventID: "event", fixtureID: 42,
		homeTeam: "Home", awayTeam: "Away", playerName: "Player", minute: 67,
		shareID: "s_right", shareState: "active", sourceTweetURL: "https://x.com/b/status/2",
		width: 1280, height: 720, durationMS: 9_000, bitrate: 2_000_000, frameRate: 30,
		popularity: 2, verified: true,
	}
	finding := componentFinding{
		assets: []asset{left, right}, matchEdges: []matchEdge{{leftID: left.id, rightID: right.id, primaryWindow: 30}},
		outcomes: map[string]int{"one": 1}, terminalIDs: []string{left.id, right.id},
	}

	var output bytes.Buffer
	if err := writeReviewCSV(&output, auditResult{findings: []componentFinding{finding}}); err != nil {
		t.Fatalf("writeReviewCSV: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(output.String())).ReadAll()
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if len(records) != 2 || len(records[0]) != len(reviewHeader) || len(records[1]) != len(reviewHeader) {
		t.Fatalf("manifest shape = %d rows, %d/%d columns", len(records), len(records[0]), len(records[1]))
	}
	columns := make(map[string]int, len(records[0]))
	for i, name := range records[0] {
		columns[name] = i
	}
	row := records[1]
	if row[columns["left_frame_rate"]] != "60.000000" || row[columns["right_frame_rate"]] != "30.000000" {
		t.Errorf("frame rates = %q/%q", row[columns["left_frame_rate"]], row[columns["right_frame_rate"]])
	}
	for _, label := range []string{"dedup_decision", "quality_winner", "quality_reasons", "notes"} {
		if row[columns[label]] != "" {
			t.Errorf("label %s = %q, want blank", label, row[columns[label]])
		}
	}
}
