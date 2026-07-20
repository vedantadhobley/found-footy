// Integration test — hits real Wikidata for a small curated roster.
// Skipped in -short mode.
//
// Purpose: verify the pipeline resolves real teams correctly against
// live Wikidata. This is the empirical guarantee that eval-set F1
// scores from `scratchpad/alias-eval.md` translate to prod behavior.
//
// Coverage set is intentionally small (a few clubs + a few nationals)
// so the test runs in a few seconds; expand only if a specific team's
// resolution regresses.
package alias_test

import (
	"context"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

// TestResolver_Integration_LiveWikidata resolves a handful of teams
// against real Wikidata and asserts the QIDs match hand-verified
// expectations. If Wikidata is unavailable or rate-limiting, tests
// SKIP (not FAIL) — we don't want a Wikidata hiccup to block CI.
func TestResolver_Integration_LiveWikidata(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	log := &logging.TestEmitter{}
	ins := wikidata.RegisterMetrics(metrics.New(), log)
	c, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint:  "https://query.wikidata.org/sparql",
		WWWHost:   "https://www.wikidata.org",
		UserAgent: "found-footy/dev (integration-test)",
		Timeout:   15 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	r := alias.NewResolver(c, nil)

	strPtr := func(s string) *string { return &s }

	cases := []struct {
		name     string
		in       alias.LookupInput
		wantQID  string
	}{
		{
			name: "Liverpool F.C.",
			in: alias.LookupInput{
				CanonicalName: "Liverpool",
				Country:       strPtr("England"),
				City:          strPtr("Liverpool"),
			},
			wantQID: "Q1130849",
		},
		{
			name: "Manchester United",
			in: alias.LookupInput{
				CanonicalName: "Manchester United",
				Country:       strPtr("England"),
				City:          strPtr("Manchester"),
			},
			wantQID: "Q18656",
		},
		{
			name: "France national team",
			in: alias.LookupInput{
				CanonicalName: "France",
				IsNational:    true,
			},
			wantQID: "Q47774",
		},
		{
			name: "Brazil national team",
			in: alias.LookupInput{
				CanonicalName: "Brazil",
				IsNational:    true,
			},
			wantQID: "Q83459",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(ctx, tc.in)
			if err != nil {
				// Skip on likely-transient wikidata errors rather than
				// fail — this is an external service test.
				t.Skipf("Wikidata unavailable / rate-limiting? Resolve: %v", err)
			}
			if got.QID != tc.wantQID {
				t.Errorf("Resolve QID = %q, want %q (label=%q description=%q score=%d)",
					got.QID, tc.wantQID, got.Label, got.Description, got.Score)
			}
		})
	}
}
