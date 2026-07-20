// lookup_national.go — national-team branch of the alias-lookup
// pipeline.
//
// National team names are near-unambiguous in Wikidata — "France
// national football team" resolves cleanly to Q47774 without the
// disambiguation gymnastics clubs require. Three fuzzy variants +
// first-valid-football-team-wins is enough.
//
// Only risk is picking the women's national team when the men's one
// exists. Same description-keyword skip-list as clubs handles that.
package alias

import (
	"context"
	"strings"

	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
)

// resolveNational is the national-branch entry point.
func (r *Resolver) resolveNational(ctx context.Context, name string, country *string) (LookupResult, error) {
	variants := buildNationalSearchVariants(name)

	seen := make(map[string]struct{})
	for _, variant := range variants {
		hits, err := r.wd.SearchEntities(ctx, variant, defaultSearchOpts())
		if err != nil {
			continue
		}
		for _, h := range hits {
			if _, dupe := seen[h.ID]; dupe {
				continue
			}
			seen[h.ID] = struct{}{}
			if !nationalCandidatePassesFilter(h) {
				continue
			}
			// First valid candidate wins — no scoring, no country
			// disambiguation. Python uses this simpler approach
			// because national-team naming is unambiguous.
			return LookupResult{
				QID:         h.ID,
				Label:       h.Label,
				Description: h.Description,
				Score:       0,
			}, nil
		}
	}
	return LookupResult{}, ErrNoMatch
}

// buildNationalSearchVariants returns the ordered search-term list
// for national teams. Same three variants Python uses. The "USA"
// aliasing is a specific Wikidata quirk: their search finds the US
// team under "United States men's national soccer team", so we
// substitute the fuller form when input is bare "USA".
func buildNationalSearchVariants(name string) []string {
	n := strings.TrimSpace(name)
	nameForSearch := n
	if strings.EqualFold(nameForSearch, "USA") {
		nameForSearch = "United States"
	}
	return []string{
		nameForSearch + " national football team",
		nameForSearch + " men's national football team",
		nameForSearch + " national soccer team",
	}
}

// nationalCandidatePassesFilter ensures the candidate is actually a
// national football/soccer team. Same skip-keyword approach as
// clubs, filtering women's / U-20 / U-21 / futsal / etc.
func nationalCandidatePassesFilter(h wikidata.SearchHit) bool {
	desc := strings.ToLower(h.Description)
	labelLower := strings.ToLower(h.Label)

	// Must be national + football/soccer.
	if !strings.Contains(desc, "national") {
		return false
	}
	if !strings.Contains(desc, "football") && !strings.Contains(desc, "soccer") {
		return false
	}

	// Same skip-keyword list as clubs — filters women's team, youth
	// teams, futsal, beach soccer, etc.
	for _, kw := range clubDescriptionSkipKeywords {
		if strings.Contains(desc, kw) || strings.Contains(labelLower, kw) {
			return false
		}
	}
	return true
}
