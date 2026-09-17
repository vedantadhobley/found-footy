// health_recovery_test.go prevents HTTP success, replay, or recovery of one
// dimension from silently resolving a different provider-health failure.
package providerintegrity

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHealthFreshnessRecoveryRequiresProgress excludes frozen HTTP-200 probes.
func TestHealthFreshnessRecoveryRequiresProgress(t *testing.T) {
	baseline := healthFacts("2h", 65)
	frozen := fixtureProbes("2h", 65, 65, 65, 65)
	require.False(t, FreshnessRecovered(baseline, frozen))
	require.False(t, FreshnessRecovered(baseline, fixtureProbes("2h", 66, 66, 66)))
	require.True(t, FreshnessRecovered(baseline, fixtureProbes("2h", 66, 66, 67)))
	probes := fixtureProbes("2h", 66, 67)
	for i := 0; i < 6; i++ {
		probe := probes[len(probes)-1]
		probe.ID = fmt.Sprintf("frozen-%d", i)
		probe.At = probe.At.Add(30 * time.Second)
		probes = append(probes, probe)
	}
	require.False(t, FreshnessRecovered(baseline, probes), "old progress cannot prove current recovery")
	require.True(t, FreshnessRecovered(baseline, fixtureProbes("2h", 66, 66, 66, 66, 67, 67, 67, 67, 68)),
		"use the latest two advances, not a window permanently anchored to the first")
	probes = fixtureProbes("2h", 66, 66, 66, 66, 66, 66, 67)
	require.True(t, FreshnessRecovered(baseline, probes), "two advances exactly three minutes apart qualify")
	probes[len(probes)-1].At = probes[len(probes)-1].At.Add(time.Second)
	require.False(t, FreshnessRecovered(baseline, probes), "advances beyond the window restart confirmation")
}

// TestHealthFreshnessRecoveryPhasePath supports recovery directly into FT/HT.
func TestHealthFreshnessRecoveryPhasePath(t *testing.T) {
	for _, tc := range []struct {
		before, after string
		minute        int
	}{
		{"2h", "ft", 90}, {"1h", "ht", 45}, {"et", "bt", 105}, {"bt", "et", 106},
	} {
		t.Run(tc.before+"-"+tc.after, func(t *testing.T) {
			baseline := healthFacts(tc.before, tc.minute-1)
			probes := fixtureProbes(tc.after, tc.minute, tc.minute, tc.minute)
			require.False(t, FreshnessRecovered(baseline, probes[:2]))
			require.True(t, FreshnessRecovered(baseline, probes))
		})
	}
	probes := fixtureProbes("2h", 46, 46, 46, 46, 46, 46, 46)
	require.False(t, FreshnessRecovered(healthFacts("ht", 45), probes), "a phase transition cannot excuse a later freeze")
	probes = fixtureProbes("2h", 46, 46, 46)
	probes[1].Facts.Elapsed = nil
	require.False(t, FreshnessRecovered(healthFacts("ht", 45), probes), "missing running clock interrupts recovery")
	probes = fixtureProbes("ft", 90, 90, 90, 90, 90)
	probes[1].Usable = false
	require.True(t, FreshnessRecovered(healthFacts("2h", 65), probes), "a later clean terminal streak can recover after a failure")
}

// TestHealthFreshnessRecoveryRejectsBrokenEvidence pins gaps and new failures.
func TestHealthFreshnessRecoveryRejectsBrokenEvidence(t *testing.T) {
	baseline := healthFacts("2h", 65)
	for _, tc := range []struct {
		name   string
		mutate func([]FixtureHealthProbe)
	}{
		{"failed response", func(p []FixtureHealthProbe) { p[1].Usable = false }},
		{"wrong fixture", func(p []FixtureHealthProbe) { p[1].Facts.FixtureID++ }},
		{"clock regression", func(p []FixtureHealthProbe) { p[1].Facts.Elapsed = intp(40) }},
		{"different phase", func(p []FixtureHealthProbe) { p[1].Facts.Status = "1h" }},
		{"invalid receipt", func(p []FixtureHealthProbe) { p[1].ID = "" }},
		{"missing receipt time", func(p []FixtureHealthProbe) { p[1].At = time.Time{} }},
		{"negative clock", func(p []FixtureHealthProbe) { p[1].Facts.Elapsed = intp(-1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probes := fixtureProbes("2h", 66, 66, 67)
			tc.mutate(probes)
			require.False(t, FreshnessRecovered(baseline, probes))
		})
	}
	probes := fixtureProbes("2h", 66, 67)
	probes[1].At = probes[0].At.Add(91 * time.Second)
	require.False(t, FreshnessRecovered(baseline, probes))
	probes[1].At = probes[0].At.Add(90 * time.Second)
	require.True(t, FreshnessRecovered(baseline, probes))
}

// TestHealthRecoveryCountsLogicalObservations excludes activity retries and late results.
func TestHealthRecoveryCountsLogicalObservations(t *testing.T) {
	baseline := healthFacts("2h", 65)
	probes := fixtureProbes("2h", 66, 67)
	probes[1].ID = probes[0].ID
	require.False(t, FreshnessRecovered(baseline, probes))
	probes[1].ID = "different"
	probes[1].At = probes[0].At
	require.False(t, FreshnessRecovered(baseline, probes))
	probes[1].At = probes[0].At.Add(-time.Second)
	require.False(t, FreshnessRecovered(baseline, probes))
	require.False(t, FreshnessRecovered(FixtureFacts{}, nil))
}

// TestHealthProviderRecoveryRequiresThreeCleanProbes preserves cause isolation.
func TestHealthProviderRecoveryRequiresThreeCleanProbes(t *testing.T) {
	probes := providerProbes(3)
	require.False(t, ProviderIntegrityRecovered(probes[:2]))
	require.True(t, ProviderIntegrityRecovered(probes))
	for _, tc := range []struct {
		name   string
		mutate func(*ProviderHealthProbe)
	}{
		{"partial response", func(p *ProviderHealthProbe) { p.Complete = false }},
		{"new systemic failure", func(p *ProviderHealthProbe) { p.NewSystemicAnomaly = true }},
		{"new affected fixture", func(p *ProviderHealthProbe) { p.NewAffectedFixture = true }},
		{"invalid receipt", func(p *ProviderHealthProbe) { p.ID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := providerProbes(4)
			tc.mutate(&p[1])
			require.False(t, ProviderIntegrityRecovered(p))
		})
	}
	probes[1].ID = probes[0].ID
	require.False(t, ProviderIntegrityRecovered(probes))
	probes = providerProbes(3)
	probes[2].At = probes[1].At.Add(91 * time.Second)
	require.False(t, ProviderIntegrityRecovered(probes))
	probes = providerProbes(4)
	probes[0].Complete = false
	require.True(t, ProviderIntegrityRecovered(probes), "three later clean observations can recover")
	// Broader recovery is not permission to clear a fixture's missing-event cause.
	require.Equal(t, PolicyPositiveOnly, RestrictPolicies(PolicyTrusted, PolicyPositiveOnly))
}

// fixtureProbes creates immutable scheduled receipts at the normal cadence.
func fixtureProbes(phase string, minutes ...int) []FixtureHealthProbe {
	start := healthEvidence(0).ObservedAt
	probes := make([]FixtureHealthProbe, len(minutes))
	for i, minute := range minutes {
		probes[i] = FixtureHealthProbe{
			ID: fmt.Sprintf("poll-%d", i), At: start.Add(time.Duration(i) * 30 * time.Second),
			Usable: true, Facts: healthFacts(phase, minute),
		}
	}
	return probes
}

// providerProbes supplies complete independent coverage without new anomalies.
func providerProbes(count int) []ProviderHealthProbe {
	start := healthEvidence(0).ObservedAt
	probes := make([]ProviderHealthProbe, count)
	for i := range probes {
		probes[i] = ProviderHealthProbe{ID: fmt.Sprintf("poll-%d", i), At: start.Add(time.Duration(i) * 30 * time.Second), Complete: true}
	}
	return probes
}
