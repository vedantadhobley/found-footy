// Tests for the alias-lookup pipeline. Uses fakes for both WikipediaResolver
// and WikidataFetcher so the domain package stays free of live vendor
// dependencies in unit tests. An integration test in a separate file
// (skipped in -short) exercises real endpoints for a small roster.
package alias_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
)

// fakeWP is an in-memory WikipediaResolver. Hits are keyed by exact
// search query so tests can assert both the query construction and the
// candidate set at once.
type fakeWP struct {
	mu    sync.Mutex
	hits  map[string][]wikipedia.Hit
	err   error
	calls []string
}

func (f *fakeWP) SearchAndResolve(_ context.Context, query string, _ wikipedia.SearchOpts) ([]wikipedia.Hit, error) {
	f.mu.Lock()
	f.calls = append(f.calls, query)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.hits[query], nil
}

// fakeWDLookup implements the reduced WikidataFetcher interface used by
// the lookup pipeline (GetEntity + BatchGetP31). GetEntity is present
// to satisfy the interface but the lookup pipeline never calls it — the
// tests below panic in it defensively.
type fakeWDLookup struct {
	mu       sync.Mutex
	p31      map[string][]string
	p31Err   error
	p31Calls [][]string
}

func (f *fakeWDLookup) BatchGetP31(_ context.Context, qids []string) (map[string][]string, error) {
	f.mu.Lock()
	f.p31Calls = append(f.p31Calls, append([]string(nil), qids...))
	f.mu.Unlock()
	if f.p31Err != nil {
		return nil, f.p31Err
	}
	out := make(map[string][]string, len(qids))
	for _, q := range qids {
		if t, ok := f.p31[q]; ok {
			out[q] = append([]string(nil), t...)
		}
	}
	return out, nil
}

func (f *fakeWDLookup) GetEntity(_ context.Context, qid string) (*wikidata.Entity, error) {
	panic(fmt.Sprintf("fakeWDLookup.GetEntity(%s): lookup pipeline never calls GetEntity — test scope drift", qid))
}

// Resolve fast-fails on empty CanonicalName.
func TestResolver_Resolve_EmptyName(t *testing.T) {
	r := alias.NewResolver(&fakeWDLookup{}, &fakeWP{})
	_, err := r.Resolve(context.Background(), alias.LookupInput{CanonicalName: "   "})
	if err == nil {
		t.Fatal("expected error for empty CanonicalName, got nil")
	}
}

// Club branch happy path: Wikipedia returns OGC Nice at top, P31
// verify passes, resolver returns Q185163.
func TestResolver_ClubBranch_TopHitWithValidP31(t *testing.T) {
	wp := &fakeWP{
		hits: map[string][]wikipedia.Hit{
			"Nice France football club": {
				{Title: "OGC Nice", WikidataQID: "Q185163", Index: 1},
				{Title: "2016 Nice truck attack", WikidataQID: "Q25893254", Index: 2},
			},
		},
	}
	wd := &fakeWDLookup{
		p31: map[string][]string{
			"Q185163": {"Q476028"},  // association football club — accepts
			"Q25893254": {"Q7860"},  // attack event — not in accept set
		},
	}
	r := alias.NewResolver(wd, wp)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "Nice",
		Country:       strPtr("France"),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q185163" {
		t.Errorf("QID = %q; want Q185163", got.QID)
	}
	if got.Label != "OGC Nice" {
		t.Errorf("Label = %q; want 'OGC Nice' (Wikipedia article title)", got.Label)
	}
	if len(wp.calls) != 1 || wp.calls[0] != "Nice France football club" {
		t.Errorf("wikipedia calls = %v; want single 'Nice France football club'", wp.calls)
	}
}

// P31 filter skips over a candidate that Wikipedia ranked at top but
// doesn't have an acceptable type. This is the marquee AC Milan / Milan
// TV regression case: Wikipedia might rank a TV channel article first,
// but P31 filter drops it and we take the next surviving hit.
func TestResolver_ClubBranch_P31RejectsWrongTypeAtTop(t *testing.T) {
	wp := &fakeWP{
		hits: map[string][]wikipedia.Hit{
			"AC Milan Italy football club": {
				{Title: "Milan TV", WikidataQID: "Q2478275", Index: 1},
				{Title: "AC Milan", WikidataQID: "Q1543", Index: 2},
			},
		},
	}
	wd := &fakeWDLookup{
		p31: map[string][]string{
			"Q2478275": {"Q2001305"},              // TV channel — no accept
			"Q1543":    {"Q476028", "Q103229495"}, // association football club + men's team
		},
	}
	r := alias.NewResolver(wd, wp)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "AC Milan",
		Country:       strPtr("Italy"),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q1543" {
		t.Errorf("QID = %q; want Q1543 (Milan TV must be P31-rejected)", got.QID)
	}
}

// P31 reject-set: even when a candidate has an accept type, ANY reject
// type drops it. Mimics Milan Futuro (reserve team + football club).
func TestResolver_ClubBranch_P31RejectsReserveEvenWithAcceptType(t *testing.T) {
	wp := &fakeWP{
		hits: map[string][]wikipedia.Hit{
			"AC Milan Italy football club": {
				{Title: "Milan Futuro", WikidataQID: "Q126923253", Index: 1},
				{Title: "AC Milan", WikidataQID: "Q1543", Index: 2},
			},
		},
	}
	wd := &fakeWDLookup{
		p31: map[string][]string{
			"Q126923253": {"Q2412834", "Q476028"}, // reserve + club — reject wins
			"Q1543":      {"Q476028"},              // accept
		},
	}
	r := alias.NewResolver(wd, wp)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "AC Milan",
		Country:       strPtr("Italy"),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q1543" {
		t.Errorf("QID = %q; want Q1543 (Milan Futuro must be reject-filtered)", got.QID)
	}
}

// Nil country falls back to the bare `{name} football club` template.
// Works but less reliable — kept as a defensive fallback in the code.
func TestResolver_ClubBranch_NoCountry_UsesBareTemplate(t *testing.T) {
	wp := &fakeWP{
		hits: map[string][]wikipedia.Hit{
			"Barcelona football club": {
				{Title: "FC Barcelona", WikidataQID: "Q7156", Index: 1},
			},
		},
	}
	wd := &fakeWDLookup{
		p31: map[string][]string{"Q7156": {"Q103229495"}}, // men's association football team
	}
	r := alias.NewResolver(wd, wp)
	got, err := r.Resolve(context.Background(), alias.LookupInput{CanonicalName: "Barcelona"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q7156" {
		t.Errorf("QID = %q; want Q7156", got.QID)
	}
	if wp.calls[0] != "Barcelona football club" {
		t.Errorf("query = %q; want 'Barcelona football club' (nil-country fallback)", wp.calls[0])
	}
}

// Hits without a wikibase_item (article with no Wikidata sitelink) are
// silently skipped — they can't be P31-verified anyway.
func TestResolver_ClubBranch_HitWithoutQIDSkipped(t *testing.T) {
	wp := &fakeWP{
		hits: map[string][]wikipedia.Hit{
			"Foo France football club": {
				{Title: "Some Article", WikidataQID: "", Index: 1},
				{Title: "Foo FC", WikidataQID: "Q_valid", Index: 2},
			},
		},
	}
	wd := &fakeWDLookup{
		p31: map[string][]string{"Q_valid": {"Q476028"}},
	}
	r := alias.NewResolver(wd, wp)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "Foo",
		Country:       strPtr("France"),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q_valid" {
		t.Errorf("QID = %q; want Q_valid (empty-QID hit must be skipped)", got.QID)
	}
}

// SPARQL failure falls back to taking Wikipedia's top hit unconditionally
// rather than cascading to NoMatch. Wikipedia's ranking is good enough
// that even without type verification the top hit is usually right;
// preferring a possible wrong answer to a hard failure keeps daily
// resolution working through vendor blips.
func TestResolver_ClubBranch_SPARQLFailureFallsBackToTopHit(t *testing.T) {
	wp := &fakeWP{
		hits: map[string][]wikipedia.Hit{
			"Nice France football club": {
				{Title: "OGC Nice", WikidataQID: "Q185163", Index: 1},
			},
		},
	}
	wd := &fakeWDLookup{p31Err: errors.New("SPARQL blip")}
	r := alias.NewResolver(wd, wp)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "Nice",
		Country:       strPtr("France"),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q185163" {
		t.Errorf("QID = %q; want Q185163 (SPARQL blip → take Wikipedia's top hit)", got.QID)
	}
}

// Wikipedia returning zero hits surfaces as ErrNoMatch — the caller
// treats this as "team unresolvable; retry next cycle".
func TestResolver_ClubBranch_NoHits_ErrNoMatch(t *testing.T) {
	wp := &fakeWP{hits: map[string][]wikipedia.Hit{}}
	wd := &fakeWDLookup{}
	r := alias.NewResolver(wd, wp)
	_, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "Nothing",
		Country:       strPtr("Nowhere"),
	})
	if !errors.Is(err, alias.ErrNoMatch) {
		t.Errorf("err = %v; want ErrNoMatch", err)
	}
}

// National branch happy path: Wikipedia article title convention lets
// `{country} men's national football team` resolve near-deterministically.
func TestResolver_NationalBranch_HappyPath(t *testing.T) {
	wp := &fakeWP{
		hits: map[string][]wikipedia.Hit{
			"France men's national football team": {
				{Title: "France men's national football team", WikidataQID: "Q47774", Index: 1},
			},
		},
	}
	wd := &fakeWDLookup{
		p31: map[string][]string{"Q47774": {"Q135408445"}}, // men's national football team
	}
	r := alias.NewResolver(wd, wp)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "France",
		Country:       strPtr("France"),
		IsNational:    true,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q47774" {
		t.Errorf("QID = %q; want Q47774", got.QID)
	}
}

// USA gets substituted to "United States" per Wikipedia's article title
// convention ("United States men's national soccer team" etc.).
func TestResolver_NationalBranch_USAExpanded(t *testing.T) {
	wp := &fakeWP{
		hits: map[string][]wikipedia.Hit{
			"United States men's national football team": {
				{Title: "United States men's national soccer team", WikidataQID: "Q_us_national", Index: 1},
			},
		},
	}
	wd := &fakeWDLookup{
		p31: map[string][]string{"Q_us_national": {"Q135408445"}},
	}
	r := alias.NewResolver(wd, wp)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "USA",
		IsNational:    true,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q_us_national" {
		t.Errorf("QID = %q; want Q_us_national", got.QID)
	}
	if wp.calls[0] != "United States men's national football team" {
		t.Errorf("query = %q; want USA → 'United States' substituted", wp.calls[0])
	}
}

// National branch: Country takes precedence over name (real ingest often
// has CanonicalName == team.name == 'England' both — but the country
// field is the authoritative signal). Also tests women's-team rejection
// via P31 reject-set.
func TestResolver_NationalBranch_P31RejectsWomensNational(t *testing.T) {
	wp := &fakeWP{
		hits: map[string][]wikipedia.Hit{
			"England men's national football team": {
				{Title: "England women's national football team", WikidataQID: "Q_eng_w", Index: 1},
				{Title: "England men's national football team", WikidataQID: "Q47762", Index: 2},
			},
		},
	}
	wd := &fakeWDLookup{
		p31: map[string][]string{
			"Q_eng_w": {"Q6997908"},   // women's national → reject
			"Q47762":  {"Q135408445"}, // men's national → accept
		},
	}
	r := alias.NewResolver(wd, wp)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "England",
		Country:       strPtr("England"),
		IsNational:    true,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q47762" {
		t.Errorf("QID = %q; want Q47762 (women's P31-rejected)", got.QID)
	}
}
