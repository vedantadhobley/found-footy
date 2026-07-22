// Tests for the Twitter search query builder. Covers D1 shape,
// D4b player-name required, D4c canonical-name fallback, D4d empty-
// query safeguard, D7 sentiment-mode toggle, D8 player-name
// tokenization table + skip-list particles. Fixed-input assertions
// on the FULL query string so any drift shows up loudly.
package discovery

import (
	"errors"
	"strings"
	"testing"
)

// TestBuild_HappyPath — canonical example from twitter-search-query.md:
// Salah goal for Liverpool. Verifies the OR structure + filter:videos
// suffix + trusted alias set + player token.
func TestBuild_HappyPath(t *testing.T) {
	got, err := Build(
		"Mohamed Salah",
		"Liverpool",
		[]string{"liverpool", "reds", "lfc", "scousers"},
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Order: player tokens first, then team aliases, deduped.
	want := "(mohamed OR salah OR liverpool OR reds OR lfc OR scousers) filter:videos"
	if got != want {
		t.Errorf("query = %q\n want = %q", got, want)
	}
}

// TestBuild_DeBruyne_ParticleDropped — D8 table: `Kevin De Bruyne`
// should produce `{kevin, bruyne}` — the `de` particle drops via the
// multilingual skip-list.
func TestBuild_DeBruyne_ParticleDropped(t *testing.T) {
	got, err := Build(
		"Kevin De Bruyne",
		"Manchester City",
		[]string{"city", "citizens", "mcfc", "blues"},
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(got, "OR de OR") || strings.HasPrefix(got, "(de OR") {
		t.Errorf("query should not contain `de` particle; got %q", got)
	}
	// Positive checks — kevin + bruyne must survive.
	for _, needle := range []string{"kevin", "bruyne", "city", "mcfc"} {
		if !strings.Contains(got, needle) {
			t.Errorf("query missing %q: %s", needle, got)
		}
	}
}

// TestBuild_InitialStripped — D8 table: `M. Salah` (initial + period)
// should produce `{salah}`. The M initial fails ≤2-char filter, period
// gets stripped by punctuation rule.
func TestBuild_InitialStripped(t *testing.T) {
	got, err := Build("M. Salah", "Liverpool", []string{"liverpool"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Should look like: (salah OR liverpool) filter:videos
	// M should NOT appear anywhere.
	if strings.Contains(got, "OR m OR") || strings.Contains(got, "(m ") {
		t.Errorf("initial `m` should be dropped; got %q", got)
	}
	if !strings.Contains(got, "salah") {
		t.Errorf("salah missing from %q", got)
	}
}

// TestBuild_DoubleInitials_AllDropped — D8: `K. De Bruyne` → `{bruyne}`.
// Both K (≤2) and `de` (skip-list) drop, leaving just bruyne + team.
func TestBuild_DoubleInitials_AllDropped(t *testing.T) {
	got, err := Build("K. De Bruyne", "Man City", []string{"city"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Only "bruyne" should be the player slot.
	// Man → 3 chars, survives ≤2 filter, so canonical tokenizes to
	// {man, city}. But we pass team aliases explicitly here, so
	// canonical doesn't come into play.
	if strings.Contains(got, "OR k OR") || strings.Contains(got, "(k ") {
		t.Errorf("initial `k` should be dropped; got %q", got)
	}
	if strings.Contains(got, "OR de OR") || strings.Contains(got, "(de ") {
		t.Errorf("particle `de` should be dropped; got %q", got)
	}
	if !strings.Contains(got, "bruyne") {
		t.Errorf("bruyne missing from %q", got)
	}
}

// TestBuild_HyphenatedSurname — D8: `Trent Alexander-Arnold` splits
// on dash into three tokens {trent, alexander, arnold}.
func TestBuild_HyphenatedSurname(t *testing.T) {
	got, err := Build("Trent Alexander-Arnold", "Liverpool", []string{"reds"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, needle := range []string{"trent", "alexander", "arnold"} {
		if !strings.Contains(got, needle) {
			t.Errorf("hyphenated name missing token %q: %s", needle, got)
		}
	}
}

// TestBuild_DiacriticStripped — D8: `Nathan Aké` → `{nathan, ake}`
// via NFD normalize (é → e).
func TestBuild_DiacriticStripped(t *testing.T) {
	got, err := Build("Nathan Aké", "Manchester City", []string{"city"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.ContainsRune(got, 'é') {
		t.Errorf("diacritic é should be stripped; got %q", got)
	}
	if !strings.Contains(got, "ake") {
		t.Errorf("`ake` missing (should survive NFD strip); got %q", got)
	}
}

// TestBuild_SingleNamePlayer — D8: `Ronaldinho` → single token,
// works fine.
func TestBuild_SingleNamePlayer(t *testing.T) {
	got, err := Build("Ronaldinho", "Barcelona", []string{"barca", "fcb"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(got, "ronaldinho") {
		t.Errorf("single-name player missing; got %q", got)
	}
}

// TestBuild_VanParticleDropped — D8: `Robin Van Persie` → `{robin, persie}`.
// `van` in skip-list.
func TestBuild_VanParticleDropped(t *testing.T) {
	got, err := Build("Robin Van Persie", "Netherlands", []string{"oranje"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(got, "OR van OR") || strings.Contains(got, "(van ") {
		t.Errorf("particle `van` should be dropped; got %q", got)
	}
	for _, needle := range []string{"robin", "persie"} {
		if !strings.Contains(got, needle) {
			t.Errorf("query missing %q: %s", needle, got)
		}
	}
}

// TestBuild_CanonicalFallback_AliasesEmpty — D4c: when TeamAliases is
// empty, canonical name tokenization fills the team slot.
func TestBuild_CanonicalFallback_AliasesEmpty(t *testing.T) {
	got, err := Build("M. Salah", "OGC Nice", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// nice tokenizes as {nice} — "ogc" fails as 3-char but survives
	// tokenizer (>2 chars); actually 3 chars > 2 so survives.
	// Verify nice is present as the team-side entry.
	if !strings.Contains(got, "nice") {
		t.Errorf("canonical fallback should surface `nice` from `OGC Nice`; got %q", got)
	}
}

// TestBuild_CanonicalMergesWithAliases_BayerLeverkusen — canonical
// name is ALWAYS unioned with aliases (not just fallback). Guards
// against the alias pipeline's ≥2-lang threshold dropping obvious
// canonical tokens. Hypothetical: aliases contain only `werkself`
// (the German nickname, ≥2-lang corroborated); `bayer` and
// `leverkusen` didn't make the multi-lang cut. Query builder must
// still include them via the canonical-name tokenization path.
func TestBuild_CanonicalMergesWithAliases_BayerLeverkusen(t *testing.T) {
	got, err := Build(
		"Florian Wirtz",
		"Bayer Leverkusen",
		[]string{"werkself"}, // pretend the alias pipeline only surfaced this
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// All three team tokens must appear: bayer + leverkusen from
	// canonical, werkself from the alias pipeline.
	for _, needle := range []string{"bayer", "leverkusen", "werkself"} {
		if !strings.Contains(got, needle) {
			t.Errorf("query missing team token %q (canonical union should include it): %s",
				needle, got)
		}
	}
}

// TestBuild_CanonicalDedupedAgainstAliases — the common case: aliases
// already contain the canonical-name tokens. Merge should collapse
// the duplicates; each token appears exactly once.
func TestBuild_CanonicalDedupedAgainstAliases(t *testing.T) {
	got, err := Build(
		"Salah",
		"Liverpool", // canonical tokenizes to {liverpool}
		[]string{"liverpool", "reds", "lfc"}, // liverpool overlaps
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// `liverpool` must appear exactly once despite being in both
	// canonical AND aliases.
	if count := strings.Count(got, "liverpool"); count != 1 {
		t.Errorf("`liverpool` should appear exactly once (dedup), got %d in %q",
			count, got)
	}
}

// TestBuild_PlayerNameRequired — D4b: empty PlayerName returns
// ErrEmptyPlayerName. Upstream (debounce) is supposed to prevent this;
// query builder enforces as a safety net.
func TestBuild_PlayerNameRequired(t *testing.T) {
	_, err := Build("", "Liverpool", []string{"reds"})
	if !errors.Is(err, ErrEmptyPlayerName) {
		t.Fatalf("expected ErrEmptyPlayerName, got %v", err)
	}
	// Whitespace-only also caught.
	_, err = Build("   ", "Liverpool", []string{"reds"})
	if !errors.Is(err, ErrEmptyPlayerName) {
		t.Errorf("whitespace-only player should return ErrEmptyPlayerName, got %v", err)
	}
}

// TestBuild_EmptyQuery_Safeguard — D4d: player name tokenizes to
// nothing (all particles / too short) AND team aliases empty AND
// canonical name empty. Returns ErrEmptyQuery so caller skips the
// Twitter call rather than firing a bare `filter:videos`.
func TestBuild_EmptyQuery_Safeguard(t *testing.T) {
	// "de la" → both particles, both drop. No team info.
	_, err := BuildTwitterQuery(QueryInput{
		PlayerName:        "de la",
		TeamCanonicalName: "",
		TeamAliases:       nil,
		VideoOnly:         true,
	})
	if !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("expected ErrEmptyQuery, got %v", err)
	}
}

// TestBuild_VideoOnlyToggle — D7: VideoOnly=false omits the
// `filter:videos` suffix. Confirms the toggle works both ways.
func TestBuild_VideoOnlyToggle(t *testing.T) {
	withVideo, err := BuildTwitterQuery(QueryInput{
		PlayerName: "Salah", TeamCanonicalName: "Liverpool",
		TeamAliases: []string{"reds"}, VideoOnly: true,
	})
	if err != nil {
		t.Fatalf("with video: %v", err)
	}
	if !strings.Contains(withVideo, "filter:videos") {
		t.Errorf("VideoOnly=true must include filter:videos; got %q", withVideo)
	}

	withoutVideo, err := BuildTwitterQuery(QueryInput{
		PlayerName: "Salah", TeamCanonicalName: "Liverpool",
		TeamAliases: []string{"reds"}, VideoOnly: false,
	})
	if err != nil {
		t.Fatalf("without video: %v", err)
	}
	if strings.Contains(withoutVideo, "filter:videos") {
		t.Errorf("VideoOnly=false must NOT include filter:videos; got %q", withoutVideo)
	}
}

// TestBuild_DeduplicatesPlayerAndTeam — Salah with `salah` in team
// aliases (contrived — but tests dedup). Should not repeat `salah`.
func TestBuild_DeduplicatesPlayerAndTeam(t *testing.T) {
	got, err := Build("Salah", "Egypt", []string{"salah", "pharaohs"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	count := strings.Count(got, "salah")
	if count != 1 {
		t.Errorf("`salah` should appear exactly once (dedup), got %d in %q", count, got)
	}
}

// TestBuild_PreservesOrder_PlayerFirst — player tokens come before
// team aliases in output. Helps debug readability + observability
// consistency.
func TestBuild_PreservesOrder_PlayerFirst(t *testing.T) {
	got, err := Build("Salah", "Liverpool", []string{"reds"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	salahIdx := strings.Index(got, "salah")
	redsIdx := strings.Index(got, "reds")
	if salahIdx == -1 || redsIdx == -1 {
		t.Fatalf("both tokens should be present: %q", got)
	}
	if salahIdx > redsIdx {
		t.Errorf("player token should precede team alias; got %q (salah@%d, reds@%d)",
			got, salahIdx, redsIdx)
	}
}

// TestBuild_LengthUnderWarnThreshold — a maximally-aliased event
// (Barcelona 14+ aliases + long player name) should stay under the
// 400-char warn threshold. Sanity check that real cases don't warn.
func TestBuild_LengthUnderWarnThreshold(t *testing.T) {
	// Simulated max case — Barcelona has ~14 aliases in the real
	// alias pipeline output.
	got, err := Build(
		"Lamine Yamal",
		"Barcelona",
		[]string{"barca", "barcelona", "azulgrana", "blaugrana",
			"culers", "fcb", "cules", "catalunya", "cat", "blaugranes",
			"catalans", "catalanes", "azulgranas", "catalunyistes"},
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if LengthWarn(got) {
		t.Errorf("realistic query should NOT warn; len=%d, query=%q",
			len(got), got)
	}
}

// TestBuild_LengthWarnsWhenReallyLong — hand-crafted runaway case
// should trigger the warn threshold. Validates the LengthWarn helper.
func TestBuild_LengthWarnsWhenReallyLong(t *testing.T) {
	longAliases := make([]string, 40)
	for i := range longAliases {
		longAliases[i] = "aliasnumber" + string(rune('a'+i))
	}
	got, err := Build("Player Name", "TeamName", longAliases)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !LengthWarn(got) {
		t.Errorf("40-alias query should trigger LengthWarn; len=%d", len(got))
	}
}

// TestBuild_OwnGoal_TeamAndPlayerAsIs — per decisions.md 2026-07-22
// own-goal handling: api-football reports beneficiary team + defender
// as player. Query builder takes both at face value — no special-case
// logic. Verify with a synthetic own-goal scenario.
func TestBuild_OwnGoal_TeamAndPlayerAsIs(t *testing.T) {
	// Scenario: Spain defender Rodri own-goals; Argentina benefits.
	// api-football would report team=Argentina, player=Rodri.
	// Query should surface Rodri's name AND Argentina's aliases —
	// tweets from both the celebrating Argentine fans + tweets naming
	// the Spanish defender match the query.
	got, err := Build(
		"Rodri",
		"Argentina",
		[]string{"argentina", "albiceleste", "afa"},
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, needle := range []string{"rodri", "argentina", "albiceleste"} {
		if !strings.Contains(got, needle) {
			t.Errorf("own-goal query missing %q: %s", needle, got)
		}
	}
}

// TestBuild_QuerysExactShape_FernandesTottenham — reproduces the
// 2026-07-22 empirical validation query (Tottenham M. Fernandes goal
// end-to-end test). Bit-exact assertion so any future refactor that
// silently changes the query shape fails loudly.
func TestBuild_QuerysExactShape_FernandesTottenham(t *testing.T) {
	got, err := Build(
		"M. Fernandes",
		"Tottenham",
		[]string{"tottenham", "spurs", "thfc"},
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// M drops (≤2), Fernandes survives. team aliases as-is.
	want := "(fernandes OR tottenham OR spurs OR thfc) filter:videos"
	if got != want {
		t.Errorf("query = %q\n want = %q", got, want)
	}
}
