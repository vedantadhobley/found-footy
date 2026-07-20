// Tests for the alias-lookup pipeline. Uses a fake WikidataFetcher so
// the domain package stays free of live Wikidata dependencies in
// unit tests. An integration test in a separate file (skipped in
// -short) exercises real Wikidata for a small team roster.
package alias_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
)

// fakeWD is an in-memory fake for the WikidataFetcher interface.
//
// searchHandler receives (term, opts) and returns hits (or an error).
// entityHandler receives a QID and returns an *Entity (or nil).
// Empty handlers behave as "no hits" / "empty entity".
//
// Call counts are captured for assertions (e.g., "the resolver made
// N SearchEntities calls").
type fakeWD struct {
	mu             sync.Mutex
	searchHandler  func(term string, opts wikidata.SearchOpts) ([]wikidata.SearchHit, error)
	entityHandler  func(qid string) (*wikidata.Entity, error)
	searchCalls    []string
	entityCalls    []string
}

func (f *fakeWD) SearchEntities(_ context.Context, term string, opts wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
	f.mu.Lock()
	f.searchCalls = append(f.searchCalls, term)
	handler := f.searchHandler
	f.mu.Unlock()
	if handler == nil {
		return nil, nil
	}
	return handler(term, opts)
}

func (f *fakeWD) GetEntity(_ context.Context, qid string) (*wikidata.Entity, error) {
	f.mu.Lock()
	f.entityCalls = append(f.entityCalls, qid)
	handler := f.entityHandler
	f.mu.Unlock()
	if handler == nil {
		return nil, nil
	}
	return handler(qid)
}

func (f *fakeWD) searchCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.searchCalls)
}

// Resolve fast-fails on empty CanonicalName.
func TestResolver_Resolve_EmptyName(t *testing.T) {
	r := alias.NewResolver(&fakeWD{}, nil)
	_, err := r.Resolve(context.Background(), alias.LookupInput{CanonicalName: "   "})
	if err == nil {
		t.Fatal("expected error for empty CanonicalName, got nil")
	}
}

// Resolve returns ErrNoMatch when every variant search returns no
// candidates that pass filtering.
func TestResolver_Resolve_NoCandidates_ReturnsErrNoMatch(t *testing.T) {
	fake := &fakeWD{
		searchHandler: func(_ string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			return nil, nil
		},
	}
	r := alias.NewResolver(fake, nil)
	_, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "Some Obscure Club",
		Country:       strPtr("Someplace"),
		City:          strPtr("Somewhere"),
	})
	if !errors.Is(err, alias.ErrNoMatch) {
		t.Fatalf("err = %v, want ErrNoMatch", err)
	}
}

// Club branch: constructs the full 9-variant search set and only the
// first city-matching candidate wins (short-circuit).
func TestResolver_ClubBranch_CityMatchShortCircuits(t *testing.T) {
	// Fake wbsearchentities returns a single football-club candidate
	// whose description mentions the city. The scoring +200 city
	// bonus alone triggers the short-circuit return, so we should
	// only see ONE SearchEntities call (the first variant).
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			return []wikidata.SearchHit{
				{
					ID:          "Q1130849",
					Label:       "Liverpool F.C.",
					Description: "association football club in Liverpool, England",
				},
			}, nil
		},
	}
	r := alias.NewResolver(fake, nil)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "Liverpool",
		Country:       strPtr("England"),
		City:          strPtr("Liverpool"),
		IsNational:    false,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q1130849" {
		t.Errorf("QID = %q, want Q1130849", got.QID)
	}
	if got.Score < 200 {
		t.Errorf("Score = %d, want ≥200 (city-match short-circuit)", got.Score)
	}
	// Country-variations lookup for "England" hits SearchEntities once,
	// then the first club variant short-circuits. Total = 2. Assert on
	// the club-search count (terms containing "FC" or "football").
	clubSearches := 0
	for _, term := range fake.searchCalls {
		if strings.Contains(term, "FC") || strings.Contains(term, "football") ||
			strings.Contains(term, "Liverpool") {
			clubSearches++
		}
	}
	if clubSearches != 1 {
		t.Errorf("club-variant searches = %d; city-match short-circuit should stop after 1", clubSearches)
	}
}

// Club branch: candidates without football/soccer in description
// get filtered.
func TestResolver_ClubBranch_FiltersNonFootballCandidates(t *testing.T) {
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			// Return candidates whose descriptions contain neither
			// "football" nor "soccer" nor "multisport".
			return []wikidata.SearchHit{
				{ID: "Q1", Label: "Something", Description: "a book about sports"},
				{ID: "Q2", Label: "Other", Description: "a movie"},
				{ID: "Q3", Label: "Third", Description: "a person"},
			}, nil
		},
	}
	r := alias.NewResolver(fake, nil)
	_, err := r.Resolve(context.Background(), alias.LookupInput{CanonicalName: "Anonymous"})
	if !errors.Is(err, alias.ErrNoMatch) {
		t.Errorf("err = %v, want ErrNoMatch (all candidates non-football)", err)
	}
}

// Club branch: women's / reserve / youth teams filtered.
func TestResolver_ClubBranch_FiltersWomensAndReserveTeams(t *testing.T) {
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			return []wikidata.SearchHit{
				{ID: "Q1", Label: "Real Madrid Femenino", Description: "women's football club in Madrid"},
				{ID: "Q2", Label: "Real Madrid Castilla", Description: "reserve football team of Real Madrid"},
				{ID: "Q3", Label: "Real Madrid U-19", Description: "youth football team"},
			}, nil
		},
	}
	r := alias.NewResolver(fake, nil)
	_, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "Real Madrid",
		Country:       strPtr("Spain"),
	})
	if !errors.Is(err, alias.ErrNoMatch) {
		t.Errorf("err = %v, want ErrNoMatch (all candidates women/reserve/youth)", err)
	}
}

// Club branch: B-team labels (Real Madrid B) filtered by label
// suffix.
func TestResolver_ClubBranch_FiltersBTeamLabels(t *testing.T) {
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			return []wikidata.SearchHit{
				{ID: "Q1", Label: "Real Madrid B", Description: "association football club in Madrid, Spain"},
			}, nil
		},
	}
	r := alias.NewResolver(fake, nil)
	_, err := r.Resolve(context.Background(), alias.LookupInput{CanonicalName: "Real Madrid"})
	if !errors.Is(err, alias.ErrNoMatch) {
		t.Errorf("err = %v, want ErrNoMatch (B-team label)", err)
	}
}

// Club branch: with no city match but a country match, highest
// scoring candidate wins (via description-length base score + country
// bonus). Requires all variants to run since no short-circuit fires.
func TestResolver_ClubBranch_CountryMatchScoring(t *testing.T) {
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			// Return two candidates on every variant: one with a
			// Spanish-country match in its description, one without.
			return []wikidata.SearchHit{
				{ID: "Q_short", Label: "X FC", Description: "association football club"},
				{ID: "Q_long", Label: "X FC", Description: "Spanish association football club based in a Spanish city"},
			}, nil
		},
	}
	r := alias.NewResolver(fake, nil)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "X",
		Country:       strPtr("Spain"),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q_long" {
		t.Errorf("QID = %q, want Q_long (longer description + country match)", got.QID)
	}
}

// National branch: uses the 3 variants + first valid candidate wins.
func TestResolver_NationalBranch_FirstValidWins(t *testing.T) {
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			return []wikidata.SearchHit{
				{
					ID:          "Q47774",
					Label:       "France national football team",
					Description: "men's national association football team representing France",
				},
			}, nil
		},
	}
	r := alias.NewResolver(fake, nil)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "France",
		IsNational:    true,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q47774" {
		t.Errorf("QID = %q, want Q47774", got.QID)
	}
	// National branch made ONE search call — first valid hit wins so
	// the other two variants weren't tried.
	if fake.searchCallCount() != 1 {
		t.Errorf("SearchEntities called %d times; first-valid should stop after 1", fake.searchCallCount())
	}
}

// National branch: women's national team filtered out; men's team
// resolved instead on a later variant.
func TestResolver_NationalBranch_FiltersWomensTeam(t *testing.T) {
	// First variant returns only the women's team → filtered.
	// Second variant (adds "men's") returns the men's team → wins.
	callIdx := 0
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			callIdx++
			switch callIdx {
			case 1:
				return []wikidata.SearchHit{
					{ID: "Q_women", Label: "France women's national football team", Description: "women's national football team representing France"},
				}, nil
			default:
				return []wikidata.SearchHit{
					{ID: "Q_men", Label: "France men's national football team", Description: "men's national football team representing France"},
				}, nil
			}
		},
	}
	r := alias.NewResolver(fake, nil)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "France",
		IsNational:    true,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q_men" {
		t.Errorf("QID = %q, want Q_men (women's filtered)", got.QID)
	}
}

// National branch: bare "USA" gets substituted to "United States" in
// search variants (Wikidata quirk — the US team is indexed under the
// long form).
func TestResolver_NationalBranch_USAExpandedToUnitedStates(t *testing.T) {
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			return []wikidata.SearchHit{
				{
					ID:          "Q164134",
					Label:       "United States men's national soccer team",
					Description: "men's national association football (soccer) team representing the USA",
				},
			}, nil
		},
	}
	r := alias.NewResolver(fake, nil)
	_, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "USA",
		IsNational:    true,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Sanity: first search term should NOT be literal "USA national ..."
	// (the substitution should have happened).
	if !strings.Contains(fake.searchCalls[0], "United States") {
		t.Errorf("first search term = %q, expected substitution to 'United States ...'", fake.searchCalls[0])
	}
}

// CountryVariations cache: hits Wikidata only on cache miss.
func TestCountryVariations_Caches(t *testing.T) {
	entityCalls := 0
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			return []wikidata.SearchHit{
				{ID: "Q29", Label: "Spain", Description: "country in southwestern Europe"},
			}, nil
		},
		entityHandler: func(qid string) (*wikidata.Entity, error) {
			entityCalls++
			// Return an empty entity — cache should still populate.
			return &wikidata.Entity{QID: qid}, nil
		},
	}
	cv := alias.NewCountryVariations(fake)
	ctx := context.Background()

	first := cv.For(ctx, "Spain")
	second := cv.For(ctx, "Spain")
	// Both calls return same variations (from cache).
	if len(first) == 0 {
		t.Fatalf("first call returned no variations")
	}
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Errorf("cached call returned different variations: first=%v second=%v", first, second)
	}
	// GetEntity only called once — second is a cache hit.
	if entityCalls != 1 {
		t.Errorf("GetEntity called %d times, want 1 (cache hit on second)", entityCalls)
	}
}

// CountryVariations always includes the country name itself as a
// variation — even if Wikidata is down.
func TestCountryVariations_FallsBackToCountryNameOnWikidataFailure(t *testing.T) {
	fake := &fakeWD{
		searchHandler: func(_ string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			return nil, errors.New("wikidata down")
		},
	}
	cv := alias.NewCountryVariations(fake)
	got := cv.For(context.Background(), "Spain")
	if len(got) == 0 {
		t.Fatal("expected at least the country name itself as a fallback variation")
	}
	found := false
	for _, v := range got {
		if v == "spain" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("fallback variations = %v, missing 'spain'", got)
	}
}
