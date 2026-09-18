// Tests for normalized competition round and priority presentation.
package fixturepresentation

import "testing"

func TestPresentRound(t *testing.T) {
	cases := []struct {
		raw       string
		wantLabel string
		wantKind  RoundKind
	}{
		{"Regular Season - 5", "Matchweek 5", RoundKindRegularSeason},
		{"regular season-12", "Matchweek 12", RoundKindRegularSeason},
		{" Final ", "Final", RoundKindFinal},
		{"Round of 16", "Round of 16", RoundKindOther},
		{"", "", RoundKindUnknown},
	}
	for _, tc := range cases {
		got := PresentRound(tc.raw)
		if got.Label != tc.wantLabel || got.Kind != tc.wantKind {
			t.Errorf("PresentRound(%q) = %+v, want label=%q kind=%q", tc.raw, got, tc.wantLabel, tc.wantKind)
		}
	}
}

func TestCompetitionPriorityPreservesCurrentOrder(t *testing.T) {
	if CompetitionPriority(39) != 39 || CompetitionPriority(140) != 140 {
		t.Fatal("competition priority no longer preserves the current league-id order")
	}
}
