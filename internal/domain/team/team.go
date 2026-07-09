// team.go — the TrackedTeam value type. Storage-agnostic.
package team

import "time"

// TrackedTeam records one team we track via the ingest filter, plus
// which (league, season) pull it came from. The provenance fields let
// us reason about refresh: if TrackedLeagueIDs changes or a season
// rolls over, we know which cache rows are still valid.
type TrackedTeam struct {
	ID          int64     // API-Football team ID (natural PK)
	Name        string    // display name; kept for observability + debugging
	LeagueID    int       // which /teams?league=X call this row came from
	LeagueName  string    // denormalized display name of the league
	Season      int       // season year at refresh time (2026 = 2026-27 in Euro convention)
	RefreshedAt time.Time // when this row was last written (UTC)
}

// Set is a convenience wrapper over map[int64]struct{} for existence
// checks — the ingest filter needs O(1) team lookup per fixture.
type Set map[int64]struct{}

// NewSetFromTeams builds a Set from a TrackedTeam slice.
func NewSetFromTeams(ts []TrackedTeam) Set {
	s := make(Set, len(ts))
	for _, t := range ts {
		s[t.ID] = struct{}{}
	}
	return s
}

// Has reports whether id is in the set.
func (s Set) Has(id int64) bool {
	_, ok := s[id]
	return ok
}

// Len returns the size of the set.
func (s Set) Len() int { return len(s) }
