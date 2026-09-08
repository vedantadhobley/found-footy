// postponed_regression_test.go pins the September 8 deferred-score evidence
// and keeps real regressions eligible for fixture and provider-wide warnings.
package providerintegrity

import "testing"

// TestProduction20260908_PostponedNullScoresRemainTrusted reproduces the
// retained zero baseline that previously generated an anomaly on every poll.
func TestProduction20260908_PostponedNullScoresRemainTrusted(t *testing.T) {
	comparison := cincinnatiPostponedComparison()
	for poll := range 3 {
		verdict := AssessFixture(comparison)
		if verdict.Policy != PolicyTrusted || len(verdict.Reasons) != 0 ||
			verdict.MissingConfirmedEvents != 0 || verdict.SupportedGoalCorrection || verdict.SupportedReplacement {
			t.Fatalf("poll %d verdict = %+v, want trusted without correction evidence", poll, verdict)
		}
	}
}

// TestAssessBatch_PostponedNullScoresDoNotAmplifyAnomalies proves that a
// benign deferred baseline cannot supply a second fixture for a global trip.
func TestAssessBatch_PostponedNullScoresDoNotAmplifyAnomalies(t *testing.T) {
	postponed := cincinnatiPostponedComparison()
	regression := baseComparison()
	regression.Stored.FixtureID, regression.Observed.FixtureID = 1490437, 1490437
	regression.Observed.HomeScore = nil

	verdict := AssessBatch([]FixtureComparison{postponed, regression})
	if verdict.Policy != PolicyTrusted || verdict.RegressedFixtures != 1 ||
		verdict.MissingConfirmedEvents != 0 || len(verdict.Reasons) != 0 || len(verdict.Fixtures) != 1 ||
		verdict.Fixtures[0].FixtureID != 1490437 ||
		!containsReason(verdict.Fixtures[0].Reasons, ReasonPopulatedFieldCleared) {
		t.Fatalf("verdict = %+v, want only the unrelated fixture quarantined", verdict)
	}

	// A second genuine score erasure still meets the existing global threshold.
	second := regression
	second.Stored.FixtureID, second.Observed.FixtureID = 2002, 2002
	verdict = AssessBatch([]FixtureComparison{postponed, regression, second})
	if verdict.Policy != PolicyPositiveOnly || verdict.RegressedFixtures != 2 ||
		!containsReason(verdict.Reasons, ReasonMultipleFixtureRegression) {
		t.Fatalf("verdict = %+v, want the two real regressions to trip globally", verdict)
	}
}

// TestAssessFixture_PostponedScoreExceptionIsNarrow prevents a status-only
// bypass from hiding played fixtures, event loss, or unrelated cleared fields.
func TestAssessFixture_PostponedScoreExceptionIsNarrow(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*FixtureComparison)
		want   Reason
	}{
		{"home goal total", func(c *FixtureComparison) { c.Stored.HomeScore = intp(1) }, ReasonPopulatedFieldCleared},
		{"away goal total", func(c *FixtureComparison) { c.Stored.AwayScore = intp(1) }, ReasonPopulatedFieldCleared},
		{"new nonzero score", func(c *FixtureComparison) { c.Observed.AwayScore = intp(1) }, ReasonPopulatedFieldCleared},
		{"cleared played clock", func(c *FixtureComparison) { c.Stored.Elapsed = intp(20) }, ReasonPopulatedFieldCleared},
		{"retained played clock", func(c *FixtureComparison) {
			c.Stored.Elapsed, c.Observed.Elapsed = intp(20), intp(20)
		}, ReasonPopulatedFieldCleared},
		{"zero clock is still evidence", func(c *FixtureComparison) { c.Stored.Elapsed = intp(0) }, ReasonPopulatedFieldCleared},
		{"new clock", func(c *FixtureComparison) { c.Observed.Elapsed = intp(1) }, ReasonPopulatedFieldCleared},
		{"stored extra", func(c *FixtureComparison) { c.Stored.Extra = intp(1) }, ReasonPopulatedFieldCleared},
		{"observed extra", func(c *FixtureComparison) { c.Observed.Extra = intp(1) }, ReasonPopulatedFieldCleared},
		{"stored event history", func(c *FixtureComparison) { c.Stored.HasEvents = true }, ReasonPopulatedFieldCleared},
		{"raw observed event", func(c *FixtureComparison) { c.Observed.HasEvents = true }, ReasonPopulatedFieldCleared},
		{"entering postponed", func(c *FixtureComparison) { c.Stored.Status = "ns" }, ReasonPopulatedFieldCleared},
		{"live to postponed", func(c *FixtureComparison) { c.Stored.Status = "1h" }, ReasonPopulatedFieldCleared},
		{"terminal to postponed", func(c *FixtureComparison) {
			c.Stored.Status, c.Stored.Terminal = "ft", true
		}, ReasonTerminalRegressed},
		{"resuming play", func(c *FixtureComparison) { c.Observed.Status = "1h" }, ReasonPopulatedFieldCleared},
		{"suspended is not postponed", func(c *FixtureComparison) {
			c.Stored.Status, c.Observed.Status = "susp", "susp"
		}, ReasonPopulatedFieldCleared},
		{"interrupted is not postponed", func(c *FixtureComparison) {
			c.Stored.Status, c.Observed.Status = "int", "int"
		}, ReasonPopulatedFieldCleared},
		{"cleared home name", func(c *FixtureComparison) { c.Observed.HomeName = "" }, ReasonPopulatedFieldCleared},
		{"cleared away name", func(c *FixtureComparison) { c.Observed.AwayName = "" }, ReasonPopulatedFieldCleared},
		{"cleared league name", func(c *FixtureComparison) { c.Observed.LeagueName = "" }, ReasonPopulatedFieldCleared},
		{"identity changed", func(c *FixtureComparison) { c.Observed.HomeID++ }, ReasonFixtureIdentityChanged},
		{"missing confirmed goal", func(c *FixtureComparison) {
			c.ConfirmedEvents = []EventFact{{Key: "goal", TeamID: 2242, Type: "goal", Minute: 10, DebounceCount: 3}}
		}, ReasonConfirmedEventsMissing},
		{"missing confirmed card", func(c *FixtureComparison) {
			c.ConfirmedEvents = []EventFact{{Key: "card", TeamID: 2242, Type: "card", Minute: 10, DebounceCount: 3}}
		}, ReasonConfirmedEventsMissing},
		{"new trackable event", func(c *FixtureComparison) {
			c.ObservedEvents = []EventFact{{Key: "penalty", TeamID: 2242, Type: "penalty_miss", Minute: 10}}
		}, ReasonPopulatedFieldCleared},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comparison := cincinnatiPostponedComparison()
			tt.mutate(&comparison)
			verdict := AssessFixture(comparison)
			if !verdict.Anomalous() || !containsReason(verdict.Reasons, tt.want) {
				t.Fatalf("verdict = %+v, want anomaly %q", verdict, tt.want)
			}
		})
	}
}

// TestAssessFixture_PostponedZeroNullCombinations covers partial scoreboard
// population without treating nil as zero outside the narrow deferred case.
func TestAssessFixture_PostponedZeroNullCombinations(t *testing.T) {
	values := []*int{nil, intp(0)}
	for _, storedHome := range values {
		for _, storedAway := range values {
			for _, observedHome := range values {
				for _, observedAway := range values {
					comparison := cincinnatiPostponedComparison()
					comparison.Stored.HomeScore, comparison.Stored.AwayScore = storedHome, storedAway
					comparison.Observed.HomeScore, comparison.Observed.AwayScore = observedHome, observedAway
					if verdict := AssessFixture(comparison); verdict.Anomalous() {
						t.Fatalf("comparison = %+v, verdict = %+v, want trusted zero/null scoreboard", comparison, verdict)
					}
				}
			}
		}
	}
}

// cincinnatiPostponedComparison captures the September 8 13:47 UTC activity
// input; SQL retains 0–0 while API-Football repeatedly reports null/null.
func cincinnatiPostponedComparison() FixtureComparison {
	stored := FixtureFacts{
		FixtureID: 1490439, HomeID: 2242, AwayID: 1615, LeagueID: 253,
		HomeName: "FC Cincinnati", AwayName: "DC United", LeagueName: "MLS",
		Status: "pst", HomeScore: intp(0), AwayScore: intp(0),
	}
	observed := stored
	observed.HomeScore, observed.AwayScore = nil, nil
	return FixtureComparison{Stored: stored, Observed: observed}
}
