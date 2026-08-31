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

func TestAssessFixture_SupportedGoalCorrectionContinuesThroughAbsenceDebounce(t *testing.T) {
	comparison := baseComparison()
	comparison.Stored.Elapsed = intp(42)
	comparison.Observed.Elapsed = intp(43)
	comparison.ConfirmedEvents = append(comparison.ConfirmedEvents, EventFact{
		Key:           "40_12_goal_1",
		TeamID:        40,
		PlayerID:      intp(12),
		Type:          "goal",
		Detail:        "normal goal",
		Minute:        40,
		DebounceCount: 2,
	})

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyTrusted || !verdict.SupportedGoalCorrection {
		t.Fatalf("verdict = %+v, want trusted continuation of supported correction", verdict)
	}
}

func TestAssessFixture_ScorerReplacementIsTrusted(t *testing.T) {
	comparison := baseComparison()
	comparison.ObservedEvents = []EventFact{{
		Key:      "40_11_goal_1",
		TeamID:   40,
		PlayerID: intp(11),
		Type:     "goal",
		Detail:   "normal goal",
		Minute:   30,
	}}

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyTrusted || !verdict.SupportedReplacement ||
		verdict.MissingConfirmedEvents != 1 {
		t.Fatalf("verdict = %+v, want trusted one-for-one scorer replacement", verdict)
	}
}

func TestAssessFixture_UnrelatedNewEventDoesNotAuthorizeDisappearance(t *testing.T) {
	comparison := baseComparison()
	comparison.ObservedEvents = []EventFact{{
		Key:      "40_11_goal_1",
		TeamID:   40,
		PlayerID: intp(11),
		Type:     "goal",
		Detail:   "normal goal",
		Minute:   60,
	}}

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyPositiveOnly || verdict.SupportedReplacement {
		t.Fatalf("verdict = %+v, want unrelated replacement quarantined", verdict)
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
		name       string
		mutate     func(*FixtureComparison)
		want       Reason
		wantPolicy MutationPolicy
	}{
		{
			name: "identity",
			mutate: func(comparison *FixtureComparison) {
				comparison.Observed.AwayID = 99
			},
			want:       ReasonFixtureIdentityChanged,
			wantPolicy: PolicyRejected,
		},
		{
			name: "phase",
			mutate: func(comparison *FixtureComparison) {
				comparison.Stored.Status = "2h"
				comparison.Observed.Status = "ht"
			},
			want:       ReasonPhaseRegressed,
			wantPolicy: PolicyPositiveOnly,
		},
		{
			name: "terminal",
			mutate: func(comparison *FixtureComparison) {
				comparison.Stored.Status = "ft"
				comparison.Stored.Terminal = true
				comparison.Observed.Status = "2h"
				comparison.Observed.Terminal = false
			},
			want:       ReasonTerminalRegressed,
			wantPolicy: PolicyPositiveOnly,
		},
		{
			name: "clock",
			mutate: func(comparison *FixtureComparison) {
				comparison.Stored.Elapsed = intp(61)
				comparison.Observed.Elapsed = intp(50)
			},
			want:       ReasonClockRegressed,
			wantPolicy: PolicyPositiveOnly,
		},
		{
			name: "score",
			mutate: func(comparison *FixtureComparison) {
				comparison.Observed.HomeScore = intp(0)
			},
			want:       ReasonScoreDecreased,
			wantPolicy: PolicyPositiveOnly,
		},
		{
			name: "cleared",
			mutate: func(comparison *FixtureComparison) {
				comparison.Observed.HomeScore = nil
			},
			want:       ReasonPopulatedFieldCleared,
			wantPolicy: PolicyPositiveOnly,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comparison := baseComparison()
			tt.mutate(&comparison)
			verdict := AssessFixture(comparison)
			if verdict.Policy != tt.wantPolicy || !containsReason(verdict.Reasons, tt.want) {
				t.Fatalf("verdict = %+v, want reason %q", verdict, tt.want)
			}
		})
	}
}

func TestAssessFixture_PeriodBoundaryClockResetIsTrusted(t *testing.T) {
	tests := []struct {
		name            string
		storedStatus    string
		storedElapsed   int
		storedExtra     int
		observedStatus  string
		observedElapsed int
	}{
		{
			name:         "halftime to second half",
			storedStatus: "ht", storedElapsed: 45, storedExtra: 4,
			observedStatus: "2h", observedElapsed: 46,
		},
		{
			name:         "extra-time break to next period",
			storedStatus: "bt", storedElapsed: 105, storedExtra: 2,
			observedStatus: "et", observedElapsed: 106,
		},
		{
			name:         "extra-time period to break",
			storedStatus: "et", storedElapsed: 105, storedExtra: 2,
			observedStatus: "bt", observedElapsed: 105,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comparison := baseComparison()
			comparison.Stored.Status = tt.storedStatus
			comparison.Stored.Elapsed = intp(tt.storedElapsed)
			comparison.Stored.Extra = intp(tt.storedExtra)
			comparison.Observed.Status = tt.observedStatus
			comparison.Observed.Elapsed = intp(tt.observedElapsed)
			comparison.Observed.Extra = nil

			verdict := AssessFixture(comparison)
			if verdict.Policy != PolicyTrusted || containsReason(verdict.Reasons, ReasonClockRegressed) {
				t.Fatalf("verdict = %+v, want trusted period boundary", verdict)
			}
		})
	}
}

func TestAssessFixture_SamePhaseClockRollbackRemainsQuarantined(t *testing.T) {
	comparison := baseComparison()
	comparison.Stored.Status = "2h"
	comparison.Stored.Elapsed = intp(90)
	comparison.Stored.Extra = intp(15)
	comparison.Observed.Status = "2h"
	comparison.Observed.Elapsed = intp(46)
	comparison.Observed.Extra = nil

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyPositiveOnly || !containsReason(verdict.Reasons, ReasonClockRegressed) {
		t.Fatalf("verdict = %+v, want same-phase rollback quarantined", verdict)
	}
}

func TestAggregateFixtureVerdicts_IsolatedRejectedFixtureDoesNotRejectBatch(t *testing.T) {
	verdict := FixtureVerdict{
		FixtureID: 100,
		Policy:    PolicyRejected,
		Reasons:   []Reason{ReasonFixtureIdentityChanged},
	}

	batch := AggregateFixtureVerdicts([]FixtureVerdict{verdict})
	if batch.Policy != PolicyTrusted || len(batch.Fixtures) != 1 ||
		batch.Fixtures[0].Policy != PolicyRejected {
		t.Fatalf("batch = %+v, want isolated rejected fixture with trusted global policy", batch)
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
	goal := EventFact{
		Key: "40_10_goal_1", TeamID: 40, PlayerID: intp(10), Type: "goal",
		Detail: "normal goal", Minute: 30, DebounceCount: 3,
	}
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
