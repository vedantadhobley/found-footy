// Unit tests for the Wikipedia adapter.
package wikipedia_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

func newFixture() (*wikipedia.Instruments, *logging.TestEmitter) {
	log := &logging.TestEmitter{}
	return wikipedia.RegisterMetrics(metrics.New(), log), log
}

func TestNewClient_FastFailGuards(t *testing.T) {
	ins, _ := newFixture()

	cases := []struct {
		name string
		cfg  config.WikipediaConfig
		ins  *wikipedia.Instruments
	}{
		{"nil-ins", config.WikipediaConfig{Host: "http://x", UserAgent: "test"}, nil},
		{"empty-host", config.WikipediaConfig{Host: "", UserAgent: "test"}, ins},
		{"empty-user-agent", config.WikipediaConfig{Host: "http://x", UserAgent: ""}, ins},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := wikipedia.NewClient(tc.cfg, tc.ins); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// SearchAndResolve builds the correct MediaWiki API URL, parses the
// pages map, and sorts hits by CirrusSearch's rank index.
func TestSearchAndResolve_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/w/api.php" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("generator") != "search" {
			t.Errorf("generator = %q, want search", q.Get("generator"))
		}
		if q.Get("gsrsearch") != "Nice France football club" {
			t.Errorf("gsrsearch = %q, want the composed 3-part club template", q.Get("gsrsearch"))
		}
		if q.Get("prop") != "pageprops" {
			t.Errorf("prop = %q, want pageprops (for wikibase_item extraction)", q.Get("prop"))
		}
		if r.Header.Get("User-Agent") == "" || r.Header.Get("User-Agent") == "Go-http-client/1.1" {
			t.Errorf("User-Agent must be set to identify caller; got %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		// Deliberately out-of-order pages map keys — the client must
		// re-sort by index (Wikipedia's rank position).
		_, _ = w.Write([]byte(`{
			"query": {
				"pages": {
					"999": {
						"pageid": 999,
						"title": "2016 Nice truck attack",
						"index": 2,
						"pageprops": { "wikibase_item": "Q25893254" }
					},
					"185163": {
						"pageid": 185163,
						"title": "OGC Nice",
						"index": 1,
						"pageprops": { "wikibase_item": "Q185163" }
					},
					"3054090": {
						"pageid": 3054090,
						"title": "List of football clubs in France",
						"index": 3,
						"pageprops": { "wikibase_item": "Q3054090" }
					}
				}
			}
		}`))
	}))
	defer srv.Close()

	ins, log := newFixture()
	c, err := wikipedia.NewClient(config.WikipediaConfig{
		Host: srv.URL, UserAgent: "found-footy-test", Timeout: 5 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	hits, err := c.SearchAndResolve(context.Background(), "Nice France football club", wikipedia.SearchOpts{})
	if err != nil {
		t.Fatalf("SearchAndResolve: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d; want 3", len(hits))
	}
	// Sorted by CirrusSearch's `index` — OGC Nice was at index=1 in the
	// response, must be first in the returned slice regardless of the
	// randomized JSON map iteration.
	if hits[0].Title != "OGC Nice" || hits[0].WikidataQID != "Q185163" {
		t.Errorf("hits[0] = %+v; want OGC Nice / Q185163", hits[0])
	}
	if hits[1].Title != "2016 Nice truck attack" {
		t.Errorf("hits[1] = %+v; want the truck-attack article (index=2)", hits[1])
	}
	if !log.HasAction(vocabulary.ModuleInfraWikipedia, vocabulary.ActionWikipediaSearch) {
		t.Errorf("expected ActionWikipediaSearch emission; got %+v", log.Snapshot())
	}
}

// A hit with no wikibase_item in pageprops (article without a Wikidata
// sitelink) surfaces with empty QID — callers filter these before
// P31 batch verify.
func TestSearchAndResolve_HitWithoutWikidataSitelink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query": {
				"pages": {
					"1": { "pageid": 1, "title": "Some article", "index": 1, "pageprops": {} }
				}
			}
		}`))
	}))
	defer srv.Close()

	ins, _ := newFixture()
	c, _ := wikipedia.NewClient(config.WikipediaConfig{
		Host: srv.URL, UserAgent: "found-footy-test", Timeout: 5 * time.Second,
	}, ins)
	hits, err := c.SearchAndResolve(context.Background(), "whatever", wikipedia.SearchOpts{})
	if err != nil {
		t.Fatalf("SearchAndResolve: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d; want 1", len(hits))
	}
	if hits[0].WikidataQID != "" {
		t.Errorf("hits[0].WikidataQID = %q; want empty for missing wikibase_item", hits[0].WikidataQID)
	}
}

// Non-2xx / MediaWiki error responses surface as errors with the
// wikipedia_search_failed action emitted.
func TestSearchAndResolve_Non2xxSurfacesFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ins, log := newFixture()
	c, _ := wikipedia.NewClient(config.WikipediaConfig{
		Host: srv.URL, UserAgent: "found-footy-test", Timeout: 5 * time.Second,
	}, ins)
	_, err := c.SearchAndResolve(context.Background(), "whatever", wikipedia.SearchOpts{})
	if err == nil {
		t.Fatal("expected non-2xx error, got nil")
	}
	if !log.HasAction(vocabulary.ModuleInfraWikipedia, vocabulary.ActionWikipediaSearchFailed) {
		t.Errorf("expected ActionWikipediaSearchFailed emission")
	}
}

func TestSearchAndResolve_MediaWikiAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"code":"badparam","info":"gsrsearch is required"}}`))
	}))
	defer srv.Close()

	ins, log := newFixture()
	c, _ := wikipedia.NewClient(config.WikipediaConfig{
		Host: srv.URL, UserAgent: "found-footy-test", Timeout: 5 * time.Second,
	}, ins)
	_, err := c.SearchAndResolve(context.Background(), "whatever", wikipedia.SearchOpts{})
	if err == nil {
		t.Fatal("expected MediaWiki API error, got nil")
	}
	if !strings.Contains(err.Error(), "badparam") {
		t.Errorf("error missing api code context: %v", err)
	}
	if !log.HasAction(vocabulary.ModuleInfraWikipedia, vocabulary.ActionWikipediaSearchFailed) {
		t.Errorf("expected ActionWikipediaSearchFailed emission")
	}
}
