// text.go — deterministic word-processing helpers retained from the former
// alias pipeline and used by current player-name query construction:
//
//  1. NFD normalize + strip Unicode combining marks (diacritics)
//  2. ß → ss preprocessing
//  3. Split on whitespace + dashes; strip periods, commas, apostrophes
//  4. Lowercase for filter + output
//  5. Skip tokens ≤ 2 chars, pure-digit tokens, CamelCase concats
//
// Higher-level helpers decide which normalized tokens are useful for a
// specific search surface.
package alias

import (
	"strings"
	"unicode"

	"github.com/gosimple/unidecode"
)

// tokenize breaks a name (team alias or player name) into individual
// lowercased ASCII word tokens. Order preserved (callers usually de-dupe).
//
// Normalization is a single transliteration pass via gosimple/unidecode,
// which maps ANY Unicode — accents, stroke/ligature letters, Cyrillic,
// Greek, CJK, Arabic — to a best-effort ASCII approximation. This
// replaced the former NFD + Mn-strip + hand-rolled extended-Latin table
// (which handled only a subset) AND the old "drop non-Latin scripts"
// rule. Rationale (decisions.md 2026-07-24):
//
//   - Latin diacritics/strokes fold correctly: Ødegaard→odegaard,
//     Guðmundsson→gudmundsson, Fußball→fussball, München→munchen.
//     Twitter search is diacritic/stroke-insensitive, so the ASCII token
//     is optimal — it catches both special-char and plain-keyboard tweets.
//   - Non-Latin scripts are now ROMANIZED, not dropped: Cyrillic
//     Спартак→spartak and Korean 레알→real are real tokens English tweets
//     use. We keep the recall rather than discard it by script.
//   - Romanization noise (Chinese 红魔→"hong mo", Arabic ريال→"ryl mdryd"
//     — phonetic strings no English tweet contains) is NOT filtered here.
//     The tokenizer normalizes; it does not judge. The alias-selection
//     pipeline's ≥2-language threshold voting is the arbiter: a token
//     seen in only one language's label is dropped as noise, one
//     corroborated across languages survives. "romanize + let the
//     threshold decide" keeps the pipeline fully dynamic — no hardcoded
//     roster, no per-script special-casing.
//
// The other filters (≤2-char, all-digit, camelCase-concat) still apply.
// Known follow-up: isCamelConcat also drops Mc/Mac names (McTominay) —
// pre-existing, tracked separately, not a regression from this change.
func tokenize(phrase string) []string {
	if phrase == "" {
		return nil
	}
	// Single transliteration pass: any script → best-effort ASCII.
	ascii := unidecode.Unidecode(phrase)

	// Split on whitespace + dashes; strip trailing/leading punctuation.
	words := splitWords(ascii)
	out := make([]string, 0, len(words))
	for _, w := range words {
		w = stripPunct(w)
		if w == "" {
			continue
		}
		if isCamelConcat(w) {
			continue
		}
		low := strings.ToLower(w)
		if len(low) <= 2 {
			continue
		}
		if isAllDigit(low) {
			continue
		}
		out = append(out, low)
	}
	return out
}

// TokenizePlayerName tokenizes a player's name string and filters
// through the multilingual skip-list. Used by the Discovery-side
// Twitter query builder (per twitter-search-query.md D8) to expand
// a player name like "Kevin De Bruyne" into query tokens `{kevin,
// bruyne}` — with `de` filtered as a Romance-language particle.
//
// Applies both tokenize's normalization (unidecode transliteration to
// ASCII, dash split, ≤2-char drop) AND the skip-list filter (particles
// like `de/van/der/von/la/le/el/il/das/di/da`). Deduplicates while
// preserving first-seen order.
//
// The alias selection pipeline uses tokenize + skip-list too but
// with additional layers (per-language threshold, English rescue,
// venue-city skip) — this helper is the player-name variant that
// stops after the core two steps.
func TokenizePlayerName(name string) []string {
	tokens := tokenize(name)
	if len(tokens) == 0 {
		return nil
	}
	out := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, tok := range tokens {
		if isSkipped(tok) {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out
}

// splitWords splits on whitespace, hyphen, en-dash, em-dash, and
// forward slash. Empty tokens dropped.
func splitWords(s string) []string {
	f := func(r rune) bool {
		return unicode.IsSpace(r) || r == '-' || r == '‐' || r == '–' || r == '—' || r == '/'
	}
	return strings.FieldsFunc(s, f)
}

// stripPunct removes period / comma / apostrophe (both straight and
// typographic) from anywhere in the token — matches Python's rag.py
// `_clean_wikidata_aliases` which does `.replace('.', ”).replace(',', ”)`.
// Handles cases like "F.C." → "FC", "L.F.C." → "LFC", "F. C." → "F  C"
// (whitespace-split downstream cleans "F"/"C" via ≤2 filter).
func stripPunct(w string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '.', ',', '\'', '’':
			return -1
		}
		return r
	}, w)
}

// isCamelConcat detects concatenated forms Wikidata sometimes stores
// as aliases (noise for our token-based match). Two patterns caught:
//
//  1. Lowercase → uppercase transition inside a token: "LiverpoolFC",
//     "FCBarcelona", "AtléticoMadrid". The classic camelCase concat.
//
//  2. Acronym → word transition: THREE or more consecutive uppercase
//     letters followed by a lowercase letter, e.g. "ACFFiorentina"
//     (from Wikidata alias "A.C.F.Fiorentina" after stripPunct
//     collapses the periods). Discovered 2026-07-23 during the O3/d
//     smoke test.
//
//     Threshold is 3+ uppercase not 2+ so we don't wrongly drop
//     legitimate contractions like "L'Olympique" (which becomes
//     "LOlympique" after stripPunct — only 2 uppercase before the
//     lowercase, so it survives). Real acronym-concat cases we've
//     seen (ACFFiorentina, SSCNapoli, HSVHamburg) have ≥3 uppercase
//     letters at the start, matching the length of the org
//     abbreviation.
//
// Any token flagged as concat is dropped. Other Wikidata aliases
// typically carry the individual tokens ("ACF" alone, "Fiorentina"
// alone), so recall isn't affected — the concat form is redundant
// noise, not signal.
func isCamelConcat(w string) bool {
	if len(w) < 4 {
		return false
	}
	for i := 1; i < len(w); i++ {
		prev := rune(w[i-1])
		curr := rune(w[i])
		// Pattern 1: LiverpoolFC — lower → upper.
		if unicode.IsLower(prev) && unicode.IsUpper(curr) {
			return true
		}
		// Pattern 2: ACFFiorentina — need at least THREE uppercase
		// letters BEFORE the lower letter. Requires i >= 3 so we can
		// look at w[i-2] and w[i-3].
		if i >= 3 && unicode.IsUpper(prev) && unicode.IsLower(curr) &&
			unicode.IsUpper(rune(w[i-2])) && unicode.IsUpper(rune(w[i-3])) {
			return true
		}
	}
	return false
}

// isAllDigit reports whether a token is entirely ASCII digits.
// Founding-year tokens like "1886" get dropped this way.
func isAllDigit(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// knownOrgSuffixes are the 2-char club-abbreviation suffixes that
// commonly terminate a concatenated English alias like "PSGFC". Kept
// deliberately narrow — expanding this list means auditing every team
// for new false-positive risks.
var knownOrgSuffixes = []string{"fc", "ac", "sc", "cf"}

// stripKnownOrgSuffix returns (prefix, true) if tok's last 2 chars
// are in knownOrgSuffixes AND the prefix is ≥3 chars. Otherwise
// returns ("", false).
//
// Used by the selection pipeline (select.go extractSources) to surface
// implicit English acronyms hidden inside concatenated Wikidata
// aliases: e.g. Wikidata's English alias for PSG (Q483020) is "PSGFC"
// with no separator, so our tokenizer produces the single token
// "psgfc". Fans use "PSG" as the primary handle — we recover it by
// stripping the known "fc" suffix and adding "psg" as an implicit
// English alias token, which then participates in the normal ≥2-lang
// threshold. If some other language of Wikidata already tokenized to
// "psg" (Italian's "PSG F.C." does), the two-language corroboration
// keeps the token. If no other language has it, ≥2-lang drops it
// (e.g. NYCFC — nyc appears nowhere else in Wikidata's aliases →
// dropped by threshold). Evidence-based rescue.
//
// The 3-char prefix guard specifically protects AVFC / MUFC / NUFC /
// CFC / AFC — all 4-char forms whose 2-char prefix falls below the
// guard and is left untouched.
func stripKnownOrgSuffix(tok string) (string, bool) {
	if len(tok) < 5 {
		return "", false
	}
	lastTwo := tok[len(tok)-2:]
	for _, sfx := range knownOrgSuffixes {
		if lastTwo == sfx {
			prefix := tok[:len(tok)-2]
			if len(prefix) >= 3 {
				return prefix, true
			}
			return "", false
		}
	}
	return "", false
}
