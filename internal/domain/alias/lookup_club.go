// lookup_club.go — club branch of the alias-lookup pipeline.
//
// Ports Python's rag.py `_search_wikidata_qid` for clubs. 9 fuzzy
// wbsearchentities variants + description-quality scoring produces
// the best-matching QID. LLM country-variations replaced by
// Wikidata-derived P1549 + P1448 tokens per country.
package alias

import (
	"context"
	"strings"

	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
)

// clubDescriptionSkipKeywords is the set of description substrings
// that immediately disqualify a candidate. Same rules Python used —
// filters women's teams, reserve squads, youth teams, and other non-
// senior-men football entities that share names with senior clubs.
var clubDescriptionSkipKeywords = []string{
	"women", "femen", "reserve", "youth", "junior",
	"u-19", "u-20", "u-21", "under-19", "under-20", "under-21",
	"academy", "futsal", "beach", "basketball",
	"stadium", "arena", "list article", "disambiguation",
}

// clubLabelSkipSuffixes catches B/C/II/III second-team labels
// (Real Madrid Castilla is Real Madrid B, etc.).
var clubLabelSkipSuffixes = []string{" B", " C", " II", " III"}

// Scoring constants — ported from Python. Absolute values don't
// matter (only relative ordering); reused so the club branch's
// output is comparable to Python's audit logs.
const (
	scoreCityMatchShortCircuit = 200
	scoreCountryMatch          = 100
	scoreLocationInPhrase      = 50
)

// resolveClub is the club-branch entry point. Runs the 9-variant
// fuzzy search + scoring + returns the winning QID.
func (r *Resolver) resolveClub(ctx context.Context, name string, country, city *string) (LookupResult, error) {
	variants := buildClubSearchVariants(name, country, city)

	// Precompute normalized country variations + city tokens for scoring.
	var countryVariations []string
	if country != nil && *country != "" {
		countryVariations = r.varCache.For(ctx, *country)
	}
	var cityLower string
	if city != nil && *city != "" {
		cityLower = lowerASCII(*city)
	}

	// Scan all variants, keep the highest-scoring candidate seen. A
	// perfect city match short-circuits (return immediately) as in
	// Python — extra variant searches would be wasted work.
	var (
		bestHit   wikidata.SearchHit
		bestScore int
		haveBest  bool
		seen      = make(map[string]struct{}) // dedupe by QID across variants
	)
	for _, variant := range variants {
		hits, err := r.wd.SearchEntities(ctx, variant, defaultSearchOpts())
		if err != nil {
			// Transport error on one variant — try the next. Only
			// surface as ErrNoMatch if every variant fails to yield a
			// scored candidate.
			continue
		}
		for _, h := range hits {
			if _, dupe := seen[h.ID]; dupe {
				continue
			}
			seen[h.ID] = struct{}{}

			if !clubCandidatePassesFilter(h) {
				continue
			}

			score := scoreClubCandidate(h, cityLower, countryVariations)
			if score >= scoreCityMatchShortCircuit {
				// Perfect city match — return immediately.
				return LookupResult{
					QID:         h.ID,
					Label:       h.Label,
					Description: h.Description,
					Score:       score,
				}, nil
			}
			if !haveBest || score > bestScore {
				bestHit = h
				bestScore = score
				haveBest = true
			}
		}
	}

	if !haveBest {
		return LookupResult{}, ErrNoMatch
	}
	return LookupResult{
		QID:         bestHit.ID,
		Label:       bestHit.Label,
		Description: bestHit.Description,
		Score:       bestScore,
	}, nil
}

// buildClubSearchVariants returns the ordered search-term list.
// Order mirrors Python for parity with prod behavior — the first
// hit through the scoring loop wins ties, and Python's order was
// tuned over months.
func buildClubSearchVariants(name string, country, city *string) []string {
	n := strings.TrimSpace(name)
	variants := []string{
		n + " FC",
		n + " football club",
		"FC " + n,
	}
	if city != nil && *city != "" {
		variants = append(variants,
			n+" "+*city,
			n+" FC "+*city,
		)
	}
	if country != nil && *country != "" {
		variants = append(variants,
			n+" FC "+*country,
			n+" "+*country+" football",
		)
	}
	// Common English-language suffix hints.
	variants = append(variants, n+" United", n+" City")
	// Bare name is the LAST resort — Python does this so more-specific
	// variants get first crack at scoring.
	variants = append(variants, n)
	return variants
}

// clubCandidatePassesFilter rejects candidates that are clearly not
// what we want (women's teams, reserve squads, youth teams, etc.).
// Description-based filter matches Python's rag.py logic.
func clubCandidatePassesFilter(h wikidata.SearchHit) bool {
	desc := strings.ToLower(h.Description)
	label := h.Label
	labelLower := strings.ToLower(label)

	// Must be football-adjacent. Multisport clubs qualify (e.g. FC
	// Barcelona is a multisport club whose football section IS what
	// we want) — same lenient rule as Python.
	isFootball := strings.Contains(desc, "football") ||
		strings.Contains(desc, "soccer") ||
		strings.Contains(desc, "fútbol") ||
		strings.Contains(desc, "futbol")
	isMultisport := strings.Contains(desc, "multisport") ||
		strings.Contains(desc, "sports club")
	if !(isFootball || isMultisport) {
		return false
	}

	// Skip descriptions containing disqualifying keywords.
	for _, kw := range clubDescriptionSkipKeywords {
		if strings.Contains(desc, kw) || strings.Contains(labelLower, kw) {
			return false
		}
	}
	// Skip B/C/II/III second-team label suffixes.
	for _, suffix := range clubLabelSkipSuffixes {
		if strings.HasSuffix(label, suffix) {
			return false
		}
	}
	return true
}

// scoreClubCandidate computes the composite score for a candidate.
// Matches Python's scoring formula for compat with prod audit logs.
//
// Base score is len(description) — a proxy for "how detailed is
// Wikidata's description of this entity" (detailed descriptions
// come from well-maintained entities, usually the senior team).
// City match adds a huge bonus (usually decisive). Country match
// adds a smaller bonus. " in " suggests locational phrasing.
func scoreClubCandidate(h wikidata.SearchHit, cityLower string, countryVariations []string) int {
	desc := strings.ToLower(h.Description)
	score := len(desc)

	if cityLower != "" && descContainsCity(desc, cityLower) {
		score += scoreCityMatchShortCircuit
	}
	for _, v := range countryVariations {
		if v == "" {
			continue
		}
		if strings.Contains(desc, v) {
			score += scoreCountryMatch
			break // don't double-count when a country has 10 variations
		}
	}
	if strings.Contains(desc, " in ") {
		score += scoreLocationInPhrase
	}
	return score
}

// descContainsCity matches the city name against the description with
// a spelling-variation forgiveness (first 5 chars) — same trick
// Python uses for Sevilla/Seville, München/Munich.
func descContainsCity(desc, cityLower string) bool {
	if strings.Contains(desc, cityLower) {
		return true
	}
	if len(cityLower) >= 5 && strings.Contains(desc, cityLower[:5]) {
		return true
	}
	return false
}

// defaultSearchOpts is what both branches use for SearchEntities:
// English language, 10 hits per variant (enough headroom to survive
// filtering without triggering Wikidata's max-50 cap).
func defaultSearchOpts() wikidata.SearchOpts {
	return wikidata.SearchOpts{Language: "en", Limit: 10}
}
