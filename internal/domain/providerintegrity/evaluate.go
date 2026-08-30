// evaluate.go implements deterministic fixture and batch integrity rules with
// no database, transport, Temporal, logging, or metric dependencies.
package providerintegrity

const (
	materialClockRollbackMinutes = 2
	goalCorrectionWindowMinutes  = 10
	globalFixtureTripCount       = 2
	globalMissingEventTripCount  = 3
)

// AssessBatch evaluates every fixture from its pre-write stored snapshot and
// derives the provider-wide shadow policy from correlated anomalies.
func AssessBatch(comparisons []FixtureComparison) BatchVerdict {
	verdicts := make([]FixtureVerdict, 0, len(comparisons))
	for _, comparison := range comparisons {
		verdicts = append(verdicts, AssessFixture(comparison))
	}
	return AggregateFixtureVerdicts(verdicts)
}

// AssessFixture compares one fresh observation with the last stored state.
// The returned policy is advisory until FF-075's enforcement phase ships.
func AssessFixture(comparison FixtureComparison) FixtureVerdict {
	stored := comparison.Stored
	observed := comparison.Observed
	verdict := FixtureVerdict{
		FixtureID: stored.FixtureID,
		Policy:    PolicyTrusted,
	}

	identityChanged := stored.FixtureID != observed.FixtureID ||
		stored.HomeID != observed.HomeID || stored.AwayID != observed.AwayID ||
		stored.LeagueID != observed.LeagueID
	phaseRegressed := providerPhaseRegressed(stored.Status, observed.Status)
	terminalRegressed := stored.Terminal && !observed.Terminal
	clockRegressed := materialClockRegressed(stored, observed)
	populatedFieldCleared := providerFieldCleared(stored, observed)
	missing := missingConfirmedEvents(comparison.ConfirmedEvents, comparison.ObservedEvents)
	scoreDecreased := providerScoreDecreased(stored, observed)

	if identityChanged {
		verdict.Reasons = append(verdict.Reasons, ReasonFixtureIdentityChanged)
	}
	if phaseRegressed {
		verdict.Reasons = append(verdict.Reasons, ReasonPhaseRegressed)
	}
	if terminalRegressed {
		verdict.Reasons = append(verdict.Reasons, ReasonTerminalRegressed)
	}
	if clockRegressed {
		verdict.Reasons = append(verdict.Reasons, ReasonClockRegressed)
	}
	if populatedFieldCleared {
		verdict.Reasons = append(verdict.Reasons, ReasonPopulatedFieldCleared)
	}

	verdict.MissingConfirmedEvents = len(missing)
	verdict.SupportedGoalCorrection = supportedGoalCorrection(
		comparison, missing, identityChanged || phaseRegressed || terminalRegressed ||
			clockRegressed || populatedFieldCleared,
	)
	if scoreDecreased && !verdict.SupportedGoalCorrection {
		verdict.Reasons = append(verdict.Reasons, ReasonScoreDecreased)
	}
	if len(missing) > 0 && !verdict.SupportedGoalCorrection {
		verdict.Reasons = append(verdict.Reasons, ReasonConfirmedEventsMissing)
	}
	if len(verdict.Reasons) > 0 {
		verdict.Policy = PolicyPositiveOnly
	}
	return verdict
}

// AggregateFixtureVerdicts derives only the global circuit recommendation.
// A single positive-only fixture remains isolated in Fixtures while Policy
// stays trusted for unrelated matches.
func AggregateFixtureVerdicts(verdicts []FixtureVerdict) BatchVerdict {
	out := BatchVerdict{Policy: PolicyTrusted}
	for _, verdict := range verdicts {
		if !verdict.Policy.Valid() {
			continue
		}
		if verdict.Policy == PolicyRejected {
			out.Policy = PolicyRejected
		}
		if !verdict.Anomalous() {
			continue
		}
		out.Fixtures = append(out.Fixtures, verdict)
		out.RegressedFixtures++
		out.MissingConfirmedEvents += verdict.MissingConfirmedEvents
	}
	if out.Policy == PolicyRejected {
		return out
	}
	if out.RegressedFixtures >= globalFixtureTripCount {
		out.Policy = PolicyPositiveOnly
		out.Reasons = append(out.Reasons, ReasonMultipleFixtureRegression)
	}
	if out.MissingConfirmedEvents >= globalMissingEventTripCount {
		out.Policy = PolicyPositiveOnly
		out.Reasons = append(out.Reasons, ReasonMultipleEventDisappearance)
	}
	return out
}

func missingConfirmedEvents(stored, observed []EventFact) []EventFact {
	present := make(map[string]struct{}, len(observed))
	for _, providerEvent := range observed {
		present[providerEvent.Key] = struct{}{}
	}
	missing := make([]EventFact, 0)
	for _, providerEvent := range stored {
		if _, exists := present[providerEvent.Key]; !exists {
			missing = append(missing, providerEvent)
		}
	}
	return missing
}

func supportedGoalCorrection(comparison FixtureComparison, missing []EventFact, hasOtherRegression bool) bool {
	if hasOtherRegression || len(missing) != 1 || missing[0].Type != "goal" {
		return false
	}
	stored := comparison.Stored
	observed := comparison.Observed
	missingGoal := missing[0]
	if !scoreDroppedForTeamByOne(stored, observed, missingGoal.TeamID) ||
		!observedGoalInventoryMatchesScore(observed, comparison.ObservedEvents) {
		return false
	}
	if observed.Elapsed == nil {
		return false
	}
	distance := fixtureClock(observed) - eventClock(missingGoal.Minute, missingGoal.Extra)
	return distance >= 0 && distance <= goalCorrectionWindowMinutes
}

func scoreDroppedForTeamByOne(stored, observed FixtureFacts, teamID int) bool {
	if stored.HomeScore == nil || stored.AwayScore == nil ||
		observed.HomeScore == nil || observed.AwayScore == nil {
		return false
	}
	switch teamID {
	case stored.HomeID:
		return *stored.HomeScore == *observed.HomeScore+1 &&
			*stored.AwayScore == *observed.AwayScore
	case stored.AwayID:
		return *stored.AwayScore == *observed.AwayScore+1 &&
			*stored.HomeScore == *observed.HomeScore
	default:
		return false
	}
}

func observedGoalInventoryMatchesScore(observed FixtureFacts, events []EventFact) bool {
	if observed.HomeScore == nil || observed.AwayScore == nil {
		return false
	}
	goals := map[int]int{observed.HomeID: 0, observed.AwayID: 0}
	for _, providerEvent := range events {
		if providerEvent.Type == "goal" {
			goals[providerEvent.TeamID]++
		}
	}
	return goals[observed.HomeID] == *observed.HomeScore &&
		goals[observed.AwayID] == *observed.AwayScore
}

func providerScoreDecreased(stored, observed FixtureFacts) bool {
	return optionalIntDecreased(stored.HomeScore, observed.HomeScore) ||
		optionalIntDecreased(stored.AwayScore, observed.AwayScore)
}

func providerFieldCleared(stored, observed FixtureFacts) bool {
	return (stored.HomeScore != nil && observed.HomeScore == nil) ||
		(stored.AwayScore != nil && observed.AwayScore == nil) ||
		(stored.Elapsed != nil && observed.Elapsed == nil) ||
		(stored.HomeName != "" && observed.HomeName == "") ||
		(stored.AwayName != "" && observed.AwayName == "") ||
		(stored.LeagueName != "" && observed.LeagueName == "")
}

func materialClockRegressed(stored, observed FixtureFacts) bool {
	if stored.Elapsed == nil || observed.Elapsed == nil {
		return false
	}
	return fixtureClock(observed)+materialClockRollbackMinutes < fixtureClock(stored)
}

func providerPhaseRegressed(stored, observed string) bool {
	storedRank, storedKnown := phaseRank(stored)
	observedRank, observedKnown := phaseRank(observed)
	return storedKnown && observedKnown && observedRank < storedRank
}

func phaseRank(status string) (int, bool) {
	switch status {
	case "ns", "tbd":
		return 0, true
	case "1h":
		return 1, true
	case "ht":
		return 2, true
	case "2h":
		return 3, true
	case "et", "bt":
		return 4, true
	case "p":
		return 5, true
	case "ft", "aet", "pen", "canc", "abd", "awd", "wo":
		return 6, true
	default:
		return 0, false
	}
}

func fixtureClock(facts FixtureFacts) int {
	if facts.Elapsed == nil {
		return 0
	}
	return eventClock(*facts.Elapsed, facts.Extra)
}

func eventClock(minute int, extra *int) int {
	if extra != nil {
		return minute + *extra
	}
	return minute
}

func optionalIntDecreased(stored, observed *int) bool {
	return stored != nil && observed != nil && *observed < *stored
}
