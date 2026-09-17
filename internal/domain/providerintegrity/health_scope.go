// health_scope.go correlates distinct incident onsets rather than repeated
// warnings or a large number of missing events from one fixture.
package providerintegrity

import (
	"slices"
	"time"
)

// CorrelationWindow is the proposed shared-failure onset window.
const CorrelationWindow = 2 * time.Minute

// HealthCause separates freshness evidence from destructive inconsistency.
type HealthCause string

const (
	CauseClockStalled      HealthCause = "clock_stalled"
	CauseBoundaryWarning   HealthCause = "boundary_warning"
	CauseIntegrityConflict HealthCause = "integrity_conflict"
)

// HealthEpisode represents an unresolved or resolved fixture cause. StartedAt
// is its immutable detection onset, not the timestamp of its latest repeat.
type HealthEpisode struct {
	FixtureID  int64
	Cause      HealthCause
	StartedAt  time.Time
	ResolvedAt time.Time
}

// HealthCorrelation reports newly correlated evidence, not durable recovery.
// An empty result must not clear an existing provider restriction; recovery
// has its own evidence and is evaluated separately.
type HealthCorrelation struct {
	StalledFixtures  []int64
	ConflictFixtures []int64
	Policy           MutationPolicy
}

// CorrelateHealth counts each fixture once per cause family. Boundary warnings
// cannot generate restrictions, and one family's signals cannot amplify another.
func CorrelateHealth(at time.Time, episodes []HealthEpisode) HealthCorrelation {
	stalled, conflicts := make(map[int64]struct{}), make(map[int64]struct{})
	for _, episode := range episodes {
		if at.IsZero() || episode.FixtureID <= 0 || episode.StartedAt.IsZero() ||
			!episode.ResolvedAt.IsZero() || episode.StartedAt.After(at) ||
			at.Sub(episode.StartedAt) > CorrelationWindow {
			continue
		}
		switch episode.Cause {
		case CauseClockStalled:
			stalled[episode.FixtureID] = struct{}{}
		case CauseIntegrityConflict:
			conflicts[episode.FixtureID] = struct{}{}
		}
	}
	out := HealthCorrelation{Policy: PolicyTrusted}
	if len(stalled) >= 2 {
		out.StalledFixtures = sortedFixtureIDs(stalled)
	}
	if len(conflicts) >= 2 {
		out.ConflictFixtures = sortedFixtureIDs(conflicts)
		out.Policy = PolicyPositiveOnly
	}
	return out
}

// sortedFixtureIDs stabilizes diagnostics without relying on map iteration.
func sortedFixtureIDs(ids map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
