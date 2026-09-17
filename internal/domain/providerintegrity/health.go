// health.go defines temporal health policy used by the durable collector. The
// Postgres mutation boundary enforces the resulting permissions under lock.
package providerintegrity

import "time"

const (
	// InteriorStaleAfter is the first-release hypothesis for a running clock.
	InteriorStaleAfter = 3 * time.Minute
	// BoundaryStaleAfter allows capped period-end clocks a longer warning window.
	BoundaryStaleAfter = 10 * time.Minute
	// ObservationGapLimit separates continuous evidence from unobserved time.
	ObservationGapLimit = 90 * time.Second
)

// Freshness describes clock evidence, not event completeness or write safety.
type Freshness string

const (
	FreshnessUnknown         Freshness = "unknown"
	FreshnessWithinWindow    Freshness = "within_window"
	FreshnessNotClocked      Freshness = "not_clocked"
	FreshnessStalled         Freshness = "stalled"
	FreshnessBoundaryWarning Freshness = "boundary_warning"
)

// FreshnessEvidence is the bounded input a durable collector must maintain.
// ContinuousSince is the start of uninterrupted usable observations in the
// current clock regime; ClockProgressAt is its last accepted high-water mark.
// The collector must reset continuity on a failed/missing poll or phase change,
// and must not update progress for identical snapshots, metadata, or retries.
// ObserveFixture maintains these guarantees; SQL persists its typed cursors.
type FreshnessEvidence struct {
	Facts            FixtureFacts
	ObservedAt       time.Time
	PreviousUsableAt time.Time
	ContinuousSince  time.Time
	ClockProgressAt  time.Time
	Usable           bool
}

// FreshnessVerdict supplies only this observation's freshness contribution.
// Unknown evidence cannot clear a previously persisted restriction. The caller
// must resolve that cause using recovery evidence, independently of this value.
type FreshnessVerdict struct {
	State       Freshness
	Policy      MutationPolicy
	EvidenceAge time.Duration
}

// AssessFreshness distinguishes supported clock silence from normal pauses.
// It intentionally receives no wall clock, database, or request client.
func AssessFreshness(e FreshnessEvidence) FreshnessVerdict {
	out := FreshnessVerdict{State: FreshnessUnknown, Policy: PolicyTrusted}
	if !e.Usable || e.ObservedAt.IsZero() {
		return out
	}
	switch e.Facts.Status {
	case "ht", "bt", "p", "int", "susp", "ns", "pst", "tbd", "ft", "aet", "pen", "canc", "abd", "awd", "wo":
		out.State = FreshnessNotClocked
		return out
	case "1h", "2h", "et":
	default:
		return out
	}
	if e.Facts.Elapsed == nil || *e.Facts.Elapsed < 0 || (e.Facts.Extra != nil && *e.Facts.Extra < 0) {
		return out
	}
	// At least two distinct, ordered observations are necessary. A collector
	// cannot certify continuity by passing the current successful response twice.
	if e.PreviousUsableAt.IsZero() || !e.PreviousUsableAt.Before(e.ObservedAt) ||
		e.ObservedAt.Sub(e.PreviousUsableAt) > ObservationGapLimit ||
		e.ContinuousSince.IsZero() || e.ContinuousSince.After(e.PreviousUsableAt) ||
		e.ClockProgressAt.IsZero() || e.ClockProgressAt.After(e.ObservedAt) {
		return out
	}
	start := e.ClockProgressAt
	if e.ContinuousSince.After(start) {
		start = e.ContinuousSince
	}
	out.EvidenceAge = e.ObservedAt.Sub(start)
	out.State = FreshnessWithinWindow
	threshold := InteriorStaleAfter
	boundary := periodBoundary(e.Facts)
	if boundary {
		threshold = BoundaryStaleAfter
	}
	if out.EvidenceAge < threshold {
		return out
	}
	if boundary {
		out.State = FreshnessBoundaryWarning
	} else {
		out.State, out.Policy = FreshnessStalled, PolicyPositiveOnly
	}
	return out
}

// periodBoundary treats continuous overtime minutes like capped extra fields.
// ET reuses one status for both periods, so only its 105 boundary or 120+
// qualifies; minute 106 is ordinary second-period play when no BT was observed.
func periodBoundary(f FixtureFacts) bool {
	switch f.Status {
	case "1h":
		return *f.Elapsed >= 45
	case "2h":
		return *f.Elapsed >= 90
	case "et":
		return *f.Elapsed == 105 || *f.Elapsed >= 120
	default:
		return false
	}
}

// ClockProgressed compares an observation against its retained clock baseline.
// Callers must retain the high-water mark rather than feed a regressed prior
// response back as the baseline. Score/name changes are deliberately ignored.
func ClockProgressed(baseline, observed FixtureFacts) bool {
	if baseline.FixtureID != observed.FixtureID || baseline.FixtureID <= 0 ||
		!clockValuesValid(baseline) || !clockValuesValid(observed) {
		return false
	}
	if forwardPhase(baseline.Status, observed.Status) {
		return true
	}
	if baseline.Status != observed.Status || observed.Elapsed == nil || baseline.Elapsed == nil {
		return false
	}
	switch observed.Status {
	case "1h", "2h", "et":
		return fixtureClock(observed) > fixtureClock(baseline)
	default:
		return false
	}
}

// clockValuesValid rejects invalid numeric evidence without inventing null clocks.
func clockValuesValid(f FixtureFacts) bool {
	return (f.Elapsed == nil || *f.Elapsed >= 0) && (f.Extra == nil || *f.Extra >= 0)
}

// forwardPhase reuses the existing period ordering without requiring elapsed
// growth across halftime or extra-time resets.
func forwardPhase(before, after string) bool {
	left, knownLeft := phaseRank(before)
	right, knownRight := phaseRank(after)
	return (knownLeft && knownRight && right > left) ||
		(before == "et" && after == "bt") || (before == "bt" && after == "et")
}

// RestrictPolicies composes independent causes without letting one recovery
// clear another restriction. An invalid policy fails closed.
func RestrictPolicies(policies ...MutationPolicy) MutationPolicy {
	result := PolicyTrusted
	for _, policy := range policies {
		if !policy.Valid() || policy == PolicyRejected {
			return PolicyRejected
		}
		if policy == PolicyPositiveOnly {
			result = PolicyPositiveOnly
		}
	}
	return result
}
