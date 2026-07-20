// lookup_national.go — national-team branch of the alias-lookup
// pipeline.
//
// National team names are near-unambiguous in Wikidata — "France
// national football team" resolves cleanly to Q47774 without the
// disambiguation gymnastics clubs require. Three fuzzy variants +
// batch P31 verification against the "men's national association
// football team" type (Q135408445) picks the right entity even when
// wbsearchentities also returns fictional characters or misclassified
// entities that happen to share the country's name.
package alias

import (
	"context"
	"strings"

	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
)

// P31 accept-set for national candidates. Verified across Brazil /
// England / France / Japan / Argentina (2026-07-20): all real national
// men's teams have Q135408445 as their canonical type. Older entities
// might additionally have Q6979593 (national association football
// team) so it's included for forward-compat but not strictly needed
// against modern data.
var nationalAcceptP31 = map[string]struct{}{
	"Q135408445": {}, // men's national association football team
	"Q6979593":   {}, // national association football team (legacy)
}

// nationalRejectP31 mirrors the club shape — women's national teams
// have their own P31 (Q6997908, women's national association football
// team). Youth-national types exist too but P31 alone catches the
// senior/junior split cleanly.
var nationalRejectP31 = map[string]struct{}{
	"Q6997908": {}, // women's national association football team
}

// resolveNational is the national-branch entry point. Same 3-variant
// fuzzy stack as before + a P31 batch-filter step (replaces the
// description-contains-"national"/"football" text checks).
func (r *Resolver) resolveNational(ctx context.Context, name string, country *string) (LookupResult, error) {
	variants := buildNationalSearchVariants(name)

	// Stage 1: collect candidates + keyword pre-skip.
	var (
		candidates []wikidata.SearchHit
		seen       = make(map[string]struct{})
	)
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
			candidates = append(candidates, h)
		}
	}
	if len(candidates) == 0 {
		return LookupResult{}, ErrNoMatch
	}

	// Stage 2: batch P31 type-check. Kills wbsearchentities false
	// positives (fictional characters, museums, towns) that happen to
	// share a country's name. SPARQL failure falls back to the
	// pre-Layer-2 description-text heuristic (must contain "national"
	// + "football"/"soccer") so a Wikidata blip doesn't cascade to a
	// NoMatch for the whole national roster.
	candidateIDs := make([]string, 0, len(candidates))
	for _, h := range candidates {
		candidateIDs = append(candidateIDs, h.ID)
	}
	p31, p31Err := r.wd.BatchGetP31(ctx, candidateIDs)
	for _, h := range candidates {
		if p31Err != nil {
			if !descriptionLooksNational(h.Description) {
				continue
			}
		} else if !passesP31Filter(p31[h.ID], nationalAcceptP31, nationalRejectP31) {
			continue
		}
		// First surviving candidate wins — national-team naming is
		// unambiguous enough that further scoring adds noise.
		return LookupResult{
			QID:         h.ID,
			Label:       h.Label,
			Description: h.Description,
			Score:       0,
		}, nil
	}
	return LookupResult{}, ErrNoMatch
}

// descriptionLooksNational is the national-branch fallback filter
// used only when BatchGetP31 fails. Same rules Python used pre-P31.
func descriptionLooksNational(description string) bool {
	desc := strings.ToLower(description)
	if !strings.Contains(desc, "national") {
		return false
	}
	return strings.Contains(desc, "football") || strings.Contains(desc, "soccer")
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

// nationalCandidatePassesFilter is the pre-P31 label/description
// keyword guard. P31 handles the "is this a national team" question;
// this function catches women/youth/futsal variants whose P31
// might not distinguish (they sometimes share the parent type with
// senior men's teams). Uses the same skip-list as clubs.
func nationalCandidatePassesFilter(h wikidata.SearchHit) bool {
	desc := strings.ToLower(h.Description)
	labelLower := strings.ToLower(h.Label)

	for _, kw := range clubDescriptionSkipKeywords {
		if strings.Contains(desc, kw) || strings.Contains(labelLower, kw) {
			return false
		}
	}
	return true
}
