// collector_provider.go correlates fixture incident onsets and advances the
// independent provider recovery streak inside the durable observation boundary.
package providerintegrity

import "slices"

// ObserveProvider combines already-updated fixture evidence. Poll receipt and
// cursor locking belong to HealthStore, so retries cannot run this twice.
func ObserveProvider(s *ProviderHealth, poll HealthPoll, fixtures []FixtureHealth, transitions []HealthTransition) HealthResult {
	if s.Policy == "" {
		s.Policy = PolicyTrusted
	}
	s.Evidence.Version = HealthEvidenceVersion
	out := HealthResult{ObservedAt: poll.At, Requested: len(poll.Observations), Transitions: transitions}
	for _, observation := range poll.Observations {
		if observation.Availability == AvailabilityUsable {
			out.Usable++
		} else {
			out.UnavailableIDs = append(out.UnavailableIDs, observation.FixtureID)
		}
	}
	for _, f := range fixtures {
		if FixtureHealthPolicy(f) != PolicyTrusted {
			out.RestrictedIDs = append(out.RestrictedIDs, f.FixtureID)
		}
		for _, incident := range f.Evidence.Incidents {
			switch incident.Cause {
			case CauseClockStalled:
				out.StalledIDs = append(out.StalledIDs, f.FixtureID)
			case CauseBoundaryWarning:
				out.BoundaryIDs = append(out.BoundaryIDs, f.FixtureID)
			}
		}
	}
	recent := make([]HealthEpisode, 0, len(s.Evidence.Recent))
	for _, episode := range s.Evidence.Recent {
		if poll.At.Sub(episode.StartedAt) <= CorrelationWindow {
			recent = append(recent, episode)
		}
	}
	newConflict := false
	for _, transition := range transitions {
		family := causeFamily(transition.Cause)
		if family == CauseUnavailable || family == CauseBoundaryWarning {
			continue
		}
		if transition.Resolved {
			// A fixture may still have another integrity cause after this one
			// resolves. Only close its family when all such causes are gone.
			if fixtureHasFamily(fixtures, transition.FixtureID, family) {
				continue
			}
			for i := range recent {
				if recent[i].FixtureID == transition.FixtureID && recent[i].Cause == family {
					recent[i].ResolvedAt = poll.At
				}
			}
			continue
		}
		// Only the first cause in this fixture/family is a new affected
		// fixture. Older unresolved warnings cannot acquire a fresh onset.
		onset := transition.StartedAt
		for _, fixture := range fixtures {
			if fixture.FixtureID != transition.FixtureID {
				continue
			}
			for _, incident := range fixture.Evidence.Incidents {
				if causeFamily(incident.Cause) == family && incident.StartedAt.Before(onset) {
					onset = incident.StartedAt
				}
			}
		}
		if !onset.Equal(poll.At) {
			continue
		}
		if family == CauseIntegrityConflict {
			newConflict = true
		}
		episode := HealthEpisode{FixtureID: transition.FixtureID, Cause: family, StartedAt: onset}
		if !slices.Contains(recent, episode) {
			recent = append(recent, episode)
		}
	}
	s.Evidence.Recent = recent
	out.Correlation = CorrelateHealth(poll.At, recent)
	newSystemic := newConflict && out.Correlation.Policy == PolicyPositiveOnly
	if newSystemic {
		if s.Policy != PolicyPositiveOnly {
			s.Generation++
			s.Evidence.IncidentAt = poll.At
			out.Transitions = append(out.Transitions, HealthTransition{Cause: CauseIntegrityConflict, StartedAt: poll.At})
		}
		s.Policy = PolicyPositiveOnly
		s.Evidence.Probes = nil
	}
	if s.Policy == PolicyPositiveOnly {
		complete := out.Usable == out.Requested
		for _, observation := range poll.Observations {
			complete = complete && !observation.Interrupted
		}
		s.Evidence.Probes = append(s.Evidence.Probes, ProviderHealthProbe{
			ID: poll.ID, At: poll.At, Complete: complete,
			NewSystemicAnomaly: newSystemic, NewAffectedFixture: newConflict,
		})
		if len(s.Evidence.Probes) > 3 {
			s.Evidence.Probes = s.Evidence.Probes[len(s.Evidence.Probes)-3:]
		}
		if ProviderIntegrityRecovered(s.Evidence.Probes) {
			s.Generation++
			s.Policy = PolicyTrusted
			out.Transitions = append(out.Transitions, HealthTransition{Cause: CauseIntegrityConflict, StartedAt: s.Evidence.IncidentAt, Resolved: true})
			s.Evidence = ProviderHealthEvidence{Version: HealthEvidenceVersion, Recent: recent}
		}
	}
	s.LastObservedAt = poll.At
	out.Generation, out.ProviderPolicy = s.Generation, s.Policy
	slices.Sort(out.UnavailableIDs)
	slices.Sort(out.RestrictedIDs)
	slices.Sort(out.StalledIDs)
	slices.Sort(out.BoundaryIDs)
	return out
}

// causeFamily keeps reason-specific recovery independent of scope correlation.
func causeFamily(cause HealthCause) HealthCause {
	switch cause {
	case CauseUnavailable, CauseClockStalled, CauseBoundaryWarning:
		return cause
	default:
		return CauseIntegrityConflict
	}
}

// fixtureHasFamily reports unresolved evidence, not repeated warning count.
func fixtureHasFamily(fixtures []FixtureHealth, id int64, family HealthCause) bool {
	for _, fixture := range fixtures {
		if fixture.FixtureID == id {
			for _, incident := range fixture.Evidence.Incidents {
				if causeFamily(incident.Cause) == family {
					return true
				}
			}
		}
	}
	return false
}
