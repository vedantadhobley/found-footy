// Tests for TeamAlias types + Normalize + state helpers.
package alias_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
)

// helpers ---------------------------------------------------------

func strPtr(s string) *string { return &s }

func makeClub() *alias.TeamAlias {
	return alias.New(
		530, "Atlético de Madrid", false,
		strPtr("Spain"), strPtr("Madrid"),
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

func TestNew_PopulatesInputFieldsAndLeavesResolvedNil(t *testing.T) {
	ta := makeClub()

	if ta.TeamID != 530 {
		t.Errorf("TeamID = %d, want 530", ta.TeamID)
	}
	if ta.TeamName != "Atlético de Madrid" {
		t.Errorf("TeamName = %q, want 'Atlético de Madrid'", ta.TeamName)
	}
	if ta.IsNational {
		t.Error("IsNational should default to input (false for a club)")
	}
	if ta.Country == nil || *ta.Country != "Spain" {
		t.Errorf("Country = %v, want Spain", ta.Country)
	}
	// Resolved fields untouched
	if ta.WikidataQID != nil {
		t.Errorf("WikidataQID = %v, want nil", ta.WikidataQID)
	}
	if len(ta.WikidataAliases) != 0 {
		t.Errorf("WikidataAliases = %v, want empty", ta.WikidataAliases)
	}
	if len(ta.TwitterAliases) != 0 {
		t.Errorf("TwitterAliases = %v, want empty", ta.TwitterAliases)
	}
	if ta.LLMModel != nil {
		t.Errorf("LLMModel = %v, want nil", ta.LLMModel)
	}
}

// SetWikidataResolution ------------------------------------------

func TestSetWikidataResolution_PopulatesFields(t *testing.T) {
	ta := makeClub()
	at := time.Date(2026, 7, 8, 12, 15, 0, 0, time.UTC)
	aliases := []string{"Atletico Madrid", "El Atleti", "ATM", "Colchoneros"}

	ta.SetWikidataResolution("Q8701", aliases, at)

	if ta.WikidataQID == nil || *ta.WikidataQID != "Q8701" {
		t.Errorf("WikidataQID = %v, want Q8701", ta.WikidataQID)
	}
	if len(ta.WikidataAliases) != 4 {
		t.Errorf("WikidataAliases len = %d, want 4", len(ta.WikidataAliases))
	}
	if !ta.HasWikidataResolution() {
		t.Error("HasWikidataResolution should be true after SetWikidataResolution")
	}
	if !ta.UpdatedAt.Equal(at) {
		t.Errorf("UpdatedAt = %v, want %v", ta.UpdatedAt, at)
	}
}

func TestSetWikidataResolution_CopiesInputSlice(t *testing.T) {
	ta := makeClub()
	source := []string{"Atletico", "Atleti"}
	ta.SetWikidataResolution("Q8701", source, time.Now())

	// Mutate the source AFTER SetWikidataResolution returns.
	source[0] = "MUTATED"

	if ta.WikidataAliases[0] == "MUTATED" {
		t.Error("SetWikidataResolution stored source slice by reference; internal state can be mutated by caller")
	}
}

// SetTwitterAliases ----------------------------------------------

func TestSetTwitterAliases_PopulatesFields(t *testing.T) {
	ta := makeClub()
	at := time.Date(2026, 7, 8, 12, 20, 0, 0, time.UTC)
	if err := ta.SetTwitterAliases([]string{"Atletico", "Atleti", "ATM"}, "Qwen3-VL-8B", at); err != nil {
		t.Fatalf("SetTwitterAliases: %v", err)
	}
	if len(ta.TwitterAliases) != 3 {
		t.Errorf("TwitterAliases len = %d, want 3", len(ta.TwitterAliases))
	}
	if ta.LLMModel == nil || *ta.LLMModel != "Qwen3-VL-8B" {
		t.Errorf("LLMModel = %v, want Qwen3-VL-8B", ta.LLMModel)
	}
	if !ta.HasTwitterAliases() {
		t.Error("HasTwitterAliases should be true after SetTwitterAliases")
	}
	if !ta.UpdatedAt.Equal(at) {
		t.Errorf("UpdatedAt = %v, want %v", ta.UpdatedAt, at)
	}
}

func TestSetTwitterAliases_RejectsEmptyModel(t *testing.T) {
	ta := makeClub()
	err := ta.SetTwitterAliases([]string{"a", "b"}, "  ", time.Now())
	if err == nil {
		t.Fatal("expected error for empty/whitespace LLMModel, got nil")
	}
	var iae *alias.InvalidArgError
	if !errors.As(err, &iae) {
		t.Errorf("err type = %T, want *InvalidArgError", err)
	}
}

func TestSetTwitterAliases_CopiesInputSlice(t *testing.T) {
	ta := makeClub()
	source := []string{"Atletico", "Atleti"}
	if err := ta.SetTwitterAliases(source, "model-x", time.Now()); err != nil {
		t.Fatalf("SetTwitterAliases: %v", err)
	}
	source[0] = "MUTATED"
	if ta.TwitterAliases[0] == "MUTATED" {
		t.Error("SetTwitterAliases stored source slice by reference")
	}
}

// State helpers --------------------------------------------------

func TestHasResolutionFlags_DefaultFalse(t *testing.T) {
	ta := makeClub()
	if ta.HasWikidataResolution() {
		t.Error("HasWikidataResolution should default false")
	}
	if ta.HasTwitterAliases() {
		t.Error("HasTwitterAliases should default false")
	}
}

func TestHasWikidataResolution_TrueEvenWithEmptyAliases(t *testing.T) {
	// The distinct-states requirement: QID set + empty aliases means
	// "we looked and found nothing" — that's still a resolution.
	ta := makeClub()
	ta.SetWikidataResolution("Q8701", []string{}, time.Now())
	if !ta.HasWikidataResolution() {
		t.Error("resolution with empty aliases must still count as resolved")
	}
}
