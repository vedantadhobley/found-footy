// Unit tests for api-name-based candidate selection (nameTokenSet +
// pickBestNameMatch) — the fix that replaced Wikipedia's rank order and
// stopped "Sporting CP B" outranking "Sporting CP" (decisions.md 2026-07-24).
package alias

import (
	"testing"

	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
)

// TestNameTokenSet_KeepsShortDiscriminators — unlike tokenize, name
// matching must KEEP ≤2-char tokens (cp, b, ii, fc) because those are the
// discriminators between a senior side and its B team / women's side.
func TestNameTokenSet_KeepsShortDiscriminators(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Sporting CP", []string{"sporting", "cp"}},
		{"Sporting CP B", []string{"sporting", "cp", "b"}},
		{"Real Madrid Castilla", []string{"real", "madrid", "castilla"}},
		{"Paris Saint-Germain", []string{"paris", "saint", "germain"}},      // dash split
		{"Bayern München", []string{"bayern", "munchen"}},                   // unidecode ö
		{"Sporting CP (football)", []string{"sporting", "cp", "football"}},  // parens split
	}
	for _, tc := range cases {
		got := nameTokenSet(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("nameTokenSet(%q) = %v (size %d), want %v", tc.in, got, len(got), tc.want)
			continue
		}
		for _, w := range tc.want {
			if _, ok := got[w]; !ok {
				t.Errorf("nameTokenSet(%q) missing %q; got %v", tc.in, w, got)
			}
		}
	}
}

// TestPickBestNameMatch — the candidate slices list the B team FIRST
// (mirroring Wikipedia's rank order, which is what caused the bug), so a
// pass proves selection overrides Wikipedia's ranking and matches the api
// name in BOTH directions: senior when api names the senior, B when api
// names the B side (which happens in friendlies).
func TestPickBestNameMatch(t *testing.T) {
	sporting := []wikipedia.Hit{
		{Title: "Sporting CP B", WikidataQID: "Q3494112"}, // Wikipedia rank 1
		{Title: "Sporting CP", WikidataQID: "Q75729"},     // Wikipedia rank 2
	}
	real := []wikipedia.Hit{
		{Title: "Real Madrid", WikidataQID: "QSENIOR"},
		{Title: "Real Madrid Castilla", WikidataQID: "QCASTILLA"},
	}
	cases := []struct {
		name    string
		apiName string
		hits    []wikipedia.Hit
		wantQID string
	}{
		{"senior beats B-team ranked first (the bug)", "Sporting CP", sporting, "Q75729"},
		{"B-team wins when api names it", "Sporting CP B", sporting, "Q3494112"},
		{"senior Real Madrid", "Real Madrid", real, "QSENIOR"},
		{"Castilla when api names it", "Real Madrid Castilla", real, "QCASTILLA"},
		{"single candidate returned as-is", "Anytown FC",
			[]wikipedia.Hit{{Title: "Anytown FC", WikidataQID: "Q1"}}, "Q1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickBestNameMatch(tc.apiName, tc.hits)
			if got.WikidataQID != tc.wantQID {
				t.Errorf("pickBestNameMatch(%q) = %s (%q); want %s",
					tc.apiName, got.WikidataQID, got.Title, tc.wantQID)
			}
		})
	}
}
