// Package alias is the domain layer for team-alias resolution. Pure Go
// — no HTTP, no DB. Mirrors the team_aliases table (see
// internal/infra/pg/schema.sql).
//
// The RAG pipeline that populates a TeamAlias runs as a Phase O
// activity: Wikidata SPARQL for candidate aliases → LLM chat for
// selection → Normalize each survivor → store. Multiple callers
// consume Normalize (this pipeline, the Discovery activity's Twitter
// search-query builder, potentially the read-side API), so it lives in
// domain. LLM output validation ("did the LLM hallucinate?") lives with
// the alias activity — one caller, no benefit to sharing.
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
// Fields split into two groups:
//   INPUT (known at ingest): TeamID, TeamName, IsNational, Country, City
//   RESOLVED (populated by the alias activity):
//     WikidataQID, WikidataAliases, TwitterAliases, LLMModel
//
// A row can exist with only the input fields populated — meaning the
// alias activity hasn't run yet (or ran + failed to resolve). Callers
// gate on HasWikidataResolution / HasTwitterAliases before consuming
// the resolved fields.
type TeamAlias struct {
	// Input fields — set at construction.
	TeamID     int
	TeamName   string
	IsNational bool
	Country    *string
	City       *string

	// Resolved fields — populated by the RAG pipeline in Phase O.
	WikidataQID     *string
	WikidataAliases []string // raw Wikidata pipeline output (audit trail)
	TwitterAliases  []string // LLM-selected + Normalize'd (search-ready)
	LLMModel        *string  // which model generated TwitterAliases

	CreatedAt time.Time
	UpdatedAt time.Time
}

// New constructs a TeamAlias with only the input fields populated.
// Resolved fields are left as zero values (nil pointer / empty slice)
// and get filled in by SetWikidataResolution / SetTwitterAliases as the
// RAG pipeline runs.
func New(teamID int, teamName string, isNational bool, country, city *string, at time.Time) *TeamAlias {
	utc := at.UTC()
	return &TeamAlias{
		TeamID:     teamID,
		TeamName:   teamName,
		IsNational: isNational,
		Country:    country,
		City:       city,
		CreatedAt:  utc,
		UpdatedAt:  utc,
	}
}

// SetWikidataResolution records the QID + raw alias list returned by
// the Wikidata SPARQL query. Both stored together — a QID without
// aliases means "the entity exists but had no useful alias data,"
// which is a distinct state from "we haven't looked yet."
func (t *TeamAlias) SetWikidataResolution(qid string, aliases []string, at time.Time) {
	t.WikidataQID = &qid
	// Store a defensive copy so callers who reuse their slice don't
	// mutate our state. Cheap; the list is typically 5-20 strings.
	t.WikidataAliases = append([]string(nil), aliases...)
	t.UpdatedAt = at.UTC()
}

// SetTwitterAliases records the LLM-filtered + normalized alias list
// alongside the model that generated it. The Model field is required
// (empty string errors) — attribution matters when we later ask "why
// did we pick these particular aliases?"
func (t *TeamAlias) SetTwitterAliases(aliases []string, llmModel string, at time.Time) error {
	if strings.TrimSpace(llmModel) == "" {
		return &InvalidArgError{Field: "llmModel", Reason: "must be non-empty when setting TwitterAliases"}
	}
	t.TwitterAliases = append([]string(nil), aliases...)
	t.LLMModel = &llmModel
	t.UpdatedAt = at.UTC()
	return nil
}

// HasWikidataResolution reports whether the RAG pipeline's Wikidata
// stage has run for this alias. False = "we haven't looked yet"; true
// = "we looked, and the QID + WikidataAliases fields reflect what we
// found (potentially empty aliases)."
func (t *TeamAlias) HasWikidataResolution() bool {
	return t.WikidataQID != nil
}

// HasTwitterAliases reports whether the LLM selection stage has
// populated the TwitterAliases field. Callers of the Discovery
// activity's Twitter search-query builder gate on this — no aliases,
// no query.
func (t *TeamAlias) HasTwitterAliases() bool {
	return len(t.TwitterAliases) > 0
}

// InvalidArgError is returned by domain methods that reject a caller's
// input for reasons that aren't a proper enum violation (which get
// dedicated sentinel errors elsewhere in the domain package).
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
// Intended for Latin-alphabet team names — that's what the RAG
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
