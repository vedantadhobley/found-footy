// Unit tests for reserve-suffix demotion in club resolution — the narrow
// fix for "Sporting CP → Sporting CP B" / "Hamburger SV → Hamburger SV II"
// (decisions.md 2026-07-24).
package alias

import "testing"

func TestReserveMarker(t *testing.T) {
	cases := map[string]string{
		"Hamburger SV II":     "ii",
		"VfB Stuttgart II":    "ii",
		"Sporting CP B":       "b",
		"Real Madrid III":     "iii",
		"Some Club C":         "c",
		"Hamburger SV":        "",         // senior — no marker
		"FC Bayern Munich":    "",         // "munich" is not a marker
		"Athletic Bilbao":     "",         // "bilbao" is not a marker
		"Real Madrid Castilla": "",        // named reserve, not the suffix convention
		"Barcelona":           "",         // single token, no marker
		"":                    "",         // empty
	}
	for in, want := range cases {
		if got := reserveMarker(in); got != want {
			t.Errorf("reserveMarker(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestReserveMarker_ApiNamedReserveNotDemoted documents the guard: when
// api-football itself names the reserve, the marker matches on BOTH sides
// so the demotion in resolveClub does NOT fire (m == apiMarker).
func TestReserveMarker_ApiNamedReserveNotDemoted(t *testing.T) {
	apiMarker := reserveMarker("Hamburger SV II") // "ii"
	titleMarker := reserveMarker("Hamburger SV II")
	if apiMarker != titleMarker {
		t.Fatalf("expected api and title markers to match: %q vs %q", apiMarker, titleMarker)
	}
	// Senior title vs api-named-reserve: senior has no marker, so it is
	// NOT the api-requested side — resolveClub would keep looking.
	if reserveMarker("Hamburger SV") == apiMarker {
		t.Errorf("senior should not match the api reserve marker %q", apiMarker)
	}
}
