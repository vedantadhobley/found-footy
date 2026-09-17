// health_test.go pins the FF-075 clock policy independently of workflow,
// repository, and durable collector adapters.
package providerintegrity

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHealthFreshnessThresholds pins exact boundaries and phase-specific caps.
func TestHealthFreshnessThresholds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		phase  string
		minute int
		age    time.Duration
		want   Freshness
		policy MutationPolicy
	}{
		{"interior before", "2h", 65, 179 * time.Second, FreshnessWithinWindow, PolicyTrusted},
		{"interior exact", "2h", 65, 180 * time.Second, FreshnessStalled, PolicyPositiveOnly},
		{"Barcelona twenty minutes", "2h", 65, 20 * time.Minute, FreshnessStalled, PolicyPositiveOnly},
		{"first half", "1h", 28, 3 * time.Minute, FreshnessStalled, PolicyPositiveOnly},
		{"first extra half", "et", 99, 3 * time.Minute, FreshnessStalled, PolicyPositiveOnly},
		{"second extra half", "et", 106, 3 * time.Minute, FreshnessStalled, PolicyPositiveOnly},
		{"boundary before", "1h", 45, 599 * time.Second, FreshnessWithinWindow, PolicyTrusted},
		{"boundary exact", "1h", 45, 600 * time.Second, FreshnessBoundaryWarning, PolicyTrusted},
		{"full time boundary", "2h", 90, 10 * time.Minute, FreshnessBoundaryWarning, PolicyTrusted},
		{"continuous overtime clock", "2h", 94, 10 * time.Minute, FreshnessBoundaryWarning, PolicyTrusted},
		{"extra time break boundary", "et", 105, 10 * time.Minute, FreshnessBoundaryWarning, PolicyTrusted},
		{"extra time end", "et", 120, 10 * time.Minute, FreshnessBoundaryWarning, PolicyTrusted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := healthEvidence(tc.age)
			evidence.Facts.Status, evidence.Facts.Elapsed = tc.phase, intp(tc.minute)
			got := AssessFreshness(evidence)
			require.Equal(t, tc.want, got.State)
			require.Equal(t, tc.policy, got.Policy)
			require.Equal(t, tc.age, got.EvidenceAge)
		})
	}
}

// TestHealthFreshnessNoRunningClock excludes genuine pauses and missing clocks.
func TestHealthFreshnessNoRunningClock(t *testing.T) {
	for _, phase := range []string{"ht", "bt", "p", "int", "susp", "ns", "pst", "tbd", "ft", "aet", "pen", "canc", "abd", "awd", "wo"} {
		t.Run(phase, func(t *testing.T) {
			e := healthEvidence(24 * time.Hour)
			e.Facts.Status = phase
			require.Equal(t, FreshnessNotClocked, AssessFreshness(e).State)
			require.Equal(t, PolicyTrusted, AssessFreshness(e).Policy)
		})
	}
	for _, phase := range []string{"live", "future_status", ""} {
		e := healthEvidence(24 * time.Hour)
		e.Facts.Status = phase
		require.Equal(t, FreshnessUnknown, AssessFreshness(e).State)
	}
}

// TestHealthFreshnessRequiresContinuousEvidence prevents local gaps or a single
// successful response from being reported as a proven provider freeze.
func TestHealthFreshnessRequiresContinuousEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*FreshnessEvidence)
	}{
		{"request failed", func(e *FreshnessEvidence) { e.Usable = false }},
		{"first sample", func(e *FreshnessEvidence) { e.PreviousUsableAt = time.Time{} }},
		{"same sample twice", func(e *FreshnessEvidence) { e.PreviousUsableAt = e.ObservedAt }},
		{"future prior", func(e *FreshnessEvidence) { e.PreviousUsableAt = e.ObservedAt.Add(time.Second) }},
		{"polling gap", func(e *FreshnessEvidence) { e.PreviousUsableAt = e.ObservedAt.Add(-91 * time.Second) }},
		{"missing continuity", func(e *FreshnessEvidence) { e.ContinuousSince = time.Time{} }},
		{"invalid continuity", func(e *FreshnessEvidence) { e.ContinuousSince = e.ObservedAt.Add(time.Minute) }},
		{"unknown progress", func(e *FreshnessEvidence) { e.ClockProgressAt = time.Time{} }},
		{"future progress", func(e *FreshnessEvidence) { e.ClockProgressAt = e.ObservedAt.Add(time.Second) }},
		{"missing clock", func(e *FreshnessEvidence) { e.Facts.Elapsed = nil }},
		{"negative clock", func(e *FreshnessEvidence) { e.Facts.Elapsed = intp(-1) }},
		{"negative extra", func(e *FreshnessEvidence) { e.Facts.Extra = intp(-1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := healthEvidence(20 * time.Minute)
			tc.mutate(&e)
			require.Equal(t, FreshnessUnknown, AssessFreshness(e).State)
			// Unknown is not authority to clear an existing cause.
			require.Equal(t, PolicyPositiveOnly, RestrictPolicies(PolicyPositiveOnly, AssessFreshness(e).Policy))
		})
	}
	e := healthEvidence(20 * time.Minute)
	e.ContinuousSince = e.ObservedAt.Add(-time.Minute)
	require.Equal(t, FreshnessWithinWindow, AssessFreshness(e).State)
	require.Equal(t, time.Minute, AssessFreshness(e).EvidenceAge)
	e.PreviousUsableAt = e.ObservedAt.Add(-90 * time.Second)
	e.ContinuousSince = e.ObservedAt.Add(-3 * time.Minute)
	require.Equal(t, FreshnessStalled, AssessFreshness(e).State)
}

// TestHealthProgressUsesRetainedClock pins resets, high-water comparisons, and
// unrelated field changes that must never reset freshness age.
func TestHealthProgressUsesRetainedClock(t *testing.T) {
	baseline := healthFacts("2h", 65)
	for _, tc := range []struct {
		name     string
		observed FixtureFacts
		want     bool
	}{
		{"same", healthFacts("2h", 65), false},
		{"advance", healthFacts("2h", 66), true},
		{"rollback", healthFacts("2h", 64), false},
		{"forward phase", healthFacts("ft", 90), true},
		{"backward phase", healthFacts("1h", 30), false},
		{"unknown phase", healthFacts("new", 66), false},
		{"negative extra", FixtureFacts{FixtureID: 1570385, Status: "2h", Elapsed: intp(67), Extra: intp(-1)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, ClockProgressed(baseline, tc.observed)) })
	}
	changed := baseline
	changed.HomeScore, changed.HomeName, changed.HasEvents = intp(5), "New name", true
	require.False(t, ClockProgressed(baseline, changed))
	changed.FixtureID++
	changed.Elapsed = intp(70)
	require.False(t, ClockProgressed(baseline, changed))
	baseline = healthFacts("ht", 45)
	baseline.Extra = intp(7)
	require.True(t, ClockProgressed(baseline, healthFacts("2h", 46)))
	require.True(t, ClockProgressed(healthFacts("et", 105), healthFacts("bt", 105)))
	require.True(t, ClockProgressed(healthFacts("bt", 105), healthFacts("et", 106)))
	// A rollback followed by a return to the retained maximum is not progress.
	require.False(t, ClockProgressed(healthFacts("2h", 66), healthFacts("2h", 65)))
	require.False(t, ClockProgressed(healthFacts("2h", 66), healthFacts("2h", 66)))
}

// TestHealthIndependentRestrictions ensures freshness recovery cannot clear a
// missing-event restriction or an identity rejection.
func TestHealthIndependentRestrictions(t *testing.T) {
	require.Equal(t, PolicyTrusted, RestrictPolicies())
	require.Equal(t, PolicyPositiveOnly, RestrictPolicies(PolicyTrusted, PolicyPositiveOnly, PolicyTrusted))
	require.Equal(t, PolicyRejected, RestrictPolicies(PolicyPositiveOnly, PolicyRejected, PolicyTrusted))
	require.Equal(t, PolicyRejected, RestrictPolicies(MutationPolicy("invalid")))
}

// TestHealthEvidenceRoundTrip preserves the policy inputs across serialization;
// it does not stand in for the future database collector/restart tests.
func TestHealthEvidenceRoundTrip(t *testing.T) {
	evidence := healthEvidence(20 * time.Minute)
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	var restored FreshnessEvidence
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.Equal(t, AssessFreshness(evidence), AssessFreshness(restored))
	require.Equal(t, evidence, restored, "assessment must not mutate its input")
}

// healthFacts builds normalized snapshots without any wall-clock assumptions.
func healthFacts(phase string, minute int) FixtureFacts {
	return FixtureFacts{FixtureID: 1570385, Status: phase, Elapsed: intp(minute)}
}

// healthEvidence models a continuous sequence whose last clock advance is old.
func healthEvidence(age time.Duration) FreshnessEvidence {
	at := time.Date(2026, 9, 16, 21, 23, 0, 0, time.UTC)
	return FreshnessEvidence{
		Facts: healthFacts("2h", 65), Usable: true, ObservedAt: at,
		PreviousUsableAt: at.Add(-30 * time.Second),
		ContinuousSince:  at.Add(-age), ClockProgressAt: at.Add(-age),
	}
}
