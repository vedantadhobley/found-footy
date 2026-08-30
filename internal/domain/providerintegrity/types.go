// Package providerintegrity classifies untrusted provider observations before
// they are allowed to mutate canonical fixture and event state.
package providerintegrity

// MutationPolicy is the strongest mutation class an observation may perform.
type MutationPolicy string

const (
	// PolicyTrusted permits ordinary reconciliation, including supported
	// destructive corrections.
	PolicyTrusted MutationPolicy = "trusted"
	// PolicyPositiveOnly permits additions and forward progress but suppresses
	// destructive or regressive mutations.
	PolicyPositiveOnly MutationPolicy = "positive_only"
	// PolicyRejected permits no provider-derived mutation.
	PolicyRejected MutationPolicy = "rejected"
)

// Valid reports whether p is a known mutation policy.
func (p MutationPolicy) Valid() bool {
	switch p {
	case PolicyTrusted, PolicyPositiveOnly, PolicyRejected:
		return true
	}
	return false
}

// Reason is a bounded semantic anomaly code returned by the pure evaluator.
type Reason string

const (
	ReasonFixtureIdentityChanged     Reason = "fixture_identity_changed"
	ReasonPhaseRegressed             Reason = "phase_regressed"
	ReasonTerminalRegressed          Reason = "terminal_regressed"
	ReasonClockRegressed             Reason = "clock_regressed"
	ReasonScoreDecreased             Reason = "score_decreased"
	ReasonPopulatedFieldCleared      Reason = "populated_field_cleared"
	ReasonConfirmedEventsMissing     Reason = "confirmed_events_missing"
	ReasonMultipleFixtureRegression  Reason = "multiple_fixture_regressions"
	ReasonMultipleEventDisappearance Reason = "multiple_event_disappearances"
)

// FixtureFacts contains the provider-owned fixture facts needed to assess
// semantic forward progress. It is deliberately smaller than either the wire
// response or the canonical fixture aggregate.
type FixtureFacts struct {
	FixtureID int64
	HomeID    int
	AwayID    int
	LeagueID  int

	HomeName   string
	AwayName   string
	LeagueName string

	Status   string
	Terminal bool
	Elapsed  *int
	Extra    *int

	HomeScore *int
	AwayScore *int
}

// EventFact is the provider-independent identity and clock evidence needed to
// determine whether one confirmed event is still represented.
type EventFact struct {
	Key    string
	TeamID int
	Type   string
	Minute int
	Extra  *int
}

// FixtureComparison pairs the last stored facts with one fresh provider
// observation. ConfirmedEvents contains only active events that previously
// crossed the debounce threshold; ObservedEvents contains every currently
// trackable provider event, including newly reported events.
type FixtureComparison struct {
	Stored          FixtureFacts
	Observed        FixtureFacts
	ConfirmedEvents []EventFact
	ObservedEvents  []EventFact
}

// FixtureVerdict is the shadow mutation recommendation for one fixture.
type FixtureVerdict struct {
	FixtureID               int64
	Policy                  MutationPolicy
	Reasons                 []Reason
	MissingConfirmedEvents  int
	SupportedGoalCorrection bool
}

// Anomalous reports whether this fixture should be quarantined when
// enforcement ships.
func (v FixtureVerdict) Anomalous() bool {
	return v.Policy == PolicyPositiveOnly || v.Policy == PolicyRejected
}

// BatchVerdict is the provider-wide shadow recommendation. Fixtures contains
// only anomalous per-fixture verdicts so workflow payloads remain bounded.
type BatchVerdict struct {
	Policy                 MutationPolicy
	Reasons                []Reason
	Fixtures               []FixtureVerdict
	RegressedFixtures      int
	MissingConfirmedEvents int
}

// Anomalous reports whether any fixture or the provider-wide batch produced a
// shadow warning.
func (v BatchVerdict) Anomalous() bool {
	return v.Policy != PolicyTrusted || len(v.Fixtures) > 0
}
