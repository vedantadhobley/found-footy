// production_regression_test.go preserves the API-Football observations that
// classified the first FF-075 production shadow window on 2026-08-30.
package providerintegrity

import "testing"

func TestProduction20260830_SystemicEmptyEventsTripsGlobalPolicy(t *testing.T) {
	cases := []struct {
		fixtureID int64
		missing   int
	}{
		{fixtureID: 1_552_743, missing: 2},
		{fixtureID: 1_557_379, missing: 5},
		{fixtureID: 1_557_382, missing: 1},
		{fixtureID: 1_575_145, missing: 2},
	}
	comparisons := make([]FixtureComparison, 0, len(cases))
	for _, tc := range cases {
		comparison := baseComparison()
		comparison.Stored.FixtureID = tc.fixtureID
		comparison.Observed.FixtureID = tc.fixtureID
		comparison.ConfirmedEvents = nil
		comparison.ObservedEvents = nil
		for index := range tc.missing {
			comparison.ConfirmedEvents = append(comparison.ConfirmedEvents, EventFact{
				Key: "missing_" + string(rune('a'+index)), TeamID: 40,
				PlayerID: intp(100 + index), Type: "goal", Detail: "normal goal",
				Minute: 20 + index, DebounceCount: 3,
			})
		}
		comparisons = append(comparisons, comparison)
	}

	verdict := AssessBatch(comparisons)
	if verdict.Policy != PolicyPositiveOnly || verdict.RegressedFixtures != 4 ||
		verdict.MissingConfirmedEvents != 10 ||
		!containsReason(verdict.Reasons, ReasonMultipleFixtureRegression) ||
		!containsReason(verdict.Reasons, ReasonMultipleEventDisappearance) {
		t.Fatalf("verdict = %+v, want four fixtures and ten missing events", verdict)
	}
}

func TestProduction20260830_HalftimeExtraClearingIsForwardProgress(t *testing.T) {
	cases := []struct {
		fixtureID int64
		extra     int
	}{
		{fixtureID: 1_557_382, extra: 4},
		{fixtureID: 1_570_360, extra: 4},
		{fixtureID: 1_550_105, extra: 6},
		{fixtureID: 1_570_356, extra: 4},
		{fixtureID: 1_550_102, extra: 5},
		{fixtureID: 1_490_435, extra: 4},
	}
	for _, tc := range cases {
		comparison := baseComparison()
		comparison.Stored.FixtureID = tc.fixtureID
		comparison.Observed.FixtureID = tc.fixtureID
		comparison.Stored.Status = "ht"
		comparison.Stored.Elapsed = intp(45)
		comparison.Stored.Extra = intp(tc.extra)
		comparison.Observed.Status = "2h"
		comparison.Observed.Elapsed = intp(46)
		comparison.Observed.Extra = nil

		verdict := AssessFixture(comparison)
		if verdict.Policy != PolicyTrusted {
			t.Fatalf("fixture %d verdict = %+v, want trusted", tc.fixtureID, verdict)
		}
	}
}

func TestProduction20260830_SamePhaseClockCollapseIsQuarantined(t *testing.T) {
	comparison := baseComparison()
	comparison.Stored.FixtureID, comparison.Observed.FixtureID = 1_552_744, 1_552_744
	comparison.Stored.Status = "2h"
	comparison.Stored.Elapsed = intp(90)
	comparison.Stored.Extra = intp(15)
	comparison.Observed.Status = "2h"
	comparison.Observed.Elapsed = intp(46)
	comparison.Observed.Extra = nil

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyPositiveOnly || !containsReason(verdict.Reasons, ReasonClockRegressed) {
		t.Fatalf("verdict = %+v, want Rennes clock collapse quarantined", verdict)
	}
}

func TestProduction20260830_ScoreDropWithGoalStillPresentIsQuarantined(t *testing.T) {
	comparison := baseComparison()
	comparison.Stored.FixtureID, comparison.Observed.FixtureID = 1_575_141, 1_575_141
	comparison.Stored.Status = "1h"
	comparison.Observed.Status = "1h"
	comparison.Stored.Elapsed = intp(10)
	comparison.Observed.Elapsed = intp(10)
	comparison.Observed.HomeScore = intp(0)

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyPositiveOnly || !containsReason(verdict.Reasons, ReasonScoreDecreased) {
		t.Fatalf("verdict = %+v, want internally inconsistent score quarantined", verdict)
	}
}

func TestProduction20260830_LiveToNotStartedRegressionIsQuarantined(t *testing.T) {
	comparison := baseComparison()
	comparison.Stored.FixtureID, comparison.Observed.FixtureID = 1_490_435, 1_490_435
	comparison.Stored.Status = "1h"
	comparison.Stored.Elapsed = intp(20)
	comparison.Stored.HomeScore = intp(1)
	comparison.Stored.AwayScore = intp(1)
	comparison.Observed = comparison.Stored
	comparison.Observed.Status = "ns"
	comparison.Observed.HomeScore = nil
	comparison.Observed.AwayScore = nil

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyPositiveOnly ||
		!containsReason(verdict.Reasons, ReasonPhaseRegressed) ||
		!containsReason(verdict.Reasons, ReasonPopulatedFieldCleared) {
		t.Fatalf("verdict = %+v, want phase and populated-field regression", verdict)
	}
}

func TestProduction20260830_ScorerAttributionReplacementIsTrusted(t *testing.T) {
	storedOwnGoal := EventFact{
		Key: "544_315699_goal_1", TeamID: 544, PlayerID: intp(315699),
		Type: "goal", Detail: "own goal", Minute: 13, DebounceCount: 3,
	}
	aubameyang := EventFact{
		Key: "544_1465_goal_1", TeamID: 544, PlayerID: intp(1465),
		Type: "goal", Detail: "normal goal", Minute: 17, DebounceCount: 3,
	}
	replacement := EventFact{
		Key: "544_333672_goal_1", TeamID: 544, PlayerID: intp(333672),
		Type: "goal", Detail: "own goal", Minute: 13,
	}
	comparison := FixtureComparison{
		Stored: FixtureFacts{
			FixtureID: 1_570_356, HomeID: 544, AwayID: 532, LeagueID: 140,
			HomeName: "Deportivo", AwayName: "Valencia", LeagueName: "La Liga",
			Status: "1h", Elapsed: intp(22), HomeScore: intp(2), AwayScore: intp(0),
		},
		ConfirmedEvents: []EventFact{storedOwnGoal, aubameyang},
		ObservedEvents:  []EventFact{replacement, aubameyang},
	}
	comparison.Observed = comparison.Stored

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyTrusted || !verdict.SupportedReplacement {
		t.Fatalf("verdict = %+v, want scorer attribution replacement trusted", verdict)
	}
}

func TestProduction20260830_GoalCancellationRemainsTrustedAcrossDebounce(t *testing.T) {
	confirmed := []EventFact{
		{Key: "1613_47440_goal_1", TeamID: 1613, PlayerID: intp(47440), Type: "goal", Detail: "normal goal", Minute: 8, DebounceCount: 3},
		{Key: "1609_1757_goal_1", TeamID: 1609, PlayerID: intp(1757), Type: "goal", Detail: "normal goal", Minute: 32, DebounceCount: 3},
		{Key: "1609_269532_goal_1", TeamID: 1609, PlayerID: intp(269532), Type: "goal", Detail: "normal goal", Minute: 41, DebounceCount: 3},
		{Key: "1609_269532_goal_2", TeamID: 1609, PlayerID: intp(269532), Type: "goal", Detail: "normal goal", Minute: 74, DebounceCount: 2},
	}
	comparison := FixtureComparison{
		Stored: FixtureFacts{
			FixtureID: 1_490_427, HomeID: 1613, AwayID: 1609, LeagueID: 253,
			HomeName: "Columbus", AwayName: "New England", LeagueName: "MLS",
			Status: "2h", Elapsed: intp(78), HomeScore: intp(1), AwayScore: intp(2),
		},
		ConfirmedEvents: confirmed,
		ObservedEvents:  confirmed[:3],
	}
	comparison.Observed = comparison.Stored

	verdict := AssessFixture(comparison)
	if verdict.Policy != PolicyTrusted || !verdict.SupportedGoalCorrection {
		t.Fatalf("verdict = %+v, want cancellation continuation trusted", verdict)
	}
}
