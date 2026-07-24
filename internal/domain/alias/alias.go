// Package alias models per-team alias resolution: the words we feed
// into Twitter advanced-search OR-queries to find goal-video candidates.
//
// Pure Go — no HTTP, no DB. Adapters wire Wikidata (lookup + fetch) and
// pg (cache). The pipeline itself is deterministic, no LLM: multilingual
// Wikidata aliases (11 Latin-script langs) + P1449 nicknames + P1549
// demonyms for nationals + word-processing rules (NFD strip diacritics,
// multilingual skip-list, ≥2-lang keep threshold, venue-city skip for
// clubs). Design ref: docs/rebuild/proposals/team-aliases.md.
//
// Callers:
//   - Ingest activity: inserts placeholder rows (canonical vendor data
//     only) as new teams appear; the alias resolution activity fills
//     in wikidata_qid + aliases + resolved_at asynchronously.
//   - Discovery activity: reads Aliases for the two teams in a fixture
//     when building a Twitter search query.
//   - Normalize helper: exported for shared use by the alias pipeline
//     and the search-query builder.
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
// Two-phase population:
//
//	Phase 1 (Ingest):  CanonicalName, TeamCode, Country, City, IsNational
//	Phase 2 (resolve): WikidataQID, Aliases, ResolvedAt
//
// A row can exist with only phase-1 fields populated — that means the
// resolution pipeline hasn't run yet. Callers gate on IsResolved before
// consuming Aliases.
type TeamAlias struct {
	// Phase-1 fields — from API-Football via Ingest.
	TeamID        int
	CanonicalName string
	TeamCode      *string // API-Football team.code (3-letter FIFA); often absent
	Country       *string
	City          *string
	IsNational    bool

	// Phase-2 fields — from the alias resolution pipeline.
	//
	// WikidataQID is cached permanently on first successful resolution
	// (Wikidata QIDs for football clubs / national teams don't change).
	// On subsequent 30-day TTL refreshes we skip the expensive fuzzy
	// wbsearchentities lookup and just re-fetch entity JSON.
	WikidataQID *string
	// Aliases is the normalized lowercase word list submitted to Twitter
	// advanced search as an OR clause. Empty slice means either "not yet
	// resolved" (ResolvedAt is nil) or "resolved but pipeline yielded
	// zero surviving tokens" (ResolvedAt is set) — distinguished by
	// IsResolved().
	Aliases []string
	// ResolvedAt is set when the pipeline completes successfully. Drives
	// the 30-day TTL check via IsFresh.
	ResolvedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// New constructs a TeamAlias placeholder with only the phase-1 vendor
// fields populated. Phase-2 fields stay nil / empty — the alias
// resolution activity fills them in via SetResolution.
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

// SetResolution records a completed pipeline run: the Wikidata QID that
// was resolved, the derived alias set, and the resolution timestamp
// (which anchors the 30-day TTL check).
//
// Passing an empty aliases slice is valid — it means "we ran the
// pipeline and no tokens survived filtering." That's distinct from
// "we haven't looked yet" (ResolvedAt still nil). Callers should
// gate on IsResolved rather than len(Aliases).
func (t *TeamAlias) SetResolution(qid string, aliases []string, at time.Time) {
	t.WikidataQID = &qid
	// Defensive copy so callers who reuse their slice don't mutate our
	// state. Cheap; the list is typically 5-30 tokens.
	t.Aliases = append([]string(nil), aliases...)
	utc := at.UTC()
	t.ResolvedAt = &utc
	t.UpdatedAt = utc
}

// IsResolved reports whether the resolution pipeline has run for this
// team (regardless of outcome — an empty Aliases result with ResolvedAt
// set still counts as resolved).
func (t *TeamAlias) IsResolved() bool {
	return t.ResolvedAt != nil
}

// IsFresh reports whether the resolution is fresh relative to a TTL.
// Unresolved rows are never fresh. The reference now is passed in so
// tests and workflow-time-aware callers can pass their own clock.
func (t *TeamAlias) IsFresh(now time.Time, ttl time.Duration) bool {
	if t.ResolvedAt == nil {
		return false
	}
	return now.UTC().Sub(*t.ResolvedAt) < ttl
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
