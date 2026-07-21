// lookup_national.go — national-team branch of the alias-lookup pipeline.
//
// Same shape as the club branch (Wikipedia CirrusSearch + P31 verify)
// with a different query template. Wikipedia's article titling
// convention for national teams is highly regular — `{country} men's
// national football team` matches the exact article title in most
// cases, making this branch nearly deterministic.
package alias

import (
	"context"
	"strings"

	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
)

// P31 accept-set for national candidates. Verified across Brazil /
// England / France / Japan (2026-07-20): all modern national men's
// teams have Q135408445 as their canonical type. Older entities might
// additionally have Q6979593 (national association football team) so
// it's included for forward-compat but not strictly needed against
// modern data.
var nationalAcceptP31 = map[string]struct{}{
	"Q135408445": {}, // men's national association football team
	"Q6979593":   {}, // national association football team (legacy)
}

// nationalRejectP31 mirrors the club reject-set shape — women's national
// teams have their own P31 (Q6997908).
var nationalRejectP31 = map[string]struct{}{
	"Q6997908": {}, // women's national association football team
}

// resolveNational runs the national-team branch. Same two-HTTP shape
// as resolveClub. Query template uses country when available (more
// reliable than the name in the "England" / "United States" cases
// where the country IS the team name); falls back to name + `national
// football team` when country isn't provided.
func (r *Resolver) resolveNational(ctx context.Context, name string, country *string) (LookupResult, error) {
	// Prefer country in the query when available — matches Wikipedia's
	// article title conventions ("France men's national football team",
	// "Brazil men's national football team", etc.). Nil country falls
	// back to the name-based template.
	base := name
	if country != nil && *country != "" {
		base = *country
	}
	// USA is a specific Wikipedia quirk — the US team's article is
	// titled "United States men's national soccer team" (not
	// "football"). Substitute the fuller form when the input matches.
	if strings.EqualFold(base, "USA") {
		base = "United States"
	}
	query := base + " men's national football team"

	hits, err := r.wp.SearchAndResolve(ctx, query, wikipedia.SearchOpts{Limit: 10})
	if err != nil {
		return LookupResult{}, err
	}
	if len(hits) == 0 {
		return LookupResult{}, ErrNoMatch
	}

	candidateIDs := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.WikidataQID != "" {
			candidateIDs = append(candidateIDs, h.WikidataQID)
		}
	}
	if len(candidateIDs) == 0 {
		return LookupResult{}, ErrNoMatch
	}

	p31, p31Err := r.wd.BatchGetP31(ctx, candidateIDs)
	for _, h := range hits {
		if h.WikidataQID == "" {
			continue
		}
		if p31Err != nil {
			return LookupResult{QID: h.WikidataQID, Label: h.Title}, nil
		}
		if !passesP31Filter(p31[h.WikidataQID], nationalAcceptP31, nationalRejectP31) {
			continue
		}
		return LookupResult{QID: h.WikidataQID, Label: h.Title}, nil
	}
	return LookupResult{}, ErrNoMatch
}
