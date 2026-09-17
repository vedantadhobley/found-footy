// health_recovery.go checks independent recovery evidence. No helper mutates
// another health cause or certifies that provider data is complete/current.
package providerintegrity

import "time"

// FixtureHealthProbe is one finalized scheduled observation, not one HTTP retry.
// The caller supplies probes after the incident in receipt order. Observation
// IDs and times must be immutable across activity retries.
type FixtureHealthProbe struct {
	ID     string
	At     time.Time
	Usable bool
	Facts  FixtureFacts
}

// ProviderHealthProbe distinguishes new failures from unresolved fixture causes.
// An old quarantined fixture does not alone prevent broader provider recovery.
type ProviderHealthProbe struct {
	ID                 string
	At                 time.Time
	Complete           bool
	NewSystemicAnomaly bool
	NewAffectedFixture bool
}

// FreshnessRecovered requires repeated forward progress or a forward phase
// followed by two compatible observations. A broken evidence sequence resets
// recovery, and a later frozen interval invalidates earlier progress evidence.
func FreshnessRecovered(baseline FixtureFacts, probes []FixtureHealthProbe) bool {
	if baseline.FixtureID <= 0 || !clockValuesValid(baseline) {
		return false
	}
	var sequence observationSequence
	var lastAdvance time.Time
	advances, phaseSamples := 0, 0
	phase := ""
	high := baseline
	for _, probe := range probes {
		if probe.ID == "" || probe.At.IsZero() {
			return false
		}
		previousAt := sequence.at
		if !sequence.accept(probe.ID, probe.At) {
			continue
		}
		usable := probe.Usable && probe.Facts.FixtureID == baseline.FixtureID && clockValuesValid(probe.Facts)
		switch probe.Facts.Status {
		case "1h", "2h", "et":
			usable = usable && probe.Facts.Elapsed != nil
		}
		if !usable ||
			(!previousAt.IsZero() && probe.At.Sub(previousAt) > ObservationGapLimit) {
			lastAdvance = time.Time{}
			advances, phaseSamples, phase = 0, 0, ""
			high = baseline
			if !usable {
				continue
			}
		}
		if forwardPhase(high.Status, probe.Facts.Status) {
			phase, phaseSamples = probe.Facts.Status, 1
			advances = 0
			high = probe.Facts
			lastAdvance = probe.At
			continue
		}
		if probe.Facts.Status != high.Status || materialClockRegressed(high, probe.Facts) {
			advances, phaseSamples, phase = 0, 0, ""
			continue
		}
		if phase != "" && phase == probe.Facts.Status {
			phaseSamples++
		}
		if ClockProgressed(high, probe.Facts) {
			if advances > 0 && probe.At.Sub(lastAdvance) <= InteriorStaleAfter {
				advances = 2
			} else {
				advances = 1
			}
			lastAdvance = probe.At
			high = probe.Facts
		}
	}
	if phaseSamples >= 3 {
		switch high.Status {
		case "1h", "2h", "et":
			if high.Elapsed == nil {
				return false
			}
			threshold := InteriorStaleAfter
			if periodBoundary(high) {
				threshold = BoundaryStaleAfter
			}
			return sequence.at.Sub(lastAdvance) < threshold
		default:
			return true
		}
	}
	return advances >= 2 && !lastAdvance.IsZero() && sequence.at.Sub(lastAdvance) < InteriorStaleAfter
}

// ProviderIntegrityRecovered requires three distinct consecutive complete
// probes, regardless of old unresolved fixture-local causes.
func ProviderIntegrityRecovered(probes []ProviderHealthProbe) bool {
	var sequence observationSequence
	clean := 0
	for _, probe := range probes {
		if probe.ID == "" || probe.At.IsZero() {
			return false
		}
		previousAt := sequence.at
		if !sequence.accept(probe.ID, probe.At) {
			continue
		}
		if !previousAt.IsZero() && probe.At.Sub(previousAt) > ObservationGapLimit {
			clean = 0
		}
		if !probe.Complete || probe.NewSystemicAnomaly || probe.NewAffectedFixture {
			clean = 0
		} else {
			clean++
		}
	}
	return clean >= 3
}

// observationSequence ignores duplicate receipts and older delayed results.
// Input histories are bounded by the caller's recovery evidence window.
type observationSequence struct {
	at  time.Time
	ids map[string]struct{}
}

// accept admits only a distinct, newer logical poll to a recovery sequence.
func (s *observationSequence) accept(id string, at time.Time) bool {
	if id == "" || at.IsZero() || !at.After(s.at) {
		return false
	}
	if _, exists := s.ids[id]; exists {
		return false
	}
	if s.ids == nil {
		s.ids = make(map[string]struct{})
	}
	s.ids[id], s.at = struct{}{}, at
	return true
}
