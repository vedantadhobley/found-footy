// overlap_report_test.go — Reports preserve keeper labels and reject incompatible evidence.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// TestOverlapReportsPreserveReviewCorpora compares all prior judgments and predictions unchanged.
func TestOverlapReportsPreserveReviewCorpora(t *testing.T) {
	for _, path := range []string{"testdata/reviewed-pairs.json", "testdata/cadence-pairs-2026-09-08.json"} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var corpus reviewedPairCorpus
			if err := json.Unmarshal(raw, &corpus); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := writePairCorpusOverlapJSON(&output, bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(&output)
			for _, pair := range corpus.Cases {
				var row overlapReport
				if err := decoder.Decode(&row); err != nil {
					t.Fatal(err)
				}
				if row.PairID != pair.ID || row.Current != pair.Current || row.Left.AssetID != pair.Left.AssetID ||
					row.Right.AssetID != pair.Right.AssetID || row.Left.FrameHashes != "" || row.Experiment != "aligned-sections-v1" {
					t.Fatalf("changed prior evidence or emitted raw hashes: %+v", row)
				}
				if pair.Human.DedupDecision == "" {
					if row.Human != nil {
						t.Fatal("invented a human label")
					}
				} else if row.Human == nil || !reflect.DeepEqual(*row.Human, pair.Human) {
					t.Fatal("changed accepted label")
				}
				if row.Left.SHA256 != pair.Left.SHA256 || row.Right.SHA256 != pair.Right.SHA256 ||
					row.Left.ExactObservations != pair.Left.ExactObservations {
					t.Fatal("lost provenance")
				}
				if !pair.Current.Matches && len(row.Routes) != 0 {
					t.Fatal("invented a match for a nonmatching pair")
				}
			}
			var extra overlapReport
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				t.Fatalf("trailing report: %v", err)
			}
		})
	}
}

// TestOverlapCSVScopeAndOrder proves the cheap report preserves production comparison pools.
func TestOverlapCSVScopeAndOrder(t *testing.T) {
	a := overlapTestAsset(overlapTrace(80))
	a.id, a.eventID = "a", "event"
	b, c, d := a, a, a
	b.id = "b"
	c.id, c.verified = "c", true
	d.id, d.eventID = "d", "other-event"
	var left, right bytes.Buffer
	if err := writeOverlapJSON(&left, []asset{a, b, c, d}); err != nil {
		t.Fatal(err)
	}
	if err := writeOverlapJSON(&right, []asset{d, c, b, a}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left.Bytes(), right.Bytes()) {
		t.Fatal("input order changed output")
	}
	var row overlapReport
	if err := json.Unmarshal(bytes.TrimSpace(left.Bytes()), &row); err != nil {
		t.Fatal(err)
	}
	if row.Left.AssetID != "a" || row.Right.AssetID != "b" || row.Human != nil {
		t.Fatal("crossed a pool or invented a label")
	}
	b.hashVersion = dvideo.CurrentFrameHashVersion(.2)
	if err := writeOverlapJSON(io.Discard, []asset{a, b}); err == nil {
		t.Fatal("mislabelled hash cadence")
	}
	if err := writeOverlapJSON(overlapFailWriter{}, []asset{a, a}); err == nil {
		t.Fatal("ignored writer failure")
	}
	a.hashVersion, b.hashVersion = dvideo.LegacyFrameHashVersion, dvideo.LegacyFrameHashVersion
	left.Reset()
	if err := writeOverlapJSON(&left, []asset{a, b}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bytes.TrimSpace(left.Bytes()), &row); err != nil {
		t.Fatal(err)
	}
	if row.HashCadenceMS != nil || len(row.Routes) == 0 {
		t.Fatal("lost legacy evidence or invented cadence")
	}
}

// TestOverlapCorpusRejectsDrift prevents accidental comparisons under a changed sampling contract.
func TestOverlapCorpusRejectsDrift(t *testing.T) {
	original := loadReviewedPairCorpus(t)
	for _, mutate := range []func(*reviewedPairCorpus){
		func(c *reviewedPairCorpus) { c.HashCadenceMS = 200 },
		func(c *reviewedPairCorpus) { c.Matcher.Primary.MaxHamming++ },
		func(c *reviewedPairCorpus) { c.Cases[0].Current.Matches = !c.Cases[0].Current.Matches },
		func(c *reviewedPairCorpus) { c.Cases[0].Left.EventID = "other" },
		func(c *reviewedPairCorpus) { c.Cases[0].Right.HashVersion = "unknown" },
		func(c *reviewedPairCorpus) { c.Cases[0].Right.FrameHashes = "bad" },
		func(c *reviewedPairCorpus) { c.Cases[1].ID = c.Cases[0].ID },
	} {
		corpus := original
		corpus.Cases = append([]reviewedPair(nil), original.Cases...)
		mutate(&corpus)
		raw, err := json.Marshal(corpus)
		if err != nil {
			t.Fatal(err)
		}
		if err := writePairCorpusOverlapJSON(io.Discard, bytes.NewReader(raw)); err == nil {
			t.Fatal("accepted corpus drift")
		}
	}
	if err := writePairCorpusOverlapJSON(io.Discard, bytes.NewBufferString("{")); err == nil {
		t.Fatal("accepted malformed JSON")
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePairCorpusOverlapJSON(overlapFailWriter{}, bytes.NewReader(raw)); err == nil {
		t.Fatal("ignored writer failure")
	}
}

// TestOverlapFlags keeps the new output mode opt-in and unambiguous.
func TestOverlapFlags(t *testing.T) {
	for _, test := range []struct{ review, overlap, pairs, valid bool }{
		{false, false, false, true}, {false, true, false, true}, {false, true, true, true},
		{true, true, false, false}, {false, false, true, false},
	} {
		if err := validateOverlapFlags(test.review, test.overlap, test.pairs); (err == nil) != test.valid {
			t.Fatalf("flags %+v: %v", test, err)
		}
	}
}

// TestOverlapNaturalBoundaries pins measured positions, not replacement thresholds.
func TestOverlapNaturalBoundaries(t *testing.T) {
	raw, err := os.ReadFile("testdata/cadence-pairs-2026-09-08.json")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writePairCorpusOverlapJSON(&output, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ leftStart, rightStart, length, hits int }{
		"el-khannouss-39": {16, 0, 73, 73},
		"adams-82":        {15, 0, 74, 73},
		"palacios-42":     {42, 0, 51, 49},
		"mitchell-35":     {11, 0, 82, 79},
		"mariano-36":      {90, 0, 54, 54},
	}
	decoder := json.NewDecoder(&output)
	for range len(want) {
		var row overlapReport
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		expected, exists := want[row.PairID]
		if !exists || len(row.Routes) != 2 {
			t.Fatalf("unexpected pair or routes: %s", row.PairID)
		}
		sustained := row.Routes[1]
		if sustained.Route != "sustained" || len(sustained.Sections) != 1 {
			t.Fatal("route snapshot drift")
		}
		section := sustained.Sections[0]
		if section.Left.Start != expected.leftStart || section.Right.Start != expected.rightStart ||
			section.Left.End-section.Left.Start != expected.length || section.SimilarFrames != expected.hits {
			t.Fatalf("%s: %+v, want %+v", row.PairID, section, expected)
		}
		if row.PairID == "adams-82" && (sustained.Left.SupportedCoverage >= .8 ||
			row.Human == nil || row.Human.QualityWinner != "right" || row.Human.DedupDecision != "collapse") {
			t.Fatal("Adams must expose why 80% supported coverage cannot replace the accepted judgment")
		}
	}
}

// overlapFailWriter supplies deterministic output failure without filesystem writes.
type overlapFailWriter struct{}

// Write proves the report does not return success after a partial output failure.
func (overlapFailWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }
