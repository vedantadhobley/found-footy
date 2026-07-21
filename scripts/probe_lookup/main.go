// scripts/probe_lookup/main.go — one-off probe: resolve a list of
// teams against live Wikidata via the alias.Resolver pipeline and
// print the raw output. NOT wired into tests — just a quick way to
// eyeball resolution quality against an ad-hoc roster.
//
// Usage (from repo root):
//
//	docker run --rm --network=host -v $PWD:/src -w /src \
//	  golang:1.25-bookworm go run ./scripts/probe_lookup
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

func strPtr(s string) *string { return &s }

// The probe roster — just the 3 that never resolved in earlier runs.
var roster = []alias.LookupInput{
	{CanonicalName: "Lyon", Country: strPtr("France"), City: strPtr("Lyon")},
	{CanonicalName: "Bayer Leverkusen", Country: strPtr("Germany"), City: strPtr("Leverkusen")},
	{CanonicalName: "Japan", IsNational: true},
}

func main() {
	log := &logging.TestEmitter{}
	reg := metrics.New()
	wdIns := wikidata.RegisterMetrics(reg, log)
	wd, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint:  "https://query.wikidata.org/sparql",
		WWWHost:   "https://www.wikidata.org",
		UserAgent: "FoundFooty/1.0 (research; https://github.com/vedantadhobley/found-footy) probe_lookup",
		Timeout:   15 * time.Second,
	}, wdIns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wikidata.NewClient: %v\n", err)
		os.Exit(1)
	}
	wpIns := wikipedia.RegisterMetrics(reg, log)
	wp, err := wikipedia.NewClient(config.WikipediaConfig{
		Host:      "https://en.wikipedia.org",
		UserAgent: "FoundFooty/1.0 (research; https://github.com/vedantadhobley/found-footy) probe_lookup",
		Timeout:   15 * time.Second,
	}, wpIns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wikipedia.NewClient: %v\n", err)
		os.Exit(1)
	}
	r := alias.NewResolver(wd, wp)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fmt.Printf("%-20s %-6s %-10s %-40s  %-6s  %s\n",
		"input", "type", "QID", "Wikidata label", "score", "description")
	fmt.Println("─────────────────────────────────────────────────────────────────────────────────────────────────────────────────")

	for i, in := range roster {
		if i > 0 {
			// Wikidata rate-limits wbsearchentities aggressively for
			// anonymous access. Each club team = 9 variants + optional
			// country entity fetch = ~10 HTTP calls. Empirically the
			// rate window is ~60s — even 20s between teams left half
			// the roster rate-limited. 45s is the sweet spot.
			time.Sleep(45 * time.Second)
		}
		// Reset search-failure trace for this team.
		log.Reset()

		start := time.Now()
		got, err := r.Resolve(ctx, in)
		elapsed := time.Since(start)

		teamType := "club"
		if in.IsNational {
			teamType = "nat"
		}
		if err != nil {
			if errors.Is(err, alias.ErrNoMatch) {
				// Was it "genuinely no candidates" or "rate-limited"?
				// Search-failure emissions on the log fixture tell us.
				failCount := 0
				for _, e := range log.Snapshot() {
					if e.Action == vocabulary.ActionWikipediaSearchFailed ||
						e.Action == vocabulary.ActionWikidataQueryFailed ||
						e.Action == vocabulary.ActionWikidataEntityFailed {
						failCount++
					}
				}
				note := "NO MATCH"
				if failCount > 0 {
					note = fmt.Sprintf("NO MATCH (%d wikidata errors — probably rate-limited)", failCount)
				}
				fmt.Printf("%-20s %-6s %-10s %-40s  %-6s\n",
					truncate(in.CanonicalName, 20), teamType, "—", note, "—")
			} else {
				fmt.Printf("%-20s %-6s %-10s ERROR: %v\n",
					truncate(in.CanonicalName, 20), teamType, "?", err)
			}
			continue
		}
		fmt.Printf("%-20s %-6s %-10s %-40s  %-6d  %s (%dms)\n",
			truncate(in.CanonicalName, 20),
			teamType,
			got.QID,
			truncate(got.Label, 40),
			got.Score,
			truncate(got.Description, 60),
			elapsed.Milliseconds(),
		)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
