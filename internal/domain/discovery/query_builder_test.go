// Tests for the Twitter search query builder — the 2026-08-15 distinctive-terms
// rework (surname only, quoted canonical name, derived abbreviation, bare
// generics dropped). Exact-shape assertions on deterministic cases so any drift
// in the query shape fails loudly, plus behavior checks for the pieces.
package discovery

import (
	"errors"
	"strings"
	"testing"
)

// TestBuild_SurnameOnly — the player slot is the SURNAME, not the full name.
// "Mohamed Salah" → salah (mohamed dropped). Canonical single word stays bare;
// aliases that duplicate it (liverpool) drop.
func TestBuild_SurnameOnly(t *testing.T) {
	got, err := Build("Mohamed Salah", "Liverpool", []string{"liverpool", "reds", "lfc"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "(salah OR Liverpool OR reds OR lfc) filter:videos"
	if got != want {
		t.Errorf("query = %q\n want = %q", got, want)
	}
	if strings.Contains(got, "mohamed") {
		t.Errorf("first name should be dropped (surname only); got %q", got)
	}
}

// TestBuild_CanonicalQuoted_GenericsDropped — the crux of the rework. Canonical
// "Manchester City" is emitted as a QUOTED phrase; the bare generics `city` +
// `blues` are dropped; the distinctive `citizens` + `mcfc` survive. The
// 2-letter derived abbrev "MC" is too short → not emitted.
func TestBuild_CanonicalQuoted_GenericsDropped(t *testing.T) {
	got, err := Build("Kevin De Bruyne", "Manchester City", []string{"city", "citizens", "mcfc", "blues"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := `(bruyne OR "Manchester City" OR citizens OR mcfc) filter:videos`
	if got != want {
		t.Errorf("query = %q\n want = %q", got, want)
	}
	if strings.Contains(got, "OR city") || strings.Contains(got, "OR blues") {
		t.Errorf("bare generic tokens should be dropped; got %q", got)
	}
	if strings.Contains(got, "kevin") || strings.Contains(got, " de ") {
		t.Errorf("first name + particle should be gone; got %q", got)
	}
}

// TestBuild_DerivedAbbrev_TorontoDorsch — the live failure case. Toronto FC's
// aliases were poisoned (york/inter/united = generics + York United), but the
// derived abbreviation "TFC" + the quoted canonical carry the team. Generics
// and the canonical-duplicate `toronto` drop; the wrong-team-but-rare `y9fc`/
// `york9` survive (harmless; a resolver fix removes them).
func TestBuild_DerivedAbbrev_TorontoDorsch(t *testing.T) {
	got, err := Build("N. Dorsch", "Toronto FC",
		[]string{"inter", "toronto", "united", "y9fc", "york", "york9"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := `(dorsch OR "Toronto FC" OR TFC OR y9fc OR york9) filter:videos`
	if got != want {
		t.Errorf("query = %q\n want = %q", got, want)
	}
	for _, banned := range []string{"OR inter", "OR united", "OR york ", "OR toronto"} {
		if strings.Contains(got, banned) {
			t.Errorf("poisoned bare token %q leaked into query: %q", banned, got)
		}
	}
}

// TestBuild_DerivedAbbrev_SuffixForms — the initials+club-suffix heuristic.
// "Orlando City SC" → OCSC; short 2-letter results are suppressed.
func TestBuild_DerivedAbbrev_SuffixForms(t *testing.T) {
	if a := deriveAbbrev("Orlando City SC"); a != "OCSC" {
		t.Errorf("deriveAbbrev(Orlando City SC) = %q, want OCSC", a)
	}
	if a := deriveAbbrev("New York City FC"); a != "NYCFC" {
		t.Errorf("deriveAbbrev(New York City FC) = %q, want NYCFC", a)
	}
	if a := deriveAbbrev("Manchester City"); a != "" {
		t.Errorf("deriveAbbrev(Manchester City) = %q, want empty (2-char MC suppressed)", a)
	}
	if a := deriveAbbrev("Liverpool"); a != "" {
		t.Errorf("deriveAbbrev(Liverpool) = %q, want empty (1-char)", a)
	}
}

// TestBuild_DerivedAbbrev_LAFC — the article-skip bug fix (decisions.md
// 2026-08-16). "Los Angeles FC" must derive LAFC (initials of ALL words +
// suffix), not "AFC" — which the old player-name tokenizer produced by skipping
// "Los" as an article, colliding with AFC Ajax. With aliases disconnected the
// derived abbrev is what carries the fan shorthand.
func TestBuild_DerivedAbbrev_LAFC(t *testing.T) {
	if a := deriveAbbrev("Los Angeles FC"); a != "LAFC" {
		t.Errorf("deriveAbbrev(Los Angeles FC) = %q, want LAFC", a)
	}
	got, err := Build("D. Bouanga", "Los Angeles FC", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := `(bouanga OR "Los Angeles FC" OR LAFC) filter:videos`
	if got != want {
		t.Errorf("query = %q\n want = %q", got, want)
	}
}

// TestBuild_GenerationalSuffixStripped — "Vinícius Júnior" → surname vinicius,
// never junior (which matches every player named Junior). The mononym guard
// keeps a player literally named "Neto" from stripping to an empty surname.
func TestBuild_GenerationalSuffixStripped(t *testing.T) {
	got, err := Build("Vinícius Júnior", "Real Madrid", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.HasPrefix(got, "(vinicius OR") {
		t.Errorf("surname should lead with vinicius (junior stripped); got %q", got)
	}
	if strings.Contains(got, "junior") {
		t.Errorf("generational suffix should be gone; got %q", got)
	}
	// Mononym guard: "Neto" is the whole name — must survive, not strip to empty.
	n, err := Build("Neto", "Bournemouth", nil)
	if err != nil {
		t.Fatalf("Build(Neto): %v", err)
	}
	if !strings.HasPrefix(n, "(neto OR") {
		t.Errorf("mononym Neto must not be stripped; got %q", n)
	}
}

// TestBuild_ParticleAndInitial_Dropped — surname extraction still rides the
// tokenizer's skip-list: "Robin Van Persie" → persie (van dropped, robin is
// not the surname), "M. Salah" → salah.
func TestBuild_ParticleAndInitial_Dropped(t *testing.T) {
	got, err := Build("Robin Van Persie", "Netherlands", []string{"oranje"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.HasPrefix(got, "(persie OR") {
		t.Errorf("surname should be `persie`; got %q", got)
	}
	if strings.Contains(got, "van") || strings.Contains(got, "robin") {
		t.Errorf("particle + first name should be gone; got %q", got)
	}

	got2, _ := Build("M. Salah", "Liverpool", nil)
	if !strings.HasPrefix(got2, "(salah OR") {
		t.Errorf("initial should drop, surname `salah` lead; got %q", got2)
	}
}

// TestBuild_CanonicalOnly_NoAliases — with no aliases, the quoted canonical
// name (+ any derived abbrev) carries the team slot.
func TestBuild_CanonicalOnly_NoAliases(t *testing.T) {
	got, err := Build("Wirtz", "Bayer Leverkusen", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// "Bayer Leverkusen" → initials "BL" = 2 chars → abbrev suppressed;
	// expect just surname + quoted canonical.
	want := `(wirtz OR "Bayer Leverkusen") filter:videos`
	if got != want {
		t.Errorf("query = %q\n want = %q", got, want)
	}
}

// TestBuild_PlayerNameRequired — empty PlayerName returns ErrEmptyPlayerName.
func TestBuild_PlayerNameRequired(t *testing.T) {
	if _, err := Build("", "Liverpool", []string{"reds"}); !errors.Is(err, ErrEmptyPlayerName) {
		t.Fatalf("expected ErrEmptyPlayerName, got %v", err)
	}
	if _, err := Build("   ", "Liverpool", nil); !errors.Is(err, ErrEmptyPlayerName) {
		t.Errorf("whitespace-only player should return ErrEmptyPlayerName, got %v", err)
	}
}

// TestBuild_EmptyQuery_Safeguard — player tokenizes to nothing AND no team info
// → ErrEmptyQuery so the caller skips the Twitter call.
func TestBuild_EmptyQuery_Safeguard(t *testing.T) {
	_, err := BuildTwitterQuery(QueryInput{PlayerName: "de la", TeamCanonicalName: "", TeamAliases: nil, VideoOnly: true})
	if !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("expected ErrEmptyQuery, got %v", err)
	}
}

// TestBuild_VideoOnlyToggle — VideoOnly=false omits filter:videos.
func TestBuild_VideoOnlyToggle(t *testing.T) {
	with, _ := BuildTwitterQuery(QueryInput{PlayerName: "Salah", TeamCanonicalName: "Liverpool", VideoOnly: true})
	if !strings.Contains(with, "filter:videos") {
		t.Errorf("VideoOnly=true must include filter:videos; got %q", with)
	}
	without, _ := BuildTwitterQuery(QueryInput{PlayerName: "Salah", TeamCanonicalName: "Liverpool", VideoOnly: false})
	if strings.Contains(without, "filter:videos") {
		t.Errorf("VideoOnly=false must NOT include filter:videos; got %q", without)
	}
}

// TestBuild_LengthWarn — realistic query stays under threshold; a runaway
// one trips it.
func TestBuild_LengthWarn(t *testing.T) {
	got, _ := Build("Lamine Yamal", "Barcelona",
		[]string{"barca", "azulgrana", "blaugrana", "culers", "cules", "catalans"})
	if LengthWarn(got) {
		t.Errorf("realistic query should not warn; len=%d, %q", len(got), got)
	}
	long := make([]string, 40)
	for i := range long {
		long[i] = "distinctivealias" + string(rune('a'+i))
	}
	if g, _ := Build("Player Name", "Team FC", long); !LengthWarn(g) {
		t.Errorf("40-alias query should warn; len=%d", len(g))
	}
}
