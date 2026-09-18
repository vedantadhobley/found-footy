// search.go defines the public fixture-search result and its authoritative
// field-level match provenance.
package fixture

import "github.com/google/uuid"

// SearchEventMatch identifies which searchable fields matched on one event.
// EventID lets consumers highlight the event while Player and Assist retain
// enough provenance for field-specific presentation without repeating text
// matching outside Found Footy.
type SearchEventMatch struct {
	EventID uuid.UUID
	Player  bool
	Assist  bool
}

// SearchMatch records why a fixture satisfied a public search query. These
// booleans are produced by the same database expressions that admit the
// fixture, so clients never need to reconstruct search semantics.
type SearchMatch struct {
	Competition bool
	HomeTeam    bool
	AwayTeam    bool
	Events      []SearchEventMatch
}

// SearchResult pairs the complete fixture with its authoritative match
// provenance. API assembly enriches Fixture with every event and live clip;
// Match only describes the fields that selected the result.
type SearchResult struct {
	Fixture *Fixture
	Match   SearchMatch
}
