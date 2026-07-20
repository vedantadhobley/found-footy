// countryvars.go — per-country variations lookup for the club branch's
// description-quality scoring.
//
// Given a country name (as reported by API-Football, e.g. "Spain"),
// produce a set of lowercased + diacritic-stripped tokens that a
// Wikidata entity description might use to identify the country:
//
//   Spain   → [spain, spanish, spaniard, espana, espanya, ...]
//   Germany → [germany, german, deutschland, deutsch, allemagne, ...]
//   England → [england, english, angleterre, ...]  (via UK entity)
//
// Data sources — all from the country's Wikidata entity:
//   - P1549 (demonym) across all languages
//   - P1448 (official name) across all languages
//   - The country name itself (always included as a fallback)
//
// Deterministic replacement for Python's LLM-generated variations
// map. Same practical output, no LLM.
package alias

import (
	"context"

	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
)

// For a given country name, return the union of variation tokens.
// Cached in-memory keyed by lowercased country name.
//
// Returns an empty slice on any Wikidata failure — variations are
// used only as a scoring signal, so an empty list means "no bonus
// for country match" (still resolvable via city + team-name signals).
// Errors are NOT propagated to the caller so a Wikidata hiccup on
// one country doesn't kill an otherwise-fine club resolution.
func (v *CountryVariations) For(ctx context.Context, countryName string) []string {
	if countryName == "" {
		return nil
	}
	key := lowerASCII(countryName)

	v.mu.RLock()
	cached, ok := v.cache[key]
	v.mu.RUnlock()
	if ok {
		return cached
	}

	// Miss — fetch. Always populate the cache with whatever we produce
	// (even empty) so we don't re-hit Wikidata on failure.
	variations := v.fetchVariations(ctx, countryName)

	v.mu.Lock()
	v.cache[key] = variations
	v.mu.Unlock()
	return variations
}

// fetchVariations does the actual Wikidata resolution: wbsearchentities
// for the country name → GetEntity for the top hit → extract P1549 +
// P1448 → normalize.
//
// The top-hit heuristic is safe here because country names are
// unambiguous in Wikidata (only one Q31 for Belgium, one Q145 for
// UK, etc.). If Wikidata's fuzzy search returns something else on
// top (rare), we're just missing scoring bonus — not corrupting
// resolution.
func (v *CountryVariations) fetchVariations(ctx context.Context, countryName string) []string {
	// Always include the country name itself as a variation.
	out := map[string]struct{}{
		lowerASCII(countryName): {},
	}

	hits, err := v.wd.SearchEntities(ctx, countryName, wikidata.SearchOpts{Language: "en", Limit: 3})
	if err != nil || len(hits) == 0 {
		return sortedKeys(out)
	}

	// Take the first hit — for country names this is deterministically
	// the country entity. Cache its QID for symmetry.
	countryQID := hits[0].ID

	v.mu.Lock()
	v.qidCache[lowerASCII(countryName)] = countryQID
	v.mu.Unlock()

	ent, err := v.wd.GetEntity(ctx, countryQID)
	if err != nil {
		return sortedKeys(out)
	}

	// P1549 demonyms across all languages — normalize each into tokens.
	for _, d := range ent.DemonymsP1549() {
		for _, tok := range tokenize(d.Text) {
			out[tok] = struct{}{}
		}
	}
	// P1448 native names across all languages.
	for _, n := range ent.NativeNamesP1448() {
		for _, tok := range tokenize(n.Text) {
			out[tok] = struct{}{}
		}
	}
	// English label of the country entity, split into tokens.
	for _, tok := range tokenize(ent.LabelEn()) {
		out[tok] = struct{}{}
	}
	return sortedKeys(out)
}
