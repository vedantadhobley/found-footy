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

// NOTE (2026-07-24): the former club REJECT-set (reserve teams, women's
// teams) was removed. It hard-dropped B/women sides even when
// api-football itself named one as the team that scored — which happens
// in friendlies (first team vs a B side). api-football is ground truth
// for WHICH team scored, so selection now ranks candidates by name-match
// to the api name (pickBestNameMatch) rather than excluding subtypes.
// The accept-set stays as a sanity guard (must be a football club, not a
// stadium/song). See decisions.md 2026-07-24.

// resolveClub runs the club branch of the lookup pipeline.
//
// Two HTTP calls, one round trip each:
//   1. Wikipedia CirrusSearch with template `{name} {country} football club`
//      → hits with pageprops.wikibase_item extracted.
//   2. SPARQL P31 batch verify against Wikidata → keep hits whose type
//      set intersects clubAcceptP31 (sanity: must be a football club).
//
// Among the survivors, the winner is the one whose Wikipedia title best
// matches the api-football team name (pickBestNameMatch) — NOT Wikipedia's
// rank order, which mis-picked reserve teams it happened to rank first
// (Sporting CP B over Sporting CP; see decisions.md 2026-07-24).
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

	// Batch P31 verify, then collect ALL candidates worth ranking (not
	// just the first). With P31 available, keep hits whose type passes
	// the accept-set. On SPARQL failure we can't type-check, so keep
	// every hit and let name-matching decide — still better than blindly
	// trusting Wikipedia's #1. (nil reject-set: subtype exclusion is gone,
	// see the NOTE above — name-matching handles senior-vs-B.)
	p31, p31Err := r.wd.BatchGetP31(ctx, candidateIDs)
	candidates := make([]wikipedia.Hit, 0, len(hits))
	for _, h := range hits {
		if h.WikidataQID == "" {
			continue
		}
		if p31Err != nil || passesP31Filter(p31[h.WikidataQID], clubAcceptP31, nil) {
			candidates = append(candidates, h)
		}
	}
	if len(candidates) == 0 {
		return LookupResult{}, ErrNoMatch
	}

	// Rank by title-vs-api-name similarity, not Wikipedia rank order.
	best := pickBestNameMatch(name, candidates)
	return LookupResult{QID: best.WikidataQID, Label: best.Title}, nil
}

// pickBestNameMatch selects, from the P31-passing candidates, the hit
// whose Wikipedia title most closely matches the api-football team name.
// Replaces the old "first hit in Wikipedia rank order wins" rule, which
// mis-picked reserve sides Wikipedia ranked above their senior team.
//
// The api name is ground truth for WHICH team scored, so we rank by
// token-set similarity to it: the best candidate neither ADDS qualifier
// tokens the api name lacks ("b", "ii", "women") nor MISSES tokens it has
// ("castilla"). Symmetric, so it self-corrects in both directions —
// api "Sporting CP" picks the senior (B is penalized for the extra "b"),
// api "Real Madrid Castilla" picks the B side (senior is penalized for
// missing "castilla"). Wikipedia rank order breaks exact ties (the range
// loop preserves input order and `>` keeps the earliest).
//
// Caller guarantees len(hits) > 0.
func pickBestNameMatch(apiName string, hits []wikipedia.Hit) wikipedia.Hit {
	want := nameTokenSet(apiName)
	best := hits[0]
	bestScore := nameMatchScore(want, nameTokenSet(hits[0].Title))
	for _, h := range hits[1:] {
		if s := nameMatchScore(want, nameTokenSet(h.Title)); s > bestScore {
			bestScore = s
			best = h
		}
	}
	return best
}

// nameMatchScore scores a candidate token set against the wanted set as
// the negative symmetric difference: 0 is an exact match, and each token
// present in one set but not the other costs one point. Higher (closer to
// 0) is better.
func nameMatchScore(want, got map[string]struct{}) int {
	diff := 0
	for t := range got {
		if _, ok := want[t]; !ok {
			diff++
		}
	}
	for t := range want {
		if _, ok := got[t]; !ok {
			diff++
		}
	}
	return -diff
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
