// text.go — shared word-processing helpers used by both the lookup
// pipeline (description scoring, country variations) and the (soon-
// to-land) selection pipeline. Same rules as the eval script:
//
//   1. NFD normalize + strip Unicode combining marks (diacritics)
//   2. ß → ss preprocessing
//   3. Split on whitespace + dashes; strip periods, commas, apostrophes
//   4. Lowercase for filter + output
//   5. Skip tokens ≤ 2 chars, pure-digit tokens, CamelCase concats
//
// The selection pipeline (#134) will layer a multilingual skip-list on
// top of tokenize's output. Lookup does NOT apply the skip-list —
// scoring wants raw tokens because "argentine" (a demonym) is exactly
// what we want to match on.
package alias

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// tokenize breaks a Wikidata alias string into individual words with
// full normalization applied. Returns lowercased, diacritic-stripped
// tokens. Order preserved (callers usually de-dupe).
func tokenize(phrase string) []string {
	if phrase == "" {
		return nil
	}
	// ß → ss so "fußball" survives NFD as "fussball" (matches an
	// eventual skip-list; keeps German content indexable).
	phrase = strings.ReplaceAll(phrase, "ß", "ss")

	// NFD + strip Mn (combining marks) — same as public Normalize but
	// we want lowercase for filter purposes.
	decomposed := norm.NFD.String(phrase)
	var stripped strings.Builder
	stripped.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		stripped.WriteRune(r)
	}

	// Split on whitespace + dashes; strip trailing/leading punctuation.
	words := splitWords(stripped.String())
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
// `_clean_wikidata_aliases` which does `.replace('.', '').replace(',', '')`.
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

// isCamelConcat detects lowercase → uppercase transitions inside a
// single un-split token: "LiverpoolFC", "FCBarcelona", "AtléticoMadrid".
// Wikidata sometimes stores concatenated forms as aliases; those are
// noise for our token-based match.
func isCamelConcat(w string) bool {
	if len(w) < 4 {
		return false
	}
	for i := 1; i < len(w); i++ {
		prev := rune(w[i-1])
		curr := rune(w[i])
		if unicode.IsLower(prev) && unicode.IsUpper(curr) {
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

// lowerASCII returns a lowercased, diacritic-stripped version of s.
// Used as a map key normalizer (e.g., country-name cache keys).
func lowerASCII(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "ß", "ss")
	decomposed := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(strings.TrimSpace(b.String()))
}

// sortedKeys returns the keys of a set in stable sorted order. Used
// by CountryVariations.fetchVariations for deterministic output that
// makes test assertions easy.
func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
