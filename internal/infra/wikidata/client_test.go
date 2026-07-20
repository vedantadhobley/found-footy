// Fast-fail + happy-path tests for the Wikidata HTTP client
// (SPARQL Query + wbsearchentities SearchEntities + Special:EntityData GetEntity).
package wikidata_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

func newFixture() (*wikidata.Instruments, *logging.TestEmitter) {
	log := &logging.TestEmitter{}
	return wikidata.RegisterMetrics(metrics.New(), log), log
}

func TestNewClient_FastFailGuards(t *testing.T) {
	ins, _ := newFixture()

	cases := []struct {
		name string
		cfg  config.WikidataConfig
		ins  *wikidata.Instruments
	}{
		{"nil-ins", config.WikidataConfig{Endpoint: "http://x", UserAgent: "test"}, nil},
		{"empty-endpoint", config.WikidataConfig{Endpoint: "", UserAgent: "test"}, ins},
		{"empty-user-agent", config.WikidataConfig{Endpoint: "http://x", UserAgent: ""}, ins},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := wikidata.NewClient(tc.cfg, tc.ins); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestQuery_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" || r.Header.Get("User-Agent") == "Go-http-client/1.1" {
			t.Errorf("Wikidata User-Agent not set / defaulted; got %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/sparql-results+json")
		_, _ = w.Write([]byte(`{"head":{"vars":["team"]},"results":{"bindings":[{"team":{"type":"uri","value":"http://wd/Q1"}}]}}`))
	}))
	defer srv.Close()

	ins, log := newFixture()
	c, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint: srv.URL, UserAgent: "found-footy-test", Timeout: 5 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	res, err := c.Query(context.Background(), "SELECT ?team WHERE {}")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Results.Bindings) != 1 {
		t.Errorf("bindings = %d, want 1", len(res.Results.Bindings))
	}
	if !log.HasAction(vocabulary.ModuleInfraWikidata, vocabulary.ActionWikidataQuery) {
		t.Errorf("expected ActionWikidataQuery; got %+v", log.Snapshot())
	}
}

// SearchEntities + GetEntity hit the MediaWiki host, not the SPARQL
// endpoint. To exercise them against a mock we need the mock to also
// answer the /w/api.php and /wiki/Special:EntityData/... paths. Set
// the SPARQL endpoint to the same server; requests are routed by
// URL path inside the mock handler.
//
// (In prod, SPARQL is query.wikidata.org and the other two are
// www.wikidata.org — separate hosts.)

func newRoutingMock(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for prefix, h := range handlers {
			if strings.HasPrefix(r.URL.Path, prefix) {
				h(w, r)
				return
			}
		}
		http.NotFound(w, r)
	}))
}

func TestSearchEntities_HappyPath(t *testing.T) {
	// wbsearchentities returns a JSON envelope with a "search" array.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/w/api.php" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("action") != "wbsearchentities" {
			t.Errorf("action = %q, want wbsearchentities", q.Get("action"))
		}
		if q.Get("search") != "Liverpool FC" {
			t.Errorf("search = %q, want 'Liverpool FC'", q.Get("search"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"search":[
			{"id":"Q1130849","label":"Liverpool F.C.","description":"association football club in Liverpool, England"},
			{"id":"Q1131189","label":"Liverpool F.C. (Montevideo)","description":"football club in Montevideo, Uruguay"}
		]}`))
	}))
	defer srv.Close()

	// Route requests to our mock by overriding the wikidata www host via
	// the client's default. The client hardcodes wikidataWWWHost; test
	// covers the path & params rather than the host. Point cfg.Endpoint
	// at the same server so at least the transport works — but the mock
	// only serves /w/api.php.
	ins, log := newFixture()
	c, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint: srv.URL, WWWHost: srv.URL, UserAgent: "found-footy-test", Timeout: 5 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	hits, err := c.SearchEntities(context.Background(), "Liverpool FC", wikidata.SearchOpts{})
	if err != nil {
		t.Fatalf("SearchEntities: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].ID != "Q1130849" {
		t.Errorf("hits[0].ID = %q, want Q1130849", hits[0].ID)
	}
	if !log.HasAction(vocabulary.ModuleInfraWikidata, vocabulary.ActionWikidataSearch) {
		t.Errorf("expected ActionWikidataSearch")
	}
}

func TestGetEntity_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/wiki/Special:EntityData/") {
			http.NotFound(w, r)
			return
		}
		// Minimal entity JSON: en label + fr alias + P17 (country) claim
		// + P1449 nickname + P1549 demonym.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entities":{"Q1130849":{
			"labels":{"en":{"language":"en","value":"Liverpool F.C."}},
			"aliases":{"fr":[{"language":"fr","value":"Les Reds"}]},
			"claims":{
				"P17":[{"mainsnak":{"datatype":"wikibase-item","datavalue":{"value":{"id":"Q145"}}}}],
				"P1449":[{"mainsnak":{"datatype":"monolingualtext","datavalue":{"value":{"text":"The Reds","language":"en"}}}}]
			}
		}}}`))
	}))
	defer srv.Close()

	ins, log := newFixture()
	c, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint: srv.URL, WWWHost: srv.URL, UserAgent: "found-footy-test", Timeout: 5 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ent, err := c.GetEntity(context.Background(), "Q1130849")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if ent.LabelEn() != "Liverpool F.C." {
		t.Errorf("LabelEn = %q, want 'Liverpool F.C.'", ent.LabelEn())
	}
	if a := ent.AliasesByLang(); len(a["fr"]) != 1 || a["fr"][0] != "Les Reds" {
		t.Errorf("AliasesByLang fr = %v", a["fr"])
	}
	if n := ent.NicknamesP1449(); len(n) != 1 || n[0] != "The Reds" {
		t.Errorf("NicknamesP1449 = %v", n)
	}
	if got := ent.FirstClaimQID("P17"); got != "Q145" {
		t.Errorf("FirstClaimQID(P17) = %q, want Q145", got)
	}
	if !log.HasAction(vocabulary.ModuleInfraWikidata, vocabulary.ActionWikidataEntityFetch) {
		t.Errorf("expected ActionWikidataEntityFetch")
	}
}

func TestGetEntity_EmptyQID(t *testing.T) {
	// Fast-fail before any HTTP.
	ins, _ := newFixture()
	c, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint: "http://x", UserAgent: "test", Timeout: time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.GetEntity(context.Background(), ""); err == nil {
		t.Error("GetEntity(\"\") should fast-fail, got nil error")
	}
}

func TestSearchEntities_Non2xxSurfacesFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ins, log := newFixture()
	c, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint: srv.URL, WWWHost: srv.URL, UserAgent: "found-footy-test", Timeout: 5 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.SearchEntities(context.Background(), "anything", wikidata.SearchOpts{}); err == nil {
		t.Error("expected non-2xx error, got nil")
	}
	if !log.HasAction(vocabulary.ModuleInfraWikidata, vocabulary.ActionWikidataSearchFailed) {
		t.Errorf("expected ActionWikidataSearchFailed")
	}
}

// BatchGetP31 sends the QIDs as a VALUES clause and returns a
// QID → P31 map keyed on the last path segment of each URI.
func TestBatchGetP31_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sparql := q.Get("query")
		if !strings.Contains(sparql, "wd:Q1543") || !strings.Contains(sparql, "wd:Q2478275") {
			t.Errorf("SPARQL missing expected VALUES: %s", sparql)
		}
		if !strings.Contains(sparql, "wdt:P31") {
			t.Errorf("SPARQL missing wdt:P31 predicate: %s", sparql)
		}
		w.Header().Set("Content-Type", "application/sparql-results+json")
		_, _ = w.Write([]byte(`{
			"head":{"vars":["item","type"]},
			"results":{"bindings":[
				{"item":{"type":"uri","value":"http://www.wikidata.org/entity/Q1543"},
				 "type":{"type":"uri","value":"http://www.wikidata.org/entity/Q476028"}},
				{"item":{"type":"uri","value":"http://www.wikidata.org/entity/Q1543"},
				 "type":{"type":"uri","value":"http://www.wikidata.org/entity/Q103229495"}},
				{"item":{"type":"uri","value":"http://www.wikidata.org/entity/Q2478275"},
				 "type":{"type":"uri","value":"http://www.wikidata.org/entity/Q2001305"}}
			]}
		}`))
	}))
	defer srv.Close()

	ins, _ := newFixture()
	c, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint: srv.URL, UserAgent: "found-footy-test", Timeout: 5 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := c.BatchGetP31(context.Background(), []string{"Q1543", "Q2478275"})
	if err != nil {
		t.Fatalf("BatchGetP31: %v", err)
	}
	if len(got["Q1543"]) != 2 {
		t.Errorf("Q1543 P31s = %v; want 2 entries", got["Q1543"])
	}
	if len(got["Q2478275"]) != 1 || got["Q2478275"][0] != "Q2001305" {
		t.Errorf("Q2478275 P31s = %v; want [Q2001305]", got["Q2478275"])
	}
}

func TestBatchGetP31_EmptyInput(t *testing.T) {
	ins, _ := newFixture()
	c, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint: "http://x", UserAgent: "found-footy-test", Timeout: 5 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := c.BatchGetP31(context.Background(), nil)
	if err != nil {
		t.Fatalf("BatchGetP31(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil input should return empty map; got %+v", got)
	}
}

