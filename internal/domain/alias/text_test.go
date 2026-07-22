// Unit tests for the tokenizer — specifically the
// Latin-with-diacritics-kept vs non-Latin-script-dropped rule that
// governs multilingual alias handling.
package alias

import (
	"sort"
	"testing"
)

// equalSets compares two string slices as sets (order-independent,
// nil == empty slice). Used across the tokenizer tests because the
// tokenizer's output is orderless in intent and reflect.DeepEqual
// treats []string{} != nil.
func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// TestTokenize_LatinWithDiacriticsPreserved — the load-bearing rule.
// Non-English Latin-script tokens like München / Atlético / São Paulo /
// Fußball must survive the tokenizer as ASCII-folded forms, because
// English tweets DO write German/Spanish/Portuguese team names both
// with and without their native diacritics.
func TestTokenize_LatinWithDiacriticsPreserved(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Bayern München", []string{"bayern", "munchen"}},
		{"Atlético Madrid", []string{"atletico", "madrid"}},
		{"São Paulo FC", []string{"sao", "paulo"}},                     // fc dropped by ≤2 later? no, fc has 2 chars so ≤2 filter drops it
		{"Fußball-Club", []string{"fussball", "club"}},                  // ß→ss, dash split, both >2 chars — skip-list is downstream
		{"España", []string{"espana"}},
		{"Sevilla FC", []string{"sevilla"}},                             // fc dropped by len ≤2
		{"Nîmes Olympique", []string{"nimes", "olympique"}},
		{"L'Olympique", []string{"lolympique"}},                         // apostrophe stripped, kept as one token
		{"Bayer 04 Leverkusen", []string{"bayer", "leverkusen"}},        // "04" dropped as all-digit
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := tokenize(tc.in)
			if !equalSets(got, tc.want) {
				t.Errorf("tokenize(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestTokenize_NonLatinScriptDropped — non-Latin scripts (Chinese,
// Greek, Cyrillic, Arabic, Japanese, Korean) don't decompose to ASCII
// via NFD and get dropped by the hasNonASCII check.
func TestTokenize_NonLatinScriptDropped(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"红魔", nil},                    // Chinese "Red Devil" — dropped
		{"γαλαζιοι", nil},               // Greek "the blues" — dropped
		{"οι", nil},                     // Greek article "the" — also ≤2 chars but ALSO non-Latin
		{"الأهلي", nil},                 // Arabic "Al-Ahly" — dropped
		{"Спартак", nil},                // Cyrillic "Spartak" — dropped
		{"ヴィッセル神戸", nil},              // Japanese "Vissel Kobe" — dropped
		{"레알 마드리드", nil},               // Korean "Real Madrid" — dropped
		{"Manchester 红魔 United", []string{"manchester", "united"}}, // mixed — Latin kept, CJK dropped
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := tokenize(tc.in)
			if !equalSets(got, tc.want) {
				t.Errorf("tokenize(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestTokenize_SkipListStillWorks — spot-check that the existing
// filters (≤2 chars, all-digit, camel-concat) still fire alongside
// the new non-Latin check.
func TestTokenize_ShortAndDigitFiltered(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"F.C. Barcelona", []string{"barcelona"}},   // "fc" ≤2 (also skip-listed but that's downstream)
		{"1899 Hoffenheim", []string{"hoffenheim"}}, // "1899" all-digit
		{"AC Milan", []string{"milan"}},              // "ac" ≤2
		{"", nil},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := tokenize(tc.in)
			if !equalSets(got, tc.want) {
				t.Errorf("tokenize(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestTokenize_ConcatFormsDropped — Wikidata sometimes stores aliases
// as period-separated concats like "A.C.F.Fiorentina" (English alias
// for Q2052). After stripPunct collapses the periods we get
// "ACFFiorentina" — should be caught by isCamelConcat's upper→lower
// pattern (added 2026-07-23 after the O3/d smoke test surfaced
// `acffiorentina` in Fiorentina's alias set).
//
// Also verifies the classic camelCase concat still drops
// (LiverpoolFC, FCBarcelona).
func TestTokenize_ConcatFormsDropped(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"A.C.F.Fiorentina", nil},   // dot-concat → ACFFiorentina → upper→lower pattern → drop
		{"S.S.C.Napoli", nil},        // same pattern
		{"LiverpoolFC", nil},         // classic camelCase → drop
		{"FCBarcelona", nil},         // acronym+word already tested elsewhere; assert drop
		{"AtléticoMadrid", nil},      // NFD strip runs BEFORE camel check on the tokenizer path,
		                              // but this specific test drives tokenize() directly and
		                              // the diacritic stripping still applies via norm.NFD
		{"ACF Fiorentina", []string{"acf", "fiorentina"}}, // space-separated: both survive
		{"F.C. Barcelona", []string{"barcelona"}},         // fc drops via ≤2
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := tokenize(tc.in)
			if !equalSets(got, tc.want) {
				t.Errorf("tokenize(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestStripKnownOrgSuffix — the acronym-rescue helper.
func TestStripKnownOrgSuffix(t *testing.T) {
	cases := []struct {
		in         string
		wantPrefix string
		wantOK     bool
	}{
		{"psgfc", "psg", true},       // 5 chars, prefix 3 chars, fires
		{"nycfc", "nyc", true},        // 5 chars, prefix 3 chars, fires
		{"mufc", "", false},           // 4 chars, prefix "mu" only 2 chars — guard
		{"nufc", "", false},           // same
		{"avfc", "", false},           // same
		{"rmcf", "", false},           // 4 chars, prefix "rm" 2 chars
		{"cfc", "", false},            // 3 chars, len < 5 minimum
		{"barcelona", "", false},      // doesn't end in known suffix
		{"scp", "", false},            // 3 chars total, doesn't hit minimum
		{"fcbm", "", false},           // 4 chars, ends in "bm" — not in suffix list
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			gotPrefix, gotOK := stripKnownOrgSuffix(tc.in)
			if gotOK != tc.wantOK || gotPrefix != tc.wantPrefix {
				t.Errorf("stripKnownOrgSuffix(%q) = (%q, %v); want (%q, %v)",
					tc.in, gotPrefix, gotOK, tc.wantPrefix, tc.wantOK)
			}
		})
	}
}
