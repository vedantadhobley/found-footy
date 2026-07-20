// lookup.go — the name → Wikidata QID resolution pipeline.
//
// Two branches, dispatched on IsNational:
//
//   Clubs   → 9-variant wbsearchentities fuzzy search + description-quality
//             scoring against per-country variations (see lookup_club.go).
//             Ported from Python's rag.py `_search_wikidata_qid` for clubs.
//   Nationals → 3-variant search, first valid football-team candidate wins
//               (see lookup_national.go). Nationals rarely have the
//               reserve/women's-team ambiguity clubs suffer from.
//
// Wikidata is injected via the narrow WikidataFetcher interface so the
// domain package stays pure Go — no HTTP. Prod passes an
// *infra/wikidata.Client. Tests pass a fake.
//
// Design ref: docs/rebuild/proposals/team-aliases.md § "Phase 1 — Lookup".
package alias

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
)

// WikidataFetcher is the narrow interface the lookup pipeline needs
// from the wikidata adapter. Defined in the consumer package (per Go
// idiom); prod passes *wikidata.Client. Tests pass an in-memory fake.
type WikidataFetcher interface {
	SearchEntities(ctx context.Context, term string, opts wikidata.SearchOpts) ([]wikidata.SearchHit, error)
	GetEntity(ctx context.Context, qid string) (*wikidata.Entity, error)
	// BatchGetP31 returns QID → P31 type-list (one SPARQL query for
	// many QIDs). The club + national branches use it to type-check
	// wbsearchentities candidates against Wikidata's own ontology,
	// replacing the fragile description-contains-"football" heuristic.
	BatchGetP31(ctx context.Context, qids []string) (map[string][]string, error)
}

// LookupInput carries the API-Football vendor data the pipeline uses
// to construct search variants + score candidates.
//
// CanonicalName is required. Country and City are optional — clubs
// benefit strongly from both (better disambiguation of same-named
// clubs across countries); nationals need only the country implicit
// in the team name.
type LookupInput struct {
	CanonicalName string
	Country       *string
	City          *string
	IsNational    bool
}

// LookupResult is what the pipeline returns on success.
type LookupResult struct {
	QID         string
	Label       string // Wikidata en label of the resolved entity
	Description string // Wikidata en description — audit trail
	Score       int    // 0 for nationals (first-valid-wins); scoring bucket for clubs
}

// Resolver is the pipeline entry point. Depends on a WikidataFetcher +
// a CountryVariations cache (both injected). Safe for concurrent use;
// the CountryVariations cache is internally mutex-guarded.
type Resolver struct {
	wd       WikidataFetcher
	varCache *CountryVariations
}

// NewResolver constructs a Resolver. varCache may be nil — in that
// case a fresh in-memory cache is created (fine for a single worker
// process; cross-process sharing would need pg-backing later).
func NewResolver(wd WikidataFetcher, varCache *CountryVariations) *Resolver {
	if varCache == nil {
		varCache = NewCountryVariations(wd)
	}
	return &Resolver{wd: wd, varCache: varCache}
}

// Resolve runs the appropriate branch and returns the winning QID +
// audit fields. Errors surface as:
//   - ErrNoMatch: pipeline ran cleanly but no candidate survived
//     filtering. Not a bug — some vendor teams genuinely aren't in
//     Wikidata (typically obscure cup opposition).
//   - Other errors: transport / decode / rate-limit failures from
//     the wikidata adapter, propagated as-is.
func (r *Resolver) Resolve(ctx context.Context, in LookupInput) (LookupResult, error) {
	name := strings.TrimSpace(in.CanonicalName)
	if name == "" {
		return LookupResult{}, fmt.Errorf("alias.Resolver.Resolve: CanonicalName is required")
	}
	if in.IsNational {
		return r.resolveNational(ctx, name, in.Country)
	}
	return r.resolveClub(ctx, name, in.Country, in.City)
}

// ErrNoMatch is returned when the pipeline ran without transport
// errors but no candidate passed filtering. Callers should log +
// treat as "team unresolvable" — the placeholder row stays without
// WikidataQID and can be retried next refresh cycle.
var ErrNoMatch = fmt.Errorf("alias.Resolver: no matching Wikidata entity")

// CountryVariations is an in-memory cache of country-name variations
// derived from Wikidata's P1549 demonyms + P1448 native names for
// each country's entity. Used by the club branch's description-
// quality scoring: knowing spain ≈ spanish ≈ espana lets us match
// entity descriptions like "Spanish football club based in Seville".
//
// Deterministic replacement for Python's LLM-generated country name
// variations. Same input, same output, no LLM.
//
// Cache is keyed by lowercased country name (as it appears in the
// API-Football vendor response). Values are lowercased + diacritic-
// stripped variation tokens. Miss triggers a Wikidata lookup:
// wbsearchentities for the country name → GetEntity for the top hit
// → extract P1549 (all langs) + P1448 (all langs) → normalize.
type CountryVariations struct {
	wd    WikidataFetcher
	mu    sync.RWMutex
	cache map[string][]string // country-lowered → variations
	// countryQID cache: some future callers might want the QID of
	// the country entity too. Not exposed yet; keeping the field for
	// symmetry with the fetch path.
	qidCache map[string]string // country-lowered → country QID
}

// NewCountryVariations constructs a fresh cache bound to wd.
func NewCountryVariations(wd WikidataFetcher) *CountryVariations {
	return &CountryVariations{
		wd:       wd,
		cache:    make(map[string][]string),
		qidCache: make(map[string]string),
	}
}
