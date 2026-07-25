// select.go — the alias-selection pipeline.
//
// Given a resolved Wikidata QID (from the lookup pipeline in
// lookup.go) + team metadata from API-Football, produce the []string
// alias set that Discovery submits to Twitter's advanced-search
// OR-syntax.
//
// Two branches on IsNational:
//
//   Clubs  → V5-shaped: multilingual aliases (11 Latin-script langs) +
//            P1449 nicknames + canonical + ≥2-lang frequency threshold
//            + venue-city skip when city ∉ canonical name.
//            See select_club.go.
//
//   Nationals → V8-shaped: V5's rules PLUS English P1549 demonyms from
//               the linked country entity (Argentina → Argentine +
//               Argentinian + Argentinean; UK → British + Briton).
//               See select_national.go.
//
// Both branches share `selectionLanguages` (11 Latin-script langs) and
// the multilingual skip-list (skiplist.go). Design ref:
// docs/design/proposals/team-aliases.md § "Phase 2 — Selection".
package alias

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// selectionLanguages is the Latin-script language subset both branches
// extract aliases + labels from. Chosen empirically per the eval —
// dropping non-Latin scripts drops zero recall on English-language
// Twitter search (tweets don't use Cyrillic/Arabic/CJK for team
// names). Ordered alphabetically for readability; no semantic
// significance to order.
var selectionLanguages = []string{
	"ca", "de", "en", "es", "fr", "gl", "it", "nl", "pl", "pt", "ro",
}

// SelectInput is what the selection pipeline needs to run.
//
// QID is the resolved Wikidata entity (usually from Resolver.Resolve).
// CanonicalName comes from API-Football's team.name — treated as
// always-kept and (for clubs) used to check whether venue.city should
// be skipped.
type SelectInput struct {
	QID           string
	IsNational    bool
	CanonicalName string
	VenueCity     *string // clubs only; nil for nationals
}

// Select runs the selection pipeline: GetEntity(QID) → run V5 or V8
// rules → return the alias []string.
//
// Alias set is sorted for stable output (matters for test
// reproducibility + human diff-reading of the pg row).
func (r *Resolver) Select(ctx context.Context, in SelectInput) ([]string, error) {
	if in.QID == "" {
		return nil, fmt.Errorf("alias.Resolver.Select: QID is required")
	}
	ent, err := r.wd.GetEntity(ctx, in.QID)
	if err != nil {
		return nil, fmt.Errorf("alias.Resolver.Select: GetEntity(%s): %w", in.QID, err)
	}

	// Extract sources common to both branches.
	sources := extractSources(ent, in.CanonicalName)

	if in.IsNational {
		return r.selectNational(ctx, ent, sources), nil
	}
	return selectClub(sources, in.VenueCity), nil
}

// selectionSources bundles the raw token evidence per team, extracted
// once and consumed by whichever branch runs.
type selectionSources struct {
	// aliasesByLang[lang] = set of tokens observed in aliases.<lang>.
	// Each language's set is deduped; the frequency count is computed
	// as len(langs containing token).
	aliasesByLang map[string]map[string]struct{}

	// labelTokens = tokens from labels.<lang> across selectionLanguages.
	// Deduped globally (which lang each came from doesn't matter for
	// selection). Contributes to the ≥2-lang threshold via the shared
	// per-token language count.
	labelsByLang map[string]map[string]struct{}

	// nicknameTokens = tokens from P1449 (language-agnostic). Always
	// kept unconditionally — Wikidata explicitly tags these as
	// nicknames.
	nicknameTokens map[string]struct{}

	// canonicalTokens = tokens from the API-Football canonical name
	// (word-split, normalized, skip-listed). Always kept unconditionally.
	canonicalTokens map[string]struct{}
}

// extractSources reads a Wikidata Entity into the source structure the
// selection branches consume. Applies word-processing (tokenize) and
// skip-list filtering to each source; frequency-threshold checks live
// in the branch functions.
func extractSources(ent entityLike, canonicalName string) selectionSources {
	src := selectionSources{
		aliasesByLang:   make(map[string]map[string]struct{}),
		labelsByLang:    make(map[string]map[string]struct{}),
		nicknameTokens:  make(map[string]struct{}),
		canonicalTokens: make(map[string]struct{}),
	}

	langSet := make(map[string]struct{}, len(selectionLanguages))
	for _, l := range selectionLanguages {
		langSet[l] = struct{}{}
	}

	// aliases[lang], with cross-language dedup for MULTI-WORD strings.
	//
	// Wikidata contributors often copy verbatim MULTI-WORD aliases from
	// one language into another (e.g. the German full name "Turn- und
	// Sportverein Bayer 04 Leverkusen" appearing as both an Italian
	// AND a Polish alias for Q104761). Without dedup, the ≥2-lang
	// threshold would count `und`/`turn`/`sportverein` as multi-language
	// corroborated even though they're monolingual German tokens that
	// no Italian or Polish speaker would use in a tweet.
	//
	// SINGLE-word aliases (abbreviations like MUFC, RMCF, PSG) are
	// legitimately shared across languages by international football
	// fans — Wikidata contributors correctly add them to multiple
	// language slots because that's real usage. We MUST NOT dedup
	// those, or we'd filter out `mufc`/`rmcf`/`psg` which are the
	// signal we want.
	//
	// Rule: an alias string is deduped across languages IFF it
	// tokenizes to >1 token. Single-token aliases get full multi-
	// language credit. Multi-word aliases get counted for exactly one
	// language — the first in sorted iteration order (deterministic).
	//
	// Introduced 2026-07-23 after the real-pipeline probe surfaced
	// `und` / `turn` / `sportverein` in Bayer Leverkusen's alias set;
	// see decisions.md.
	langs := make([]string, 0, len(ent.AliasesByLang()))
	for lang := range ent.AliasesByLang() {
		langs = append(langs, lang)
	}
	sort.Strings(langs)

	seenMultiWordAliases := make(map[string]struct{})
	for _, lang := range langs {
		if _, ok := langSet[lang]; !ok {
			continue
		}
		values := ent.AliasesByLang()[lang]
		for _, val := range values {
			toks := tokenize(val)
			if len(toks) > 1 {
				// Multi-word alias: dedup across languages.
				// Case-insensitive key — Wikidata is inconsistent about
				// casing across languages.
				key := strings.ToLower(strings.TrimSpace(val))
				if _, seen := seenMultiWordAliases[key]; seen {
					continue
				}
				seenMultiWordAliases[key] = struct{}{}
			}
			// Single-word aliases: no dedup — abbreviations like MUFC
			// are legitimately shared across languages by international
			// football-fan usage. Full multi-language credit.
			for _, tok := range toks {
				if isSkipped(tok) {
					continue
				}
				if src.aliasesByLang[lang] == nil {
					src.aliasesByLang[lang] = make(map[string]struct{})
				}
				src.aliasesByLang[lang][tok] = struct{}{}
			}
		}
	}

	// Acronym rescue for English aliases stored without a separator.
	// Wikidata's English alias for PSG is "PSGFC" — tokenized as the
	// single token "psgfc" because there's no whitespace or dash to
	// split on. Fans use "PSG" as the primary handle, and Wikidata's
	// Italian alias "PSG F.C." tokenizes to include the standalone
	// "psg".
	//
	// Rescue strategy: augment aliases.en with the implicit prefix
	// ONLY IF that prefix already exists as a token in some other
	// language's aliases/labels. This turns the rule from "invent a
	// token by pattern" into "surface an implicit English token that
	// Wikidata already has elsewhere in some other language but
	// happened to encode without a separator in English."
	//
	// The "must exist elsewhere" gate is load-bearing. Without it the
	// V10 English rescue (see select_club.go) would keep the augmented
	// prefix regardless of the ≥2-lang threshold — meaning NYCFC's
	// `nyc` would be invented from thin air. With the gate, augmented
	// tokens are evidence-based: PSG bridges to Italian (kept); NYCFC
	// bridges to nothing (dropped).
	if en := src.aliasesByLang["en"]; en != nil {
		// Union of tokens across every language OTHER than English.
		otherLangTokens := make(map[string]struct{})
		for lang, toks := range src.aliasesByLang {
			if lang == "en" {
				continue
			}
			for t := range toks {
				otherLangTokens[t] = struct{}{}
			}
		}
		for lang, toks := range src.labelsByLang {
			if lang == "en" {
				continue
			}
			for t := range toks {
				otherLangTokens[t] = struct{}{}
			}
		}

		toAdd := make([]string, 0)
		for tok := range en {
			if prefix, ok := stripKnownOrgSuffix(tok); ok && !isSkipped(prefix) {
				if _, hasElsewhere := otherLangTokens[prefix]; hasElsewhere {
					toAdd = append(toAdd, prefix)
				}
			}
		}
		for _, p := range toAdd {
			en[p] = struct{}{}
		}
	}
	// labels[lang]
	for lang, val := range ent.LabelsByLang() {
		if _, ok := langSet[lang]; !ok {
			continue
		}
		for _, tok := range tokenize(val) {
			if isSkipped(tok) {
				continue
			}
			if src.labelsByLang[lang] == nil {
				src.labelsByLang[lang] = make(map[string]struct{})
			}
			src.labelsByLang[lang][tok] = struct{}{}
		}
	}
	// P1449 nicknames (language-agnostic). Skip-list still applies —
	// removes article prefixes ("The Reds" → just "reds"; "Los Rojos"
	// → just "rojos"). The distinctive nickname WORDS survive; the
	// skip-list contains only generic articles + org terms so this
	// doesn't lose any real nickname content.
	for _, nick := range ent.NicknamesP1449() {
		for _, tok := range tokenize(nick) {
			if isSkipped(tok) {
				continue
			}
			src.nicknameTokens[tok] = struct{}{}
		}
	}
	// Canonical name from API-Football.
	for _, tok := range tokenize(canonicalName) {
		if isSkipped(tok) {
			continue
		}
		src.canonicalTokens[tok] = struct{}{}
	}
	return src
}

// entityLike is the accessor surface extractSources needs from a
// Wikidata Entity. Defined here so tests can substitute a fake.
type entityLike interface {
	AliasesByLang() map[string][]string
	LabelsByLang() map[string]string
	NicknamesP1449() []string
}

// tokenLangCount returns per-token distinct-language count across
// aliases + labels combined. Used by the ≥2-lang threshold check.
func tokenLangCount(src selectionSources) map[string]int {
	counts := make(map[string]int)
	// aliases contribution
	for _, toks := range src.aliasesByLang {
		for tok := range toks {
			counts[tok]++
		}
	}
	// labels contribution — same lang shouldn't count twice
	// (if a lang has BOTH aliases and label containing the same token,
	// count it once for that lang, not two).
	seenByToken := make(map[string]map[string]struct{})
	for lang, aliasToks := range src.aliasesByLang {
		for tok := range aliasToks {
			if seenByToken[tok] == nil {
				seenByToken[tok] = make(map[string]struct{})
			}
			seenByToken[tok][lang] = struct{}{}
		}
	}
	for lang, labelToks := range src.labelsByLang {
		for tok := range labelToks {
			if _, alreadyCounted := seenByToken[tok][lang]; alreadyCounted {
				continue
			}
			counts[tok]++
			if seenByToken[tok] == nil {
				seenByToken[tok] = make(map[string]struct{})
			}
			seenByToken[tok][lang] = struct{}{}
		}
	}
	return counts
}

// mergeAndSort combines multiple token sets into a sorted alias slice.
// Deterministic output — matters for tests + pg row stability.
func mergeAndSort(sets ...map[string]struct{}) []string {
	merged := make(map[string]struct{})
	for _, s := range sets {
		for tok := range s {
			merged[tok] = struct{}{}
		}
	}
	out := make([]string, 0, len(merged))
	for tok := range merged {
		out = append(out, tok)
	}
	sort.Strings(out)
	return out
}

// applyThreshold returns the tokens satisfying the keep rule:
//
//	appears in ≥ threshold distinct languages in aliases+labels
//	  OR is a P1449 nickname
//	  OR is a canonical-name token
//
// Empirically threshold=2 beats 3 on club recall (see decisions.md
// 2026-07-19). Nationals share the same threshold.
func applyThreshold(src selectionSources, threshold int) map[string]struct{} {
	counts := tokenLangCount(src)
	out := make(map[string]struct{})
	for tok, c := range counts {
		if c >= threshold {
			out[tok] = struct{}{}
		}
	}
	// Nicknames + canonical always survive regardless of threshold.
	for tok := range src.nicknameTokens {
		out[tok] = struct{}{}
	}
	for tok := range src.canonicalTokens {
		out[tok] = struct{}{}
	}
	return out
}

// englishAliasTokens returns the set of tokens seen in aliases.en +
// labels.en. Used by the V10 rescue: single-language-English acronyms
// (LFC, CFC, MCFC) that fail the ≥2-lang threshold get restored so
// English-language Twitter search picks them up.
func englishAliasTokens(src selectionSources) map[string]struct{} {
	out := make(map[string]struct{})
	for tok := range src.aliasesByLang["en"] {
		out[tok] = struct{}{}
	}
	for tok := range src.labelsByLang["en"] {
		out[tok] = struct{}{}
	}
	return out
}

// applyVenueCitySkip removes each venue-city-derived token from set
// UNLESS the same token is in canonicalTokens (i.e., the city is
// part of the team name).
//
// Handles the "Arsenal is in London but London isn't Arsenal's alias"
// case (Arsenal canonical = {arsenal}, London city → drops "london").
// Preserves "liverpool" for Liverpool F.C. (canonical = {liverpool},
// Liverpool city → keeps "liverpool" because it's a canonical token).
func applyVenueCitySkip(set, canonicalTokens map[string]struct{}, venueCity *string) {
	if venueCity == nil || *venueCity == "" {
		return
	}
	for _, cityTok := range tokenize(*venueCity) {
		if _, isCanonical := canonicalTokens[cityTok]; isCanonical {
			continue
		}
		delete(set, cityTok)
	}
}
