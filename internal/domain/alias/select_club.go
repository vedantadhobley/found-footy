// select_club.go — V5-shaped club selection.
package alias

// selectClub runs the V5 rules for club aliases:
//
//   1. Extract multilingual aliases + labels + P1449 + canonical.
//   2. Keep tokens satisfying: appears in ≥2 langs OR P1449 OR canonical.
//   3. Rescue single-lang English aliases (LFC, CFC, MCFC) — the "V10"
//      addition on top of V5.
//   4. Apply venue-city skip when city ∉ canonical name.
//
// Called from Resolver.Select via alias.selection dispatch.
func selectClub(src selectionSources, venueCity *string) []string {
	kept := applyThreshold(src, 2)
	// V10 rescue — union in English aliases regardless of threshold.
	for tok := range englishAliasTokens(src) {
		kept[tok] = struct{}{}
	}
	// Venue-city skip — drops city tokens that aren't part of the
	// canonical team name (Arsenal ≠ London; Liverpool ✓ Liverpool).
	applyVenueCitySkip(kept, src.canonicalTokens, venueCity)
	return mergeAndSort(kept)
}
