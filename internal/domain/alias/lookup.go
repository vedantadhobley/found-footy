// lookup.go — the name → Wikidata QID resolution pipeline.
//
// Two branches, dispatched on IsNational:
//
//   Clubs   → Wikipedia CirrusSearch full-text lookup with the template
//             `{name} {country} football club`, then batch P31 verify
//             against Wikidata's ontology (Q476028 association football
//             club + Q103229495 men's team). See lookup_club.go.
//   Nationals → Wikipedia lookup with `{country} men's national football
//               team`, same P31 verify (Q135408445 + Q6979593). See
//               lookup_national.go.
//
// The Wikipedia + Wikidata split is deliberate. Wikipedia's CirrusSearch
// is a full-text retriever over article bodies (BM25 with field
// boosting) — vastly better than Wikidata's `wbsearchentities` prefix
// index at fuzzy candidate generation. Wikipedia articles carry
// `pageprops.wikibase_item` which bridges results back to Wikidata for
// the P31 type check + downstream alias extraction.
//
// Design ref: docs/design/proposals/alias-entity-resolution.md.
package alias

import (
	"context"
	"fmt"
	"strings"

	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
)

// WikidataFetcher is the narrow interface the lookup + selection
// pipelines need from the wikidata adapter. Reduced from earlier — the
// lookup pipeline no longer uses SearchEntities (Wikipedia's search
// replaces it entirely for candidate generation), only BatchGetP31 for
// structural type verification and GetEntity for alias extraction.
type WikidataFetcher interface {
	GetEntity(ctx context.Context, qid string) (*wikidata.Entity, error)
	BatchGetP31(ctx context.Context, qids []string) (map[string][]string, error)
}

// WikipediaResolver is the narrow interface the lookup pipeline needs
// from the wikipedia adapter. One method — full-text search returning
// hits with their Wikidata QIDs — is enough for both club + national
// branches.
type WikipediaResolver interface {
	SearchAndResolve(ctx context.Context, query string, opts wikipedia.SearchOpts) ([]wikipedia.Hit, error)
}

// LookupInput carries the API-Football vendor data the pipeline uses
// to construct the Wikipedia query.
//
// CanonicalName is required. Country is strongly recommended for
// disambiguation (fixes collisions like Al-Ahly Egypt vs Al-Ahli
// Amman, Sao Paulo FC vs São Paulo city). City is passed through for
// the downstream SELECT phase's venue-city skip; not used by lookup.
type LookupInput struct {
	CanonicalName string
	Country       *string
	City          *string
	IsNational    bool
}

// LookupResult is what the pipeline returns on success.
type LookupResult struct {
	QID   string
	Label string // Wikipedia article title of the resolved entity
	// Description is left empty in the Wikipedia-based pipeline — CirrusSearch
	// doesn't return the article's Wikidata description in-band. Kept in the
	// struct for backward compat with test expectations + future audit hooks.
	Description string
	Score       int // reserved; Wikipedia's own ranking supersedes scoring here
}

// Resolver is the pipeline entry point. Depends on both Wikipedia
// (candidate generation) and Wikidata (type verification + alias
// extraction). Safe for concurrent use.
type Resolver struct {
	wd WikidataFetcher
	wp WikipediaResolver
}

// NewResolver constructs a Resolver bound to the two adapters.
//
// wp may be nil for callers that only intend to use Select (the
// selection pipeline never touches Wikipedia; passing nil there keeps
// select-focused test setups minimal). Resolve requires wp non-nil.
func NewResolver(wd WikidataFetcher, wp WikipediaResolver) *Resolver {
	return &Resolver{wd: wd, wp: wp}
}

// Resolve runs the appropriate branch and returns the winning QID +
// audit fields. Errors surface as:
//   - ErrNoMatch: pipeline ran cleanly but no candidate passed the
//     P31 verify. Legitimate for obscure teams not on Wikipedia at all.
//   - Other errors: transport / decode failures from the wikipedia
//     or wikidata adapters, propagated as-is.
func (r *Resolver) Resolve(ctx context.Context, in LookupInput) (LookupResult, error) {
	name := strings.TrimSpace(in.CanonicalName)
	if name == "" {
		return LookupResult{}, fmt.Errorf("alias.Resolver.Resolve: CanonicalName is required")
	}
	if in.IsNational {
		return r.resolveNational(ctx, name, in.Country)
	}
	return r.resolveClub(ctx, name, in.Country)
}

// ErrNoMatch is returned when the pipeline ran without transport
// errors but no candidate passed filtering. Callers should log +
// treat as "team unresolvable" — the placeholder row stays without
// WikidataQID and can be retried next refresh cycle.
var ErrNoMatch = fmt.Errorf("alias.Resolver: no matching Wikidata entity")

// Ensure the wikidata import is still referenced (removes lint noise
// when the whole pipeline is refactored). The alias.WikidataFetcher
// interface consumes wikidata.Entity via GetEntity; direct usage stays
// pointed at the concrete type.
var _ = wikidata.Entity{}
