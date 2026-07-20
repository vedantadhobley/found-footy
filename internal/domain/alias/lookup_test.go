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
	// p31 maps QID → P31 type list. Nil → empty map. Used by BatchGetP31
	// to return canned type-lists for the lookup pipeline's filter step.
	// Tests that don't set this get an "empty types → filter drops
	// everything" behavior, which matches ErrNoMatch in most scenarios.
	p31            map[string][]string
	batchP31Calls  [][]string
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

func (f *fakeWD) BatchGetP31(_ context.Context, qids []string) (map[string][]string, error) {
	f.mu.Lock()
	f.batchP31Calls = append(f.batchP31Calls, append([]string(nil), qids...))
	table := f.p31
	f.mu.Unlock()
	out := make(map[string][]string, len(qids))
	if table == nil {
		return out, nil
	}
	for _, q := range qids {
		if t, ok := table[q]; ok {
			out[q] = append([]string(nil), t...)
		}
	}
	return out, nil
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

// Club branch: constructs the full 9-variant search set. Candidates
// are collected across variants, then P31-filtered, then scored;
// city-match short-circuit fires on the first survivor with score
// ≥200 (avoids scoring the rest).
func TestResolver_ClubBranch_CityMatchShortCircuits(t *testing.T) {
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
		// P31 accept: Q476028 (association football club) puts Liverpool
		// through the type filter.
		p31: map[string][]string{
			"Q1130849": {"Q476028"},
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
	// One batch P31 call fires per Resolve (over all deduped candidates).
	if len(fake.batchP31Calls) != 1 {
		t.Errorf("BatchGetP31 called %d times; want 1 (single batch across variants)", len(fake.batchP31Calls))
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

// P31 filter: even when a candidate's description contains "football",
// a non-club P31 (TV channel, stadium, etc.) drops it. This is the
// marquee AC Milan / Milan TV regression case — Q2478275 (Milan TV)
// has description "subscription-based television channel operated by
// Italian football club AC Milan" and would pass the old text-based
// isFootball check, but its P31 is Q2001305 (television channel) not
// Q476028 (association football club).
func TestResolver_ClubBranch_P31RejectsTelevisionChannel(t *testing.T) {
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			return []wikidata.SearchHit{
				{ID: "Q1543", Label: "AC Milan", Description: "football club in Milan, Italy"},
				{ID: "Q2478275", Label: "Milan TV", Description: "subscription-based television channel operated by Italian football club AC Milan"},
			}, nil
		},
		p31: map[string][]string{
			"Q1543":    {"Q476028", "Q103229495"}, // association football club + men's team
			"Q2478275": {"Q2001305", "Q561068"},   // television channel + specialty channel — no accept type
		},
	}
	r := alias.NewResolver(fake, nil)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "AC Milan",
		Country:       strPtr("Italy"),
		City:          strPtr("Milano"),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q1543" {
		t.Errorf("QID = %q, want Q1543 (Milan TV must be P31-rejected)", got.QID)
	}
}

// P31 reject-set: candidates whose P31 includes reserve-team
// (Q2412834) or women's-club (Q51481377) types are dropped even when
// they ALSO have an accept type. Verifies the reject-set discipline
// against Milan Futuro-style entities.
func TestResolver_ClubBranch_P31RejectsReserveTeam(t *testing.T) {
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			return []wikidata.SearchHit{
				{ID: "Q1543", Label: "AC Milan", Description: "football club in Milan, Italy"},
				{ID: "Q126923253", Label: "Milan Futuro", Description: "senior side of AC Milan's reserves"},
			}, nil
		},
		p31: map[string][]string{
			"Q1543":      {"Q476028"},              // accept
			"Q126923253": {"Q2412834", "Q476028"}, // accept + reject → reject wins
		},
	}
	r := alias.NewResolver(fake, nil)
	got, err := r.Resolve(context.Background(), alias.LookupInput{
		CanonicalName: "AC Milan",
		City:          strPtr("Milano"),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.QID != "Q1543" {
		t.Errorf("QID = %q, want Q1543 (reserve team must be P31-rejected)", got.QID)
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
		p31: map[string][]string{
			"Q_short": {"Q476028"},
			"Q_long":  {"Q476028"},
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

// National branch: uses the 3 variants; candidates are collected
// across all variants, then batch-P31-filtered, then first survivor
// wins (national naming is unambiguous enough that further scoring
// adds noise).
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
		p31: map[string][]string{
			// Q135408445 = men's national association football team
			"Q47774": {"Q135408445"},
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
	// National branch runs all 3 variants + 1 batch P31 (candidates
	// deduped by QID across variants).
	if len(fake.batchP31Calls) != 1 {
		t.Errorf("BatchGetP31 called %d times; want 1 (single batch across variants)", len(fake.batchP31Calls))
	}
}

// National branch: women's national team filtered out via P31 reject
// (Q6997908 = women's national football team); men's team resolved
// instead.
func TestResolver_NationalBranch_FiltersWomensTeam(t *testing.T) {
	callIdx := 0
	fake := &fakeWD{
		searchHandler: func(term string, _ wikidata.SearchOpts) ([]wikidata.SearchHit, error) {
			callIdx++
			switch callIdx {
			case 1:
				return []wikidata.SearchHit{
					// Description drops "women"/"femen" keywords so the
					// pre-P31 skip-list doesn't catch this — pure P31
					// reject-set test.
					{ID: "Q_ladies", Label: "France ladies national football team", Description: "national association football team representing France (ladies)"},
				}, nil
			default:
				return []wikidata.SearchHit{
					{ID: "Q_men", Label: "France men's national football team", Description: "national football team representing France"},
				}, nil
			}
		},
		p31: map[string][]string{
			"Q_ladies": {"Q6997908"},  // women's national → reject
			"Q_men":    {"Q135408445"}, // men's national → accept
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
		t.Errorf("QID = %q, want Q_men (women's P31-rejected)", got.QID)
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
		p31: map[string][]string{
			"Q164134": {"Q135408445"},
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
