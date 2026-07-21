// lookup_club.go — club branch of the alias-lookup pipeline.
//
// One Wikipedia CirrusSearch query, one SPARQL P31 batch verify, first
// P31-passing hit wins. Replaced the earlier 9-variant `wbsearchentities`
// stack + description-quality scoring — see
// docs/rebuild/proposals/alias-entity-resolution.md for the empirical
// basis and reasoning.
package alias

import (
	"context"

	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
)

// P31 accept-set for club candidates. Verified against a sweep of 15
// well-known clubs (2026-07-20): 14 have Q476028 directly; FC Barcelona
// (Q7156, multisport) has Q103229495 + Q10651067 + Q20639856 — the
// men's-team subtype is the only shared discriminator, so we include
// it. Adding more accept types is cheap but each addition invites false
// positives — resist unless we see real-world misses.
var clubAcceptP31 = map[string]struct{}{
	"Q476028":    {}, // association football club
	"Q103229495": {}, // men's association football team
}

// P31 reject-set for club candidates. A candidate that matches ANY
// reject type is dropped even if it also matches an accept type. This
// catches subclasses of "football club" that we specifically don't want
// (reserves, women's teams) — e.g. Milan Futuro (Q126923253) has both
// Q476028 AND Q2412834; the reject rule filters it out.
var clubRejectP31 = map[string]struct{}{
	"Q2412834":  {}, // reserve team
	"Q51481377": {}, // women's association football club
}

// resolveClub runs the club branch of the lookup pipeline.
//
// Two HTTP calls, one round trip each:
//   1. Wikipedia CirrusSearch with template `{name} {country} football club`
//      → hits with pageprops.wikibase_item extracted.
//   2. SPARQL P31 batch verify against Wikidata → filter to hits whose
//      type set intersects clubAcceptP31 and doesn't touch clubRejectP31.
//
// First surviving hit (Wikipedia's own rank position) wins.
//
// Country is strongly recommended for disambiguation — same-name clubs
// across regions (Al-Ahly Egypt vs Al-Ahli Amman, São Paulo FC vs
// several São Paulo entities) get sorted out by the country term. Nil
// country falls through to just `{name} football club` which is less
// reliable but still functional for teams whose name is unique.
func (r *Resolver) resolveClub(ctx context.Context, name string, country *string) (LookupResult, error) {
	query := name + " football club"
	if country != nil && *country != "" {
		query = name + " " + *country + " football club"
	}

	hits, err := r.wp.SearchAndResolve(ctx, query, wikipedia.SearchOpts{Limit: 10})
	if err != nil {
		return LookupResult{}, err
	}
	if len(hits) == 0 {
		return LookupResult{}, ErrNoMatch
	}

	// Collect candidate QIDs (skip hits without a Wikidata sitelink;
	// they can't be P31-verified anyway).
	candidateIDs := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.WikidataQID != "" {
			candidateIDs = append(candidateIDs, h.WikidataQID)
		}
	}
	if len(candidateIDs) == 0 {
		return LookupResult{}, ErrNoMatch
	}

	// Batch P31 verify. On SPARQL failure fall back to "take the top
	// Wikipedia hit unconditionally" — CirrusSearch's ranking is
	// generally good enough that even without type verification the
	// top hit is usually the right entity. Better than cascading to
	// NoMatch when the vendor blips.
	p31, p31Err := r.wd.BatchGetP31(ctx, candidateIDs)
	for _, h := range hits {
		if h.WikidataQID == "" {
			continue
		}
		if p31Err != nil {
			// SPARQL unavailable — trust Wikipedia's ranking.
			return LookupResult{QID: h.WikidataQID, Label: h.Title}, nil
		}
		if !passesP31Filter(p31[h.WikidataQID], clubAcceptP31, clubRejectP31) {
			continue
		}
		return LookupResult{QID: h.WikidataQID, Label: h.Title}, nil
	}
	return LookupResult{}, ErrNoMatch
}

// passesP31Filter checks a candidate's P31 type list against accept +
// reject sets. Returns true iff:
//   - the candidate has AT LEAST ONE type in the accept set, AND
//   - the candidate has NO types in the reject set.
//
// Empty type list (BatchGetP31 returned nothing for this QID) fails the
// accept check and returns false — safe default that prevents unknowns
// from slipping through.
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
