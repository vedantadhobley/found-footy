// provider_mutation_merge_test.go pins safe progress under restricted provider
// policy without losing retained clock facts or deriving inconsistent winners.
package pg

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
)

// TestMergePositiveFixtureRetainsFactsAndDerivesWinner checks that protecting
// source facts does not freeze a result projection after a legitimate score gain.
func TestMergePositiveFixtureRetainsFactsAndDerivesWinner(t *testing.T) {
	for _, tc := range []struct {
		name string
		away int
	}{
		{"equalizer", 1},
		{"lead changes", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			minute, extra, home, away := 65, 2, 1, 0
			before := &fixture.Fixture{
				APIStatus:  fixture.APIStatus{Short: "2h", Long: "Second Half"},
				APIElapsed: &minute, APIExtra: &extra, HomeScore: &home, AwayScore: &away,
			}
			before.UpdateResult(nil, nil)
			observed := *before
			observed.APIElapsed, observed.APIExtra, observed.HomeScore = nil, nil, nil
			observed.APIStatus.Long = ""
			observed.AwayScore = &tc.away
			got := mergePositiveFixture(before, &observed)
			require.Equal(t, before.APIStatus, got.APIStatus)
			require.Equal(t, minute, *got.APIElapsed)
			require.Equal(t, extra, *got.APIExtra)
			require.Equal(t, home, *got.HomeScore)
			require.Equal(t, tc.away, *got.AwayScore)
			if tc.away == home {
				require.Nil(t, got.HomeWinner)
				require.Nil(t, got.AwayWinner)
			} else {
				require.NotNil(t, got.HomeWinner)
				require.NotNil(t, got.AwayWinner)
				require.False(t, *got.HomeWinner)
				require.True(t, *got.AwayWinner)
			}
			require.True(t, *before.HomeWinner, "merging must not mutate the baseline")
		})
	}
}

// TestMergePositiveFixtureAllowsPeriodClockTransition keeps legitimate extra
// clearing possible when the provider supplies the next period's clock.
func TestMergePositiveFixtureAllowsPeriodClockTransition(t *testing.T) {
	minute, extra, next := 45, 3, 46
	before := &fixture.Fixture{APIStatus: fixture.APIStatus{Short: "ht"}, APIElapsed: &minute, APIExtra: &extra}
	observed := *before
	observed.APIStatus = fixture.APIStatus{Short: "2h", Long: "Second Half"}
	observed.APIElapsed, observed.APIExtra = &next, nil
	got := mergePositiveFixture(before, &observed)
	require.Equal(t, observed.APIStatus, got.APIStatus)
	require.Equal(t, next, *got.APIElapsed)
	require.Nil(t, got.APIExtra)
}
