// Fast-fail + happy-path tests for the Wikidata SPARQL client.
package wikidata_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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
