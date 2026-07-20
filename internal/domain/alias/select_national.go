// select_national.go — V8-shaped national-team selection.
//
// V5 rules PLUS English P1549 demonyms from the linked country entity.
// Restricted to English (`lang == "en"`) because foreign-language
// demonym forms (`americain`-fr, `americana`-es, `amerikaner`-de)
// don't match English tweets and just bloat the query. English P1549
// contains all legit forms:
//   - Argentina: Argentine + Argentinian + Argentinean
//   - UK: British + Briton
//   - Spain: Spanish + Spaniard
//   - US: American + Americans
//
// Verified 2026-07-19 with the extension test — dropped USMNT output
// from 29 → 12 tokens with zero recall loss.
package alias

import (
	"context"

	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
)

// selectNational is a method on Resolver so it can call GetEntity
// again for the country entity. The team entity was already fetched
// by the caller (Resolver.Select) and passed as ent.
func (r *Resolver) selectNational(ctx context.Context, ent *wikidata.Entity, src selectionSources) []string {
	kept := applyThreshold(src, 2)
	// V10 rescue — English aliases regardless of threshold.
	for tok := range englishAliasTokens(src) {
		kept[tok] = struct{}{}
	}

	// Demonym expansion via the country entity (P17 → country QID →
	// GetEntity → P1549). Errors are non-fatal: nationals still work
	// without the demonym forms (canonical name + label + P1449
	// nicknames carry most weight). Just less recall on nationality-
	// adjective tweets.
	countryQID := ent.FirstClaimQID("P17")
	if countryQID != "" {
		countryEnt, err := r.wd.GetEntity(ctx, countryQID)
		if err == nil && countryEnt != nil {
			for _, d := range countryEnt.DemonymsP1549() {
				if d.Lang != "en" {
					continue
				}
				for _, tok := range tokenize(d.Text) {
					// Demonyms bypass the multilingual skip-list —
					// none of the current skip-list entries overlap
					// with legit demonym forms, but this is the intent.
					kept[tok] = struct{}{}
				}
			}
		}
	}
	return mergeAndSort(kept)
}
