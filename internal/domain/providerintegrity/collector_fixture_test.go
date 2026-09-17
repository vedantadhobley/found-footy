// collector_fixture_test.go exercises evidence produced from actual sequential
// observations, rather than caller-supplied freshness/recovery timestamps.
package providerintegrity

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// collectorObservation gives consistency checks stable identity and score data.
func collectorObservation(id int64, phase string, minute int) HealthObservation {
	f := FixtureFacts{FixtureID: id, HomeID: 1, AwayID: 2, LeagueID: 3, HomeName: "Home", AwayName: "Away", LeagueName: "League",
		Status: phase, Terminal: phase == "ft", Elapsed: intp(minute), HomeScore: intp(2), AwayScore: intp(1)}
	return HealthObservation{FixtureID: id, Availability: AvailabilityUsable, Comparison: FixtureComparison{Stored: f, Observed: f}}
}

// collectorPoll supplies immutable, distinct logical poll IDs and times.
func collectorPoll(tick int, observations ...HealthObservation) HealthPoll {
	return HealthPoll{ID: fmt.Sprintf("poll-%d", tick), At: time.Date(2026, 9, 16, 21, 0, 0, 0, time.UTC).Add(time.Duration(tick) * 30 * time.Second), Observations: observations}
}

// TestCollectorFrozenClockSurvivesContinuityAndIndependentRecovery reproduces
// the incident, then checks availability and clock recovery independently.
func TestCollectorFrozenClockSurvivesContinuityAndIndependentRecovery(t *testing.T) {
	s := FixtureHealth{FixtureID: 1}
	o := collectorObservation(1, "2h", 65)
	var transitions []HealthTransition
	for i := 0; i <= 40; i++ {
		transitions = append(transitions, ObserveFixture(&s, collectorPoll(i, o), o)...)
	}
	require.Len(t, transitions, 1)
	require.Equal(t, CauseClockStalled, transitions[0].Cause)
	require.Equal(t, collectorPoll(6).At, transitions[0].StartedAt)
	require.Equal(t, collectorPoll(0).At, s.ClockProgressAt)
	require.Len(t, s.Evidence.Incidents[0].Probes, maxRecoveryProbes)
	// Transport failure resets continuity but cannot erase the stalled cause.
	fail := HealthObservation{FixtureID: 1, Availability: AvailabilityTransport}
	ObserveFixture(&s, collectorPoll(41, fail), fail)
	require.Len(t, s.Evidence.Incidents, 2)
	ObserveFixture(&s, collectorPoll(42, o), o)
	require.Len(t, s.Evidence.Incidents, 1)
	require.Equal(t, FreshnessUnknown, s.Evidence.Freshness)
	require.Equal(t, PolicyPositiveOnly, FixtureHealthPolicy(s))
	o = collectorObservation(1, "2h", 66)
	ObserveFixture(&s, collectorPoll(43, o), o)
	require.Equal(t, PolicyPositiveOnly, FixtureHealthPolicy(s), "one advance cannot recover")
	o = collectorObservation(1, "2h", 67)
	resolved := ObserveFixture(&s, collectorPoll(44, o), o)
	require.Len(t, resolved, 1)
	require.True(t, resolved[0].Resolved)
	require.Equal(t, PolicyTrusted, FixtureHealthPolicy(s))
}

// TestCollectorGapAndRollbackCannotInventProgress pins missing observations,
// retries, rollback/return oscillation, and phase-boundary warning behavior.
func TestCollectorGapAndRollbackCannotInventProgress(t *testing.T) {
	s := FixtureHealth{FixtureID: 1}
	o := collectorObservation(1, "2h", 65)
	ObserveFixture(&s, collectorPoll(0, o), o)
	ObserveFixture(&s, collectorPoll(40, o), o)
	require.Equal(t, FreshnessUnknown, s.Evidence.Freshness)
	require.Empty(t, s.Evidence.Incidents)
	for i := 41; i <= 46; i++ {
		if i%2 == 1 {
			o.Comparison.Observed.Elapsed = intp(64)
		} else {
			o.Comparison.Observed.Elapsed = intp(65)
		}
		ObserveFixture(&s, collectorPoll(i, o), o)
	}
	require.Equal(t, collectorPoll(0).At, s.ClockProgressAt)
	require.Equal(t, FreshnessStalled, s.Evidence.Freshness)
	copyState := s
	require.Empty(t, ObserveFixture(&s, collectorPoll(46, o), o))
	require.Empty(t, ObserveFixture(&s, collectorPoll(45, o), o))
	require.Equal(t, copyState, s)
	boundary := FixtureHealth{FixtureID: 2}
	for i := 0; i <= 20; i++ {
		b := collectorObservation(2, "1h", 45)
		ObserveFixture(&boundary, collectorPoll(i, b), b)
	}
	require.Equal(t, FreshnessBoundaryWarning, boundary.Evidence.Freshness)
	require.Equal(t, PolicyTrusted, FixtureHealthPolicy(boundary))
}

// TestCollectorRetainsCausalBaseline proves three corrupted canonical snapshots
// cannot erase an earlier contradiction or acquire a fresh incident onset.
func TestCollectorRetainsCausalBaseline(t *testing.T) {
	s := FixtureHealth{FixtureID: 1}
	o := collectorObservation(1, "2h", 65)
	o.Comparison.Observed.HomeScore = intp(0)
	first := ObserveFixture(&s, collectorPoll(0, o), o)
	require.Len(t, first, 1)
	require.Equal(t, HealthCause(ReasonScoreDecreased), first[0].Cause)
	o.Comparison.Stored = o.Comparison.Observed // existing shadow path wrote the bad score
	for i := 1; i < 4; i++ {
		require.Empty(t, ObserveFixture(&s, collectorPoll(i, o), o))
	}
	require.Equal(t, PolicyPositiveOnly, FixtureHealthPolicy(s))
	require.Equal(t, 2, *s.Evidence.Incidents[0].Baseline.HomeScore)
	o.Comparison.Observed.HomeScore = intp(2)
	transitions := ObserveFixture(&s, collectorPoll(4, o), o)
	require.Len(t, transitions, 1)
	require.True(t, transitions[0].Resolved)
	require.Empty(t, s.Evidence.Incidents)
}

// TestCollectorRecoveryRetainsOriginalEventInventory prevents a later pending
// event from falsely explaining a score regression that removed an older goal.
func TestCollectorRecoveryRetainsOriginalEventInventory(t *testing.T) {
	s := FixtureHealth{FixtureID: 1}
	o := collectorObservation(1, "2h", 65)
	home := EventFact{Key: "home", TeamID: 1, Type: "goal", Minute: 20, DebounceCount: 3}
	away := EventFact{Key: "away", TeamID: 2, Type: "goal", Minute: 25, DebounceCount: 3}
	old := EventFact{Key: "old", TeamID: 1, Type: "goal", Minute: 30, DebounceCount: 3}
	o.Comparison.StoredEvents = []EventFact{home, away, old}
	o.Comparison.ConfirmedEvents = []EventFact{home, away, old}
	o.Comparison.ObservedEvents = []EventFact{home, away}
	o.Comparison.Observed.HomeScore = intp(1)
	ObserveFixture(&s, collectorPoll(0, o), o)
	require.Len(t, s.Evidence.Incidents, 2)

	// Model changed canonical history after the incident. A different recent
	// goal could explain a new cancellation, but not this retained old loss.
	pending := EventFact{Key: "pending", TeamID: 1, Type: "goal", Minute: 64, DebounceCount: 2}
	o.Comparison.Stored = o.Comparison.Observed
	o.Comparison.StoredEvents = []EventFact{home, away, pending}
	o.Comparison.ConfirmedEvents = []EventFact{home, away}
	require.Empty(t, ObserveFixture(&s, collectorPoll(1, o), o))
	require.Len(t, s.Evidence.Incidents, 2)
	require.Equal(t, PolicyPositiveOnly, FixtureHealthPolicy(s))

	o.Comparison.Observed.HomeScore = intp(2)
	o.Comparison.ObservedEvents = []EventFact{home, away, old}
	ObserveFixture(&s, collectorPoll(2, o), o)
	require.Empty(t, s.Evidence.Incidents)
}

// TestCollectorTerminalAndRetryRecovery retains ordinary FT recovery, while a
// failed attempt in the same logical poll resets the recovery proof.
func TestCollectorTerminalAndRetryRecovery(t *testing.T) {
	s := FixtureHealth{FixtureID: 1}
	o := collectorObservation(1, "2h", 65)
	for i := 0; i <= 6; i++ {
		ObserveFixture(&s, collectorPoll(i, o), o)
	}
	o = collectorObservation(1, "ft", 90)
	ObserveFixture(&s, collectorPoll(7, o), o)
	o.Interrupted = true
	ObserveFixture(&s, collectorPoll(8, o), o)
	o.Interrupted = false
	ObserveFixture(&s, collectorPoll(9, o), o)
	ObserveFixture(&s, collectorPoll(10, o), o)
	require.Equal(t, PolicyPositiveOnly, FixtureHealthPolicy(s))
	ObserveFixture(&s, collectorPoll(11, o), o)
	require.Equal(t, PolicyTrusted, FixtureHealthPolicy(s))
}

// TestCollectorUnknownPhaseCanAcquireClock avoids trapping a fixture forever
// in an unranked LIVE/PST status seen before its first timed observation.
func TestCollectorUnknownPhaseCanAcquireClock(t *testing.T) {
	s := FixtureHealth{FixtureID: 1}
	o := collectorObservation(1, "live", 0)
	o.Comparison.Observed.Elapsed = nil
	ObserveFixture(&s, collectorPoll(0, o), o)
	o = collectorObservation(1, "1h", 25)
	for tick := 1; tick <= 7; tick++ {
		ObserveFixture(&s, collectorPoll(tick, o), o)
	}
	require.Equal(t, FreshnessStalled, s.Evidence.Freshness)
	require.Equal(t, "1h", s.ClockStatus)
	require.Equal(t, collectorPoll(1).At, s.PhaseSince)
	o = collectorObservation(1, "int", 25)
	ObserveFixture(&s, collectorPoll(8, o), o)
	require.Equal(t, FreshnessNotClocked, s.Evidence.Freshness)
	require.Equal(t, "int", s.ObservedStatus)
	require.Equal(t, collectorPoll(8).At, s.PhaseSince)
	require.Equal(t, "1h", s.ClockStatus, "interruption cannot lower the clock high-water mark")
}

// TestCollectorRecoveryKeepsOtherCauses proves a progressing clock cannot hide
// a still-regressed score merely because canonical storage now has the bad value.
func TestCollectorRecoveryKeepsOtherCauses(t *testing.T) {
	s := FixtureHealth{FixtureID: 1}
	o := collectorObservation(1, "2h", 65)
	o.Comparison.Observed.HomeScore = intp(0)
	ObserveFixture(&s, collectorPoll(0, o), o)
	o.Comparison.Stored = o.Comparison.Observed
	for tick := 1; tick <= 6; tick++ {
		ObserveFixture(&s, collectorPoll(tick, o), o)
	}
	require.Len(t, s.Evidence.Incidents, 2)
	for tick := 7; tick <= 8; tick++ {
		o.Comparison.Observed.Elapsed = intp(65 + tick - 6)
		ObserveFixture(&s, collectorPoll(tick, o), o)
	}
	require.Len(t, s.Evidence.Incidents, 1)
	require.Equal(t, HealthCause(ReasonScoreDecreased), s.Evidence.Incidents[0].Cause)
	require.Equal(t, PolicyPositiveOnly, FixtureHealthPolicy(s))
	o.Comparison.Observed.HomeScore = intp(2)
	ObserveFixture(&s, collectorPoll(9, o), o)
	require.Empty(t, s.Evidence.Incidents)
}

// TestCollectorMissingGenerationDoesNotDisappearWithCanonicalHistory retains
// the original missing key for audit instead of silently matching another UUID
// generation under a newly invented health-only event identity rule.
func TestCollectorMissingGenerationDoesNotDisappearWithCanonicalHistory(t *testing.T) {
	s := FixtureHealth{FixtureID: 1}
	o := collectorObservation(1, "2h", 65)
	old := EventFact{Key: "old-goal", TeamID: 1, PlayerID: intp(10), Type: "goal", Minute: 30, DebounceCount: 3}
	o.Comparison.ConfirmedEvents = []EventFact{old}
	changes := ObserveFixture(&s, collectorPoll(0, o), o)
	require.Len(t, changes, 1)
	require.Equal(t, []string{"old-goal"}, changes[0].MissingEventKeys)
	newGeneration := old
	newGeneration.Key = "new-goal"
	o.Comparison.ConfirmedEvents = []EventFact{newGeneration}
	o.Comparison.ObservedEvents = []EventFact{newGeneration}
	ObserveFixture(&s, collectorPoll(1, o), o)
	require.Equal(t, PolicyPositiveOnly, FixtureHealthPolicy(s))
	require.Equal(t, "old-goal", s.Evidence.Incidents[0].ConfirmedEvents[0].Key)
}
