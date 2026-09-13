// Popularity experiments conserve source observations and expose missing historical evidence.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// popularityTestBridge pins the 2/2/20 example with two nonmatching selected
// cuts and a hidden longer clip that directly matches both.
func popularityTestBridge() []asset {
	trace := overlapTrace(120)
	a := overlapTestAsset(trace[:60])
	a.id, a.eventID, a.shareID, a.shareState = "A", "event", "share-A", "active"
	a.observedPopularity, a.popularity = 2, 2
	b := a
	b.id, b.shareID, b.frameHashes, b.popularity = "B", "share-B", trace[60:], 22
	c := a
	c.id, c.shareID, c.shareState, c.supersededBy = "C", "share-C", "superseded", "B"
	c.frameHashes, c.observedPopularity, c.popularity = trace, 20, 20
	items := []asset{a, b, c}
	for i := range items {
		items[i].md5, items[i].fixtureID = fmt.Sprintf("%032x", i+1), 1
		items[i].fileSizeBytes = 1_000_000
		items[i].firstSeenAt = fmt.Sprintf("2026-09-12T00:00:0%dZ", i)
	}
	return items
}

// TestPopularityConservesBridgeSupport checks fixed exact ownership, ranges,
// deterministic serialization and a strict rank reversal without quality changes.
func TestPopularityConservesBridgeSupport(t *testing.T) {
	assets := popularityTestBridge()
	r := analyzePopularity(assets)
	if len(r.Excluded) != 0 || r.ExactTotal != 24 || r.RootTotal != 24 || len(r.Bridges) != 1 || len(r.Moves) != 1 {
		t.Fatalf("unexpected report: %+v", r)
	}
	if *r.Roots[0].Minimum != 2 || *r.Roots[0].Maximum != 22 || *r.Roots[1].Minimum != 2 || *r.Roots[1].Maximum != 22 {
		t.Fatalf("range is not conserved: %+v", r.Roots)
	}
	m := r.Moves[0]
	if m.Counts["A"] != 22 || m.Counts["B"] != 2 || !m.StrictRankReversed || m.VisibilityChanged {
		t.Fatalf("unexpected move: %+v", m)
	}
	if assets[1].popularity != 22 || assets[2].supersededBy != "B" {
		t.Fatal("analysis mutated recorded evidence")
	}
	var first, reversed bytes.Buffer
	if err := writePopularityJSON(&first, assets); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(assets)
	if err := writePopularityJSON(&reversed, assets); err != nil || !bytes.Equal(first.Bytes(), reversed.Bytes()) {
		t.Fatalf("order-dependent output: %v", err)
	}
}

// TestPopularityPreservesVisibilityAsymmetry pins singleton moves and the rule
// that unverified threshold clips cannot hide timestamp-verified singletons.
func TestPopularityPreservesVisibilityAsymmetry(t *testing.T) {
	assets := popularityTestBridge()
	assets[0].observedPopularity, assets[0].popularity = 1, 1
	assets[1].observedPopularity, assets[1].popularity = 1, 21
	r := analyzePopularity(assets)
	if len(r.Moves) != 1 || !r.Moves[0].VisibilityChanged || !slices.Equal(r.Visible, []string{"B"}) ||
		!slices.Equal(r.Moves[0].Visible, []string{"A"}) {
		t.Fatalf("singleton visibility drift: %+v", r)
	}
	assets[0].verified = true
	counts := map[string]int{"A": 1, "B": 21}
	if got := popularityVisible(assets[:2], counts); !slices.Equal(got, []string{"A", "B"}) {
		t.Fatalf("unverified hid verified: %v", got)
	}
	assets[0].verified, assets[1].verified = false, true
	if got := popularityVisible(assets[:2], counts); !slices.Equal(got, []string{"B"}) {
		t.Fatalf("verified threshold failed: %v", got)
	}
	counts["A"], counts["B"] = 1, 2
	if got := popularityVisible(assets[:2], counts); len(got) != 2 {
		t.Fatalf("popularity two hid a singleton: %v", got)
	}
}

// TestPopularityWithholdsInferredScores ensures aggregate holes, removed media
// state and cycles never masquerade as trustworthy zero-support alternatives.
func TestPopularityWithholdsInferredScores(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]asset)
		reason string
	}{
		{"missing", func(a []asset) { a[2].observedPopularity = 0 }, "missing_exact_attribution"},
		{"same-total-wrong-roots", func(a []asset) { a[0].popularity++; a[1].popularity-- }, "per_root_count_mismatch"},
		{"removed-root", func(a []asset) { a[0].shareState = "removed" }, "nonpublic_recorded_root"},
		{"removed-event", func(a []asset) { a[0].eventRemoved = true }, "removed_event"},
		{"reclaimed-root", func(a []asset) { a[0].objectReclaimedAt = "2026-09-12" }, "reclaimed_recorded_root"},
		{"cycle", func(a []asset) { a[1].supersededBy = "C" }, "missing_or_cyclic_lineage"},
		{"missing-owner", func(a []asset) { a[2].supersededBy = "other-event" }, "missing_or_cyclic_lineage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := popularityTestBridge()
			tc.mutate(a)
			r := analyzePopularity(a)
			if !slices.Contains(r.Excluded, tc.reason) || len(r.Moves) != 0 || r.Visible != nil {
				t.Fatalf("untrusted scoring: %+v", r)
			}
			for _, root := range r.Roots {
				if root.Minimum != nil || root.Maximum != nil {
					t.Fatal("unknown scores were presented as precise ranges")
				}
			}
		})
	}
	a := popularityTestBridge()
	a[2].shareID, a[2].shareState = "", "observed"
	r := analyzePopularity(a)
	if len(r.Bridges) != 1 || r.Bridges[0].OwnShare || len(r.Moves) != 0 || len(r.Excluded) != 0 || len(r.ConditionalMoves) != 1 {
		t.Fatalf("inherited validation became own acceptance: %+v", r)
	}
}

// TestPopularityRestorationSplitsRealCountsWithoutFabricatingValidation checks
// that restoration can expose ownership ambiguity without inventing vote counts.
func TestPopularityRestorationSplitsRealCountsWithoutFabricatingValidation(t *testing.T) {
	a := popularityTestBridge()
	a[0].supersededBy, a[0].shareState = "C", "superseded"
	a[1].popularity = 24
	p, err := projectPopularityRestoration(a)
	if err != nil || !slices.Equal(p.Restored, []string{"A"}) || len(p.Bridges) != 1 || len(p.Moves) != 1 {
		t.Fatalf("bad projected restoration: %+v, %v", p, err)
	}
	if p.Roots[0].Assigned != 2 || p.Roots[1].Assigned != 22 {
		t.Fatalf("votes were not split: %+v", p.Roots)
	}
	a[0].shareID, a[0].shareState = "", "observed"
	p, err = projectPopularityRestoration(a)
	if err != nil || len(p.Restored) != 0 || p.Roots[0].Assigned != 24 {
		t.Fatalf("never-public validation invented: %+v, %v", p, err)
	}
}

// TestPopularitySavedSeptember11 is an opt-in offline regression over the saved
// source-count corpus. No missing counts, candidate identities or media checks
// are silently upgraded to historical proof.
func TestPopularitySavedSeptember11(t *testing.T) {
	path := os.Getenv("FF_POPULARITY_CORPUS")
	if path == "" {
		t.Skip("set FF_POPULARITY_CORPUS to the converted September 11 export")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	items, err := readAssets(f)
	if err != nil {
		t.Fatal(err)
	}
	var selected []asset
	for _, a := range items {
		if a.eventID == "01dedc54-da16-4e3c-9539-ac43c81ffd77" {
			selected = append(selected, a)
		}
	}
	if len(selected) != 10 {
		t.Fatalf("expected the saved Maitland-Niles event, got %d assets", len(selected))
	}
	if r := analyzePopularity(selected); len(r.Excluded) != 0 || r.ExactTotal != 34 {
		t.Fatalf("recorded aggregate changed: %+v", r)
	}
	p, err := projectPopularityRestoration(selected)
	if err != nil || !slices.Equal(p.Restored, []string{"32b50be4-0ad5-50a5-8ebe-f590cc4b57a4"}) ||
		len(p.Moves) != 0 || len(p.ConditionalMoves) != 2 {
		t.Fatalf("changed historical projection: %+v %v", p, err)
	}
	for _, move := range p.ConditionalMoves {
		if !move.VisibilityChanged || move.StrictRankReversed {
			t.Fatalf("conditional influence changed: %+v", move)
		}
	}
}

// TestPopularityUsesDirectScopedMatches protects the production hash routes
// and bucket boundaries without introducing a transitive cluster score.
func TestPopularityUsesDirectScopedMatches(t *testing.T) {
	a := popularityTestBridge()
	if popularityDirectMatch(a[0], a[1]) || !popularityDirectMatch(a[0], a[2]) || !popularityDirectMatch(a[1], a[2]) {
		t.Fatal("synthetic bridge does not have expected direct edges")
	}
	for _, mutate := range []func(*asset){
		func(a *asset) { a.verified = !a.verified },
		func(a *asset) { a.eventID = "other" },
		func(a *asset) { a.hashVersion = dvideo.LegacyFrameHashVersion },
	} {
		b := a[2]
		mutate(&b)
		if popularityDirectMatch(a[0], b) {
			t.Fatal("matching crossed a scope boundary")
		}
	}
	a[2].frameHashes = a[1].frameHashes
	if r := analyzePopularity(a); len(r.Bridges) != 0 || len(r.Moves) != 0 {
		t.Fatal("single-owner variant was considered ambiguous")
	}
}

// TestPopularityRankLeavesTiesUnknown does not substitute first-seen time for
// missing share creation time or let unverified popularity overtake verification.
func TestPopularityRankLeavesTiesUnknown(t *testing.T) {
	a := popularityTestBridge()
	if popularityPartialOrder(a[0], a[1], 2, 2) != 0 {
		t.Fatal("fabricated tie resolution")
	}
	a[0].verified = true
	if popularityPartialOrder(a[0], a[1], 1, 999) != -1 {
		t.Fatal("popularity outranked verification")
	}
}

// TestPopularityReportRejectsInvalidModesAndInputs prevents successful-looking
// partial output and propagates write errors.
func TestPopularityReportRejectsInvalidModesAndInputs(t *testing.T) {
	for _, mutate := range []func([]asset){
		func(a []asset) { a[1].id = a[0].id },
		func(a []asset) { a[1].hashVersion = "future" },
		func(a []asset) { a[1].observedPopularity = -1 },
	} {
		a := popularityTestBridge()
		mutate(a)
		var out bytes.Buffer
		if err := writePopularityJSON(&out, a); err == nil || out.Len() != 0 {
			t.Fatal("invalid input accepted")
		}
	}
	if err := writePopularityJSON(overlapFailWriter{}, popularityTestBridge()); err == nil {
		t.Fatal("write failure ignored")
	}
	if validatePopularityFlags(true, false, true) == nil || validatePopularityFlags(false, true) != nil {
		t.Fatal("output mode validation failed")
	}
	var out bytes.Buffer
	if err := writePopularityJSON(&out, popularityTestBridge()); err != nil {
		t.Fatal(err)
	}
	var r popularityReport
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Experiment != "recorded-root-support-v1" {
		t.Fatalf("unexpected output contract: %v", err)
	}
}
