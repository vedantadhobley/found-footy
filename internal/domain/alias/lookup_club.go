// lookup_club.go — club branch of the alias-lookup pipeline.
//
// Ports Python's rag.py `_search_wikidata_qid` for clubs. 9 fuzzy
// wbsearchentities variants collect candidates; a single SPARQL P31
// batch query type-checks them against Wikidata's own ontology
// (accept-set: association football club + men's association football
// team); survivors go through description-quality scoring against
// per-country variations. LLM country-variations replaced by
// Wikidata-derived P1549 + P1448 tokens per country.
package alias

import (
	"context"
	"strings"

	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
)

// P31 accept-set for club candidates. Verified against a sweep of 15
// well-known clubs (2026-07-20): 14 have Q476028 directly; FC Barcelona
// (Q7156, multisport) has Q103229495 + Q10651067 + Q20639856 — the
// men's-team subtype is the only shared discriminator, so we include
// it. Adding more accept types is cheap (VALUES on the SPARQL side)
// but each addition invites false positives — resist unless we see
// real-world misses.
var clubAcceptP31 = map[string]struct{}{
	"Q476028":    {}, // association football club
	"Q103229495": {}, // men's association football team
}

// P31 reject-set for club candidates. A candidate that matches ANY
// reject type is dropped even if it also matches an accept type. This
// catches subclasses of "football club" that we specifically don't
// want (reserves, women's teams) — e.g. Milan Futuro (Q126923253) has
// both Q476028 AND Q2412834, and we want it out.
var clubRejectP31 = map[string]struct{}{
	"Q2412834":  {}, // reserve team
	"Q51481377": {}, // women's association football club
}

// clubDescriptionSkipKeywords is the set of description substrings
// that immediately disqualify a candidate AFTER P31 type-check. P31
// alone catches most junk (TV channels, stadiums, museums, matches)
// but a small set of clubs get typed as association football clubs
// while being reserve/women/youth teams. This keyword pass is the
// second-line filter matching the reject-set behavior.
var clubDescriptionSkipKeywords = []string{
	"women", "femen", "reserve", "youth", "junior",
	"u-19", "u-20", "u-21", "under-19", "under-20", "under-21",
	"academy", "futsal", "beach", "basketball",
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
// fuzzy search, batch-filters candidates by P31, then scores +
// returns the winning QID.
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

	// Stage 1: collect candidates across variants + label/keyword skips.
	// Description-based rejects apply here (reserve/women/youth); P31
	// filter runs next as a batch.
	var (
		candidates []wikidata.SearchHit
		seen       = make(map[string]struct{})
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
			candidates = append(candidates, h)
		}
	}
	if len(candidates) == 0 {
		return LookupResult{}, ErrNoMatch
	}

	// Stage 2: batch-fetch P31 for all unique candidates + filter.
	// One SPARQL call per team, regardless of variant count. Ontology-
	// grounded type check replaces the fragile description-text
	// heuristic — TV channels, stadiums, matches, museums, supporters'
	// associations all get dropped here even when their descriptions
	// happen to contain "football".
	//
	// SPARQL failure fallback: Wikidata's SPARQL endpoint sometimes
	// times out or returns 5xx on hot queries. Rather than cascade to
	// NoMatch (which drops legitimate clubs), fall back to the
	// pre-Layer-2 description-text heuristic (must contain
	// football/soccer/futbol). Less precise than P31 — Milan TV-class
	// mistakes can slip through this narrow window — but graceful
	// degradation beats a silent 100% miss rate during vendor blips.
	candidateIDs := make([]string, 0, len(candidates))
	for _, h := range candidates {
		candidateIDs = append(candidateIDs, h.ID)
	}
	p31, p31Err := r.wd.BatchGetP31(ctx, candidateIDs)
	filtered := candidates[:0]
	for _, h := range candidates {
		if p31Err != nil {
			if !descriptionLooksFootball(h.Description) {
				continue
			}
		} else if !passesP31Filter(p31[h.ID], clubAcceptP31, clubRejectP31) {
			continue
		}
		filtered = append(filtered, h)
	}
	if len(filtered) == 0 {
		return LookupResult{}, ErrNoMatch
	}

	// Stage 3: score survivors + short-circuit on perfect city match.
	var (
		bestHit   wikidata.SearchHit
		bestScore int
		haveBest  bool
	)
	for _, h := range filtered {
		score := scoreClubCandidate(h, cityLower, countryVariations)
		if score >= scoreCityMatchShortCircuit {
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
	return LookupResult{
		QID:         bestHit.ID,
		Label:       bestHit.Label,
		Description: bestHit.Description,
		Score:       bestScore,
	}, nil
}

// passesP31Filter checks a candidate's P31 type list against accept +
// reject sets. Returns true iff:
//   - the candidate has AT LEAST ONE type in the accept set, AND
//   - the candidate has NO types in the reject set.
//
// An empty type list (BatchGetP31 returned nothing for this QID)
// fails the accept check and returns false — safe default that
// prevents unknowns from slipping through.
func passesP31Filter(types []string, accept, reject map[string]struct{}) bool {
	hasAccept := false
	for _, t := range types {
		if _, ok := reject[t]; ok {
			return false
		}
		if _, ok := accept[t]; ok {
			hasAccept = true
		}
	}
	return hasAccept
}

// descriptionLooksFootball is the pre-Layer-2 text-heuristic used
// only as a fallback when BatchGetP31 fails (Wikidata SPARQL
// unavailable). Same logic Python used before the P31 upgrade —
// matches football/soccer/futbol/multisport descriptions. Coarser
// than P31 (Milan TV would pass this because its description
// mentions "football") but graceful during vendor blips.
func descriptionLooksFootball(description string) bool {
	desc := strings.ToLower(description)
	isFootball := strings.Contains(desc, "football") ||
		strings.Contains(desc, "soccer") ||
		strings.Contains(desc, "fútbol") ||
		strings.Contains(desc, "futbol")
	isMultisport := strings.Contains(desc, "multisport") ||
		strings.Contains(desc, "sports club")
	return isFootball || isMultisport
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

// clubCandidatePassesFilter runs the label-suffix + description-keyword
// checks (women/reserve/youth). Type-based filtering (football club vs
// TV channel vs stadium) is handled by the P31 batch step, not here —
// this function only enforces the reserve/women/youth reject that P31
// alone can miss when the subclass isn't explicit in the entity's P31.
func clubCandidatePassesFilter(h wikidata.SearchHit) bool {
	desc := strings.ToLower(h.Description)
	label := h.Label
	labelLower := strings.ToLower(label)

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
