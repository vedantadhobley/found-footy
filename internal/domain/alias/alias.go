// Package alias models the canonical team-name record and pure text helpers
// used by Twitter query construction. It has no HTTP or database dependency.
// Legacy resolver fields and helpers remain for schema compatibility but have
// no production writer.
//
// Callers:
//   - Ingest inserts or refreshes canonical vendor records as teams appear.
//   - EventWorkflow reads CanonicalName for Twitter query construction.
//   - Text helpers normalize player and team search terms.
package alias

import (
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// TeamAlias is the domain type. Field order aligned with the schema for
// straightforward scanner mapping in the pg adapter.
//
// Canonical vendor fields are active. WikidataQID, Aliases, and ResolvedAt are
// dormant compatibility fields from the retired resolver.
type TeamAlias struct {
	// Active fields — from API-Football via Ingest.
	TeamID        int
	CanonicalName string
	TeamCode      *string // API-Football team.code (3-letter FIFA); often absent
	Country       *string
	City          *string
	IsNational    bool

	// Dormant compatibility fields from the retired resolver.
	//
	// WikidataQID is cached permanently on first successful resolution
	// (Wikidata QIDs for football clubs / national teams don't change).
	WikidataQID *string
	// Aliases contains the former resolver's normalized token set.
	Aliases []string
	// ResolvedAt records the former resolver's completion time.
	ResolvedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// New constructs a TeamAlias canonical record with legacy resolver fields
// left nil or empty.
func New(teamID int, canonicalName string, isNational bool, teamCode, country, city *string, at time.Time) *TeamAlias {
	utc := at.UTC()
	return &TeamAlias{
		TeamID:        teamID,
		CanonicalName: canonicalName,
		TeamCode:      teamCode,
		Country:       country,
		City:          city,
		IsNational:    isNational,
		CreatedAt:     utc,
		UpdatedAt:     utc,
	}
}

// SetResolution populates dormant resolver fields for compatibility tests and
// archived tooling. Production has no caller.
//
// Passing an empty aliases slice is valid and still stamps ResolvedAt, so
// compatibility callers can distinguish populated-empty from never populated.
func (t *TeamAlias) SetResolution(qid string, aliases []string, at time.Time) {
	t.WikidataQID = &qid
	// Defensive copy so callers who reuse their slice don't mutate our
	// state. Cheap; the list is typically 5-30 tokens.
	t.Aliases = append([]string(nil), aliases...)
	utc := at.UTC()
	t.ResolvedAt = &utc
	t.UpdatedAt = utc
}

// IsResolved reports whether dormant resolver data was populated.
func (t *TeamAlias) IsResolved() bool {
	return t.ResolvedAt != nil
}

// InvalidArgError is returned by domain methods that reject a caller's
// input for reasons that aren't a proper enum violation.
type InvalidArgError struct {
	Field  string
	Reason string
}

func (e *InvalidArgError) Error() string {
	return "alias: invalid argument " + e.Field + ": " + e.Reason
}

// Normalize strips combining diacritic marks (Unicode category Mn)
// after NFD decomposition. Preserves case.
//
// Intended for Latin-alphabet team names — that's what the alias
// pipeline and Twitter search query builder call it on. Cyrillic
// mostly passes through unchanged (few Cyrillic letters have combining
// forms). CJK is NOT safe: Japanese dakuten (ガ = カ + combining
// voiced-sound mark) and similar phonemic marks WILL be stripped,
// which corrupts the word. If we ever need to normalize CJK team
// names, add a script-aware variant then; for now, callers only feed
// this Latin input.
//
// Examples:
//
//	Atlético  → Atletico
//	Bayern München → Bayern Munchen
//	Señor      → Senor
//	Spartak    → Spartak       (unchanged — no combining marks)
//	Спартак    → Спартак       (Cyrillic without decomposition)
//	""         → ""
func Normalize(s string) string {
	if s == "" {
		return s
	}
	decomposed := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue // Mn = "Mark, nonspacing" — the combining accent marks
		}
		b.WriteRune(r)
	}
	return b.String()
}
