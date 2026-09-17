// collector_provider_test.go pins scope and recovery using fixture transitions,
// including repeated local causes and independently progressing fixtures.
package providerintegrity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCollectorProviderRecoveryDoesNotClearFixtureConflicts proves provider
// recovery uses new affected fixtures, not repeated unresolved local warnings.
func TestCollectorProviderRecoveryDoesNotClearFixtureConflicts(t *testing.T) {
	p := ProviderHealth{}
	states := []FixtureHealth{{FixtureID: 1}, {FixtureID: 2}}
	for tick := 0; tick <= 4; tick++ {
		var observations []HealthObservation
		var transitions []HealthTransition
		for i := range states {
			o := collectorObservation(states[i].FixtureID, "2h", 65+tick)
			o.Comparison.Observed.HomeScore = intp(0)
			if tick > 0 {
				o.Comparison.Stored.HomeScore = intp(0)
			}
			observations = append(observations, o)
			transitions = append(transitions, ObserveFixture(&states[i], collectorPoll(tick, o), o)...)
		}
		out := ObserveProvider(&p, collectorPoll(tick, observations...), states, transitions)
		if tick < 3 {
			require.Equal(t, PolicyPositiveOnly, out.ProviderPolicy)
			require.EqualValues(t, 1, out.Generation)
		} else {
			require.Equal(t, PolicyTrusted, out.ProviderPolicy)
			require.EqualValues(t, 2, out.Generation)
		}
		require.Equal(t, []int64{1, 2}, out.RestrictedIDs)
	}
}

// TestCollectorOldIncidentCannotCorrelateWithNewReason keeps an old fixture
// warning from becoming a new onset when a second reason appears on that row.
func TestCollectorOldIncidentCannotCorrelateWithNewReason(t *testing.T) {
	p := ProviderHealth{}
	states := []FixtureHealth{{FixtureID: 1}, {FixtureID: 2}}
	for tick := 0; tick <= 8; tick++ {
		var observations []HealthObservation
		var transitions []HealthTransition
		for i := range states {
			o := collectorObservation(states[i].FixtureID, "2h", 65+tick)
			if i == 0 || tick == 8 {
				o.Comparison.Observed.HomeScore = intp(0)
			}
			if tick == 8 && i == 0 {
				o.Comparison.Observed.HomeName = ""
			}
			observations = append(observations, o)
			transitions = append(transitions, ObserveFixture(&states[i], collectorPoll(tick, o), o)...)
		}
		out := ObserveProvider(&p, collectorPoll(tick, observations...), states, transitions)
		require.Equal(t, PolicyTrusted, out.ProviderPolicy)
	}
}

// TestCollectorMultiFixtureStallOnlyWarnsProvider keeps freshness separate from
// correlated destructive regression, even though both affect several fixtures.
func TestCollectorMultiFixtureStallOnlyWarnsProvider(t *testing.T) {
	p := ProviderHealth{}
	states := []FixtureHealth{{FixtureID: 1}, {FixtureID: 2}}
	var out HealthResult
	for tick := 0; tick <= 6; tick++ {
		var observations []HealthObservation
		var transitions []HealthTransition
		for i := range states {
			o := collectorObservation(states[i].FixtureID, "2h", 65)
			observations = append(observations, o)
			transitions = append(transitions, ObserveFixture(&states[i], collectorPoll(tick, o), o)...)
		}
		out = ObserveProvider(&p, collectorPoll(tick, observations...), states, transitions)
	}
	require.Equal(t, PolicyTrusted, out.ProviderPolicy)
	require.Equal(t, []int64{1, 2}, out.Correlation.StalledFixtures)
	require.Equal(t, []int64{1, 2}, out.RestrictedIDs)
}
