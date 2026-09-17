// collector_fixture.go advances fixture evidence without changing canonical
// fixture/event data. Every cause has its own baseline and recovery rule.
package providerintegrity

import (
	"slices"
	"time"
)

// ObserveFixture consumes one newer finalized poll; the repository excludes
// retries and out-of-order polls before calling it.
func ObserveFixture(s *FixtureHealth, poll HealthPoll, o HealthObservation) []HealthTransition {
	if !poll.At.After(s.LastObservedAt) || poll.ID == s.LastPollID {
		return nil
	}
	if s.Evidence.Version == 0 {
		s.Evidence.Version = HealthEvidenceVersion
	}
	previousUsable := s.LastUsableAt
	usable := o.Availability == AvailabilityUsable
	var transitions []HealthTransition
	if usable {
		advanceClock(s, poll.At, o)
		s.Evidence.Freshness = AssessFreshness(FreshnessEvidence{
			Facts: o.Comparison.Observed, ObservedAt: poll.At,
			PreviousUsableAt: previousUsable, ContinuousSince: s.ContinuousSince,
			ClockProgressAt: s.ClockProgressAt, Usable: true,
		}).State
		s.LastUsableAt = poll.At
	} else {
		s.ContinuousSince = time.Time{}
		s.Evidence.Freshness = FreshnessUnknown
	}
	s.Evidence.Availability, s.Evidence.FailureReason = o.Availability, o.FailureReason
	s.LastPollID, s.LastObservedAt = poll.ID, poll.At

	// Resolve each cause using its retained baseline. A cleared availability
	// cause must not erase the separate clock or consistency restriction.
	retained := make([]HealthIncident, 0, len(s.Evidence.Incidents))
	for _, incident := range s.Evidence.Incidents {
		resolved := false
		switch incident.Cause {
		case CauseUnavailable:
			resolved = usable
		case CauseClockStalled, CauseBoundaryWarning:
			observed := o.Comparison.Observed
			probe := FixtureHealthProbe{ID: poll.ID, At: poll.At, Usable: usable && !o.Interrupted,
				Facts: FixtureFacts{FixtureID: o.FixtureID, Status: observed.Status, Elapsed: observed.Elapsed, Extra: observed.Extra}}
			incident.Probes = append(incident.Probes, probe)
			if len(incident.Probes) > maxRecoveryProbes {
				incident.Probes = incident.Probes[len(incident.Probes)-maxRecoveryProbes:]
			}
			resolved = FreshnessRecovered(incident.Baseline, incident.Probes)
		default:
			if usable {
				comparison := o.Comparison
				comparison.Stored, comparison.ConfirmedEvents = incident.Baseline, incident.ConfirmedEvents
				comparison.StoredEvents = incident.StoredEvents
				verdict := AssessFixture(comparison)
				resolved = !slices.Contains(verdict.Reasons, Reason(incident.Cause))
			}
		}
		if resolved {
			transitions = append(transitions, HealthTransition{
				FixtureID: s.FixtureID, Cause: incident.Cause, StartedAt: incident.StartedAt, Resolved: true,
				LastCredibleAt: incident.LastCredibleAt,
			})
		} else {
			retained = append(retained, incident)
		}
	}
	s.Evidence.Incidents = retained
	open := func(cause HealthCause, policy MutationPolicy, baseline FixtureFacts, events []EventFact) {
		for _, incident := range s.Evidence.Incidents {
			if incident.Cause == cause {
				return
			}
		}
		var credibleAt time.Time
		if cause == CauseUnavailable {
			credibleAt = previousUsable
		}
		if cause == CauseClockStalled || cause == CauseBoundaryWarning {
			credibleAt = s.ClockProgressAt
		}
		s.Evidence.Incidents = append(s.Evidence.Incidents, HealthIncident{
			Cause: cause, StartedAt: poll.At, LastCredibleAt: credibleAt,
			Policy: policy, Baseline: baseline, ConfirmedEvents: events,
			StoredEvents: slices.Clone(o.Comparison.StoredEvents),
		})
		transition := HealthTransition{FixtureID: s.FixtureID, Cause: cause, StartedAt: poll.At,
			LastCredibleAt: credibleAt, Availability: o.Availability, FailureReason: o.FailureReason}
		if cause == HealthCause(ReasonConfirmedEventsMissing) {
			for _, event := range missingEvents(events, o.Comparison.ObservedEvents) {
				transition.MissingEventKeys = append(transition.MissingEventKeys, event.Key)
			}
		}
		if usable {
			observed := o.Comparison.Observed
			transition.Baseline, transition.Observed = &baseline, &observed
		}
		transitions = append(transitions, transition)
	}
	if !usable {
		open(CauseUnavailable, PolicyRejected, FixtureFacts{}, nil)
		return transitions
	}
	switch s.Evidence.Freshness {
	case FreshnessStalled:
		open(CauseClockStalled, PolicyPositiveOnly, clockFacts(s), nil)
	case FreshnessBoundaryWarning:
		open(CauseBoundaryWarning, PolicyTrusted, clockFacts(s), nil)
	}
	verdict := AssessFixture(o.Comparison)
	for _, reason := range verdict.Reasons {
		policy := PolicyPositiveOnly
		if reason == ReasonFixtureIdentityChanged {
			policy = PolicyRejected
		}
		open(HealthCause(reason), policy, o.Comparison.Stored, o.Comparison.ConfirmedEvents)
	}
	return transitions
}

// advanceClock preserves high-water progress through rollbacks and restarts.
// Loss of coverage resets continuity, not the retained progress timestamp.
func advanceClock(s *FixtureHealth, at time.Time, o HealthObservation) {
	facts := o.Comparison.Observed
	if s.ObservedStatus != facts.Status {
		s.ObservedStatus = facts.Status
		s.ContinuousSince, s.PhaseSince = at, at
	}
	if s.LastUsableAt.IsZero() || s.ContinuousSince.IsZero() || at.Sub(s.LastUsableAt) > ObservationGapLimit || o.Interrupted {
		s.ContinuousSince = at
	}
	_, knownClock := phaseRank(s.ClockStatus)
	_, knownObserved := phaseRank(facts.Status)
	if s.ClockStatus == "" || (!knownClock && knownObserved) || (s.ClockStatus == facts.Status && s.ClockElapsed == nil && facts.Elapsed != nil) || ClockProgressed(clockFacts(s), facts) {
		if s.ClockStatus != facts.Status {
			s.ContinuousSince = at
		}
		s.ClockStatus, s.ClockElapsed, s.ClockExtra = facts.Status, facts.Elapsed, facts.Extra
		s.ClockProgressAt = at
	}
	// A backward/unassessable phase is not a new credible clock regime.
	if facts.Status != s.ClockStatus || facts.Elapsed == nil {
		s.ContinuousSince = at
	}
}

// clockFacts strips unrelated score/name data from the running-clock baseline.
func clockFacts(s *FixtureHealth) FixtureFacts {
	return FixtureFacts{FixtureID: s.FixtureID, Status: s.ClockStatus, Elapsed: s.ClockElapsed, Extra: s.ClockExtra}
}

// FixtureHealthPolicy combines unresolved causes, never a latest-success flag.
func FixtureHealthPolicy(s FixtureHealth) MutationPolicy {
	policy := PolicyTrusted
	for _, incident := range s.Evidence.Incidents {
		policy = RestrictPolicies(policy, incident.Policy)
	}
	return policy
}
