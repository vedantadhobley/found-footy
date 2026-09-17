// health_scope_test.go pins episode identity and scope without changing the
// production shadow aggregate's older count-based rules.
package providerintegrity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHealthScopeCountsFixturesNotMissingEvents rejects duplicate amplification.
func TestHealthScopeCountsFixturesNotMissingEvents(t *testing.T) {
	at := healthEvidence(0).ObservedAt
	one := HealthEpisode{FixtureID: 1, Cause: CauseIntegrityConflict, StartedAt: at}
	repeated := make([]HealthEpisode, 10)
	for i := range repeated {
		repeated[i] = one
	}
	require.Equal(t, PolicyTrusted, CorrelateHealth(at, repeated).Policy)
	repeated = append(repeated, HealthEpisode{FixtureID: 2, Cause: CauseIntegrityConflict, StartedAt: at})
	got := CorrelateHealth(at, repeated)
	require.Equal(t, PolicyPositiveOnly, got.Policy)
	require.Equal(t, []int64{1, 2}, got.ConflictFixtures)
}

// TestHealthScopeSeparatesCauses keeps stale feeds from restricting healthy ones.
func TestHealthScopeSeparatesCauses(t *testing.T) {
	at := healthEvidence(0).ObservedAt
	one := HealthEpisode{FixtureID: 1, Cause: CauseClockStalled, StartedAt: at}
	two := HealthEpisode{FixtureID: 2, Cause: CauseClockStalled, StartedAt: at}
	got := CorrelateHealth(at, []HealthEpisode{two, one})
	require.Equal(t, []int64{1, 2}, got.StalledFixtures)
	require.Equal(t, PolicyTrusted, got.Policy)
	two.Cause = CauseIntegrityConflict
	got = CorrelateHealth(at, []HealthEpisode{one, two})
	require.Empty(t, got.StalledFixtures)
	require.Empty(t, got.ConflictFixtures)
	require.Equal(t, PolicyTrusted, got.Policy)
	one.Cause = CauseBoundaryWarning
	two.Cause = CauseBoundaryWarning
	require.Empty(t, CorrelateHealth(at, []HealthEpisode{one, two}).StalledFixtures)
}

// TestHealthScopeOnsetWindow rejects recurring old, resolved, and future causes.
func TestHealthScopeOnsetWindow(t *testing.T) {
	at := healthEvidence(0).ObservedAt
	fresh := HealthEpisode{FixtureID: 2, Cause: CauseIntegrityConflict, StartedAt: at}
	for _, tc := range []struct {
		name  string
		other HealthEpisode
		want  MutationPolicy
	}{
		{"exact window", HealthEpisode{FixtureID: 1, Cause: CauseIntegrityConflict, StartedAt: at.Add(-2 * time.Minute)}, PolicyPositiveOnly},
		{"older than window", HealthEpisode{FixtureID: 1, Cause: CauseIntegrityConflict, StartedAt: at.Add(-121 * time.Second)}, PolicyTrusted},
		{"Cincinnati repeated old cause", HealthEpisode{FixtureID: 1490439, Cause: CauseIntegrityConflict, StartedAt: at.Add(-24 * time.Hour)}, PolicyTrusted},
		{"resolved", HealthEpisode{FixtureID: 1, Cause: CauseIntegrityConflict, StartedAt: at.Add(-time.Minute), ResolvedAt: at}, PolicyTrusted},
		{"future", HealthEpisode{FixtureID: 1, Cause: CauseIntegrityConflict, StartedAt: at.Add(time.Second)}, PolicyTrusted},
		{"missing onset", HealthEpisode{FixtureID: 1, Cause: CauseIntegrityConflict}, PolicyTrusted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, CorrelateHealth(at, []HealthEpisode{fresh, tc.other}).Policy)
		})
	}
}
