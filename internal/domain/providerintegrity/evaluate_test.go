// evaluate_test.go pins normal progression, supported correction, isolated
// quarantine, and systemic provider-regression classifications.
package providerintegrity

import "testing"

func TestAssessFixture_NormalProgressionIsTrusted(t *testing.T) {
	comparison := baseComparison()
	comparison.Observed.Elapsed = intp(61)
	comparison.Observed.HomeScore = intp(2)
	comparison.ObservedEvents = append(comparison.ObservedEvents, EventFact{
		Key: "40_11_goal_1", TeamID: 40, Type: "goal", Minute: 60,
	})

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyTrusted || verdict.Anomalous() {
		t.Fatalf("verdict = %+v, want trusted", verdict)
	}
}

func TestAssessFixture_SupportedRecentGoalCorrectionIsTrusted(t *testing.T) {
	comparison := baseComparison()
	comparison.Stored.HomeScore = intp(2)
	comparison.Observed.HomeScore = intp(1)
	comparison.Stored.Elapsed = intp(42)
	comparison.Observed.Elapsed = intp(43)
	removed := EventFact{Key: "40_12_goal_1", TeamID: 40, Type: "goal", Minute: 40}
	comparison.ConfirmedEvents = append(comparison.ConfirmedEvents, removed)

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyTrusted || !verdict.SupportedGoalCorrection {
		t.Fatalf("verdict = %+v, want supported trusted correction", verdict)
	}
}

func TestAssessFixture_UnsupportedEventDisappearanceQuarantinesOnlyFixture(t *testing.T) {
	comparison := baseComparison()
	comparison.ConfirmedEvents = append(comparison.ConfirmedEvents, EventFact{
		Key: "42_91_card_1", TeamID: 42, Type: "card", Minute: 50,
	})

	fixtureVerdict := AssessFixture(comparison)
	if fixtureVerdict.Policy != PolicyPositiveOnly || fixtureVerdict.MissingConfirmedEvents != 1 {
		t.Fatalf("fixture verdict = %+v, want one positive-only disappearance", fixtureVerdict)
	}
	batch := AggregateFixtureVerdicts([]FixtureVerdict{fixtureVerdict})
	if batch.Policy != PolicyTrusted || len(batch.Fixtures) != 1 {
		t.Fatalf("batch = %+v, want globally trusted with one quarantine", batch)
	}
}

func TestAssessFixture_MinuteCorrectionStillMatchesConfirmedEvent(t *testing.T) {
	comparison := baseComparison()
	comparison.ConfirmedEvents[0].Minute = 30
	comparison.ObservedEvents[0].Minute = 34

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyTrusted || verdict.MissingConfirmedEvents != 0 {
		t.Fatalf("verdict = %+v, want clock-tolerant event match", verdict)
	}
}

func TestAssessFixture_StaleGoalCorrectionIsNotAutomaticallyTrusted(t *testing.T) {
	comparison := baseComparison()
	comparison.Stored.HomeScore = intp(2)
	comparison.Observed.HomeScore = intp(1)
	comparison.Stored.Elapsed = intp(70)
	comparison.Observed.Elapsed = intp(71)
	comparison.ConfirmedEvents = append(comparison.ConfirmedEvents, EventFact{
		Key: "40_12_goal_1", TeamID: 40, Type: "goal", Minute: 40,
	})

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyPositiveOnly || verdict.SupportedGoalCorrection {
		t.Fatalf("verdict = %+v, want old correction quarantined", verdict)
	}
}

func TestAssessFixture_SemanticRegressionsAreTyped(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*FixtureComparison)
		want   Reason
	}{
		{
			name: "identity",
			mutate: func(comparison *FixtureComparison) {
				comparison.Observed.AwayID = 99
			},
			want: ReasonFixtureIdentityChanged,
		},
		{
			name: "phase",
			mutate: func(comparison *FixtureComparison) {
				comparison.Stored.Status = "2h"
				comparison.Observed.Status = "ht"
			},
			want: ReasonPhaseRegressed,
		},
		{
			name: "terminal",
			mutate: func(comparison *FixtureComparison) {
				comparison.Stored.Status = "ft"
				comparison.Stored.Terminal = true
				comparison.Observed.Status = "2h"
				comparison.Observed.Terminal = false
			},
			want: ReasonTerminalRegressed,
		},
		{
			name: "clock",
			mutate: func(comparison *FixtureComparison) {
				comparison.Stored.Elapsed = intp(61)
				comparison.Observed.Elapsed = intp(50)
			},
			want: ReasonClockRegressed,
		},
		{
			name: "score",
			mutate: func(comparison *FixtureComparison) {
				comparison.Observed.HomeScore = intp(0)
			},
			want: ReasonScoreDecreased,
		},
		{
			name: "cleared",
			mutate: func(comparison *FixtureComparison) {
				comparison.Observed.HomeScore = nil
			},
			want: ReasonPopulatedFieldCleared,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comparison := baseComparison()
			tt.mutate(&comparison)
			verdict := AssessFixture(comparison)
			if verdict.Policy != PolicyPositiveOnly || !containsReason(verdict.Reasons, tt.want) {
				t.Fatalf("verdict = %+v, want reason %q", verdict, tt.want)
			}
		})
	}
}

func TestAssessBatch_ProductionRegressionTripsGlobalShadowPolicy(t *testing.T) {
	first := baseComparison()
	first.Stored.FixtureID, first.Observed.FixtureID = 1001, 1001
	first.ConfirmedEvents = append(first.ConfirmedEvents,
		EventFact{Key: "40_12_goal_1", TeamID: 40, Type: "goal", Minute: 40},
		EventFact{Key: "42_13_card_1", TeamID: 42, Type: "card", Minute: 45},
	)
	first.ObservedEvents = nil
	first.Observed.HomeScore = intp(0)

	second := baseComparison()
	second.Stored.FixtureID, second.Observed.FixtureID = 2002, 2002
	second.ConfirmedEvents = append(second.ConfirmedEvents,
		EventFact{Key: "42_14_goal_1", TeamID: 42, Type: "goal", Minute: 55},
	)
	second.ObservedEvents = nil
	second.Observed.HomeScore = intp(0)

	verdict := AssessBatch([]FixtureComparison{first, second})
	if verdict.Policy != PolicyPositiveOnly || verdict.RegressedFixtures != 2 ||
		verdict.MissingConfirmedEvents < 3 {
		t.Fatalf("batch verdict = %+v, want systemic positive-only", verdict)
	}
	if !containsReason(verdict.Reasons, ReasonMultipleFixtureRegression) ||
		!containsReason(verdict.Reasons, ReasonMultipleEventDisappearance) {
		t.Fatalf("batch reasons = %v, want both global trip reasons", verdict.Reasons)
	}
}

func baseComparison() FixtureComparison {
	stored := FixtureFacts{
		FixtureID:  100,
		HomeID:     40,
		AwayID:     42,
		LeagueID:   39,
		HomeName:   "Home",
		AwayName:   "Away",
		LeagueName: "League",
		Status:     "2h",
		Elapsed:    intp(60),
		HomeScore:  intp(1),
		AwayScore:  intp(0),
	}
	observed := stored
	goal := EventFact{Key: "40_10_goal_1", TeamID: 40, Type: "goal", Minute: 30}
	return FixtureComparison{
		Stored:          stored,
		Observed:        observed,
		ConfirmedEvents: []EventFact{goal},
		ObservedEvents:  []EventFact{goal},
	}
}

func containsReason(reasons []Reason, want Reason) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func intp(value int) *int { return &value }
