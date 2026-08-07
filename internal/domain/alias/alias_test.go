// Tests for TeamAlias types + Normalize + state helpers.
package alias_test

import (
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
)

// helpers ---------------------------------------------------------

func strPtr(s string) *string { return &s }

func makeClub() *alias.TeamAlias {
	return alias.New(
		530, "Atlético de Madrid", false,
		strPtr("ATM"), strPtr("Spain"), strPtr("Madrid"),
		time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC),
	)
}

// Normalize ------------------------------------------------------

func TestNormalize_StripsDiacritics(t *testing.T) {
	cases := map[string]string{
		"Atlético":       "Atletico",
		"Bayern München": "Bayern Munchen",
		"Señor":          "Senor",
		"Málaga":         "Malaga",
		"São Paulo":      "Sao Paulo",
		"Beşiktaş":       "Besiktas",
		"Spartak":        "Spartak", // pure ASCII, unchanged
		"":               "",        // empty, unchanged
	}
	for in, want := range cases {
		if got := alias.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalize_PreservesCyrillicWithoutCombiningMarks(t *testing.T) {
	// Cyrillic team names without combining marks (most Cyrillic
	// letters have precomposed forms that NFD doesn't split) pass
	// through unchanged. This documents the load-bearing case for
	// Russian / Ukrainian / Serbian league fixtures if we ever expand
	// scope past top-5 European leagues.
	//
	// NOT tested: CJK — dakuten and similar are combining marks that
	// Normalize would strip, corrupting the word. See Normalize's
	// docstring; script-aware handling if we ever need it.
	if got := alias.Normalize("Спартак Москва"); got != "Спартак Москва" {
		t.Errorf("Normalize Cyrillic = %q, want unchanged", got)
	}
}

func TestNormalize_PreservesCase(t *testing.T) {
	// Normalize is diacritic-only, NOT case folding — search-query
	// builders that want lowercase apply strings.ToLower separately.
	if got := alias.Normalize("Atlético MADRID"); got != "Atletico MADRID" {
		t.Errorf("Normalize preserved-case = %q, want case preserved", got)
	}
}

// TeamAlias construction -----------------------------------------

func TestNew_PopulatesPhase1FieldsAndLeavesPhase2Nil(t *testing.T) {
	ta := makeClub()

	if ta.TeamID != 530 {
		t.Errorf("TeamID = %d, want 530", ta.TeamID)
	}
	if ta.CanonicalName != "Atlético de Madrid" {
		t.Errorf("CanonicalName = %q, want 'Atlético de Madrid'", ta.CanonicalName)
	}
	if ta.IsNational {
		t.Error("IsNational should default to input (false for a club)")
	}
	if ta.TeamCode == nil || *ta.TeamCode != "ATM" {
		t.Errorf("TeamCode = %v, want ATM", ta.TeamCode)
	}
	if ta.Country == nil || *ta.Country != "Spain" {
		t.Errorf("Country = %v, want Spain", ta.Country)
	}
	if ta.City == nil || *ta.City != "Madrid" {
		t.Errorf("City = %v, want Madrid", ta.City)
	}
	// Phase-2 fields untouched
	if ta.WikidataQID != nil {
		t.Errorf("WikidataQID = %v, want nil", ta.WikidataQID)
	}
	if len(ta.Aliases) != 0 {
		t.Errorf("Aliases = %v, want empty", ta.Aliases)
	}
	if ta.ResolvedAt != nil {
		t.Errorf("ResolvedAt = %v, want nil", ta.ResolvedAt)
	}
}

// SetResolution --------------------------------------------------

func TestSetResolution_PopulatesPhase2Fields(t *testing.T) {
	ta := makeClub()
	at := time.Date(2026, 7, 8, 12, 15, 0, 0, time.UTC)
	aliases := []string{"atletico", "atleti", "atm"}

	ta.SetResolution("Q8701", aliases, at)

	if ta.WikidataQID == nil || *ta.WikidataQID != "Q8701" {
		t.Errorf("WikidataQID = %v, want Q8701", ta.WikidataQID)
	}
	if len(ta.Aliases) != 3 {
		t.Errorf("Aliases len = %d, want 3", len(ta.Aliases))
	}
	if ta.ResolvedAt == nil || !ta.ResolvedAt.Equal(at) {
		t.Errorf("ResolvedAt = %v, want %v", ta.ResolvedAt, at)
	}
	if !ta.IsResolved() {
		t.Error("IsResolved should be true after SetResolution")
	}
	if !ta.UpdatedAt.Equal(at) {
		t.Errorf("UpdatedAt = %v, want %v", ta.UpdatedAt, at)
	}
}

func TestSetResolution_CopiesInputSlice(t *testing.T) {
	ta := makeClub()
	source := []string{"atletico", "atleti"}
	ta.SetResolution("Q8701", source, time.Now())

	// Mutate the source AFTER SetResolution returns.
	source[0] = "MUTATED"

	if ta.Aliases[0] == "MUTATED" {
		t.Error("SetResolution stored source slice by reference; internal state can be mutated by caller")
	}
}

func TestSetResolution_EmptyAliasesStillCountsAsResolved(t *testing.T) {
	// Distinct-state requirement: ResolvedAt set + empty aliases means
	// "we ran the pipeline and no tokens survived filtering" — that's
	// still a resolution. Callers gate on IsResolved, not len(Aliases).
	ta := makeClub()
	ta.SetResolution("Q8701", []string{}, time.Now())
	if !ta.IsResolved() {
		t.Error("resolution with empty aliases must still count as resolved")
	}
}

// State helpers --------------------------------------------------

func TestIsResolved_DefaultFalse(t *testing.T) {
	if makeClub().IsResolved() {
		t.Error("IsResolved should default false for a placeholder")
	}
}
