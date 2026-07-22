// scripts/probe_query/main.go — audit the Discovery query builder
// against REAL pipeline aliases (not hand-crafted stand-ins). For each
// (team, hypothetical player) pair, resolves the team via the live
// alias pipeline (Wikipedia CirrusSearch + Wikidata SPARQL), then
// runs discovery.Build with the actual alias tokens and prints the
// resulting query.
//
// This is the sanity-check probe — replaces the earlier hand-crafted
// version that was testing what I THOUGHT the pipeline produces, not
// what it actually does.
//
// Usage (from repo root):
//
//	docker run --rm --network=host -v $PWD:/src -w /src \
//	  golang:1.25-bookworm go run ./scripts/probe_query
//
// Requires internet access for Wikipedia/Wikidata calls (~2s per team,
// throttled). Runs offline for the query-composition part; only the
// alias resolution needs network.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/domain/discovery"
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

func strPtr(s string) *string { return &s }

// probe pairs a real team spec with hypothetical player names that
// exercise tokenizer edge cases.
type probe struct {
	Team          string
	Country       string
	City          string
	National      bool
	Players       []string // real player names representative of the roster
}

var roster = []probe{
	{
		Team: "Liverpool", Country: "England", City: "Liverpool",
		Players: []string{"M. Salah", "Virgil van Dijk", "Trent Alexander-Arnold"},
	},
	{
		Team: "Manchester City", Country: "England", City: "Manchester",
		Players: []string{"K. De Bruyne", "Rúben Dias", "Nico O'Reilly"},
	},
	{
		Team: "Real Madrid", Country: "Spain", City: "Madrid",
		Players: []string{"Vinícius Júnior", "Kylian Mbappé", "Rodrygo"},
	},
	{
		Team: "Bayer Leverkusen", Country: "Germany", City: "Leverkusen",
		Players: []string{"Florian Wirtz", "Granit Xhaka"},
	},
	{
		Team: "Wolverhampton Wanderers", Country: "England", City: "Wolverhampton",
		Players: []string{"Cunha", "H. Bueno"},
	},
	{
		Team: "Argentina", National: true, Country: "Argentina",
		Players: []string{"Enzo Fernández", "Rodrigo De Paul", "Lionel Messi"},
	},
	{
		Team: "Netherlands", National: true, Country: "Netherlands",
		Players: []string{"Donny van de Beek", "Virgil van Dijk"},
	},
}

func main() {
	log := &logging.TestEmitter{}
	reg := metrics.New()

	wd, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint:  "https://query.wikidata.org/sparql",
		WWWHost:   "https://www.wikidata.org",
		UserAgent: "FoundFooty/1.0 (research; https://github.com/vedantadhobley/found-footy) probe_query",
		Timeout:   20 * time.Second,
	}, wikidata.RegisterMetrics(reg, log))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wikidata.NewClient: %v\n", err)
		os.Exit(1)
	}
	wp, err := wikipedia.NewClient(config.WikipediaConfig{
		Host:      "https://en.wikipedia.org",
		UserAgent: "FoundFooty/1.0 (research; https://github.com/vedantadhobley/found-footy) probe_query",
		Timeout:   20 * time.Second,
	}, wikipedia.RegisterMetrics(reg, log))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wikipedia.NewClient: %v\n", err)
		os.Exit(1)
	}
	r := alias.NewResolver(wd, wp)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	fmt.Println("=== Discovery Query Builder — Real-Pipeline Audit ===")
	fmt.Println()

	maxLen := 0
	for i, p := range roster {
		if i > 0 {
			time.Sleep(2 * time.Second) // gentle throttle
		}
		fmt.Printf("═══ %s ═══\n", p.Team)

		in := alias.LookupInput{
			CanonicalName: p.Team,
			Country:       strPtr(p.Country),
			IsNational:    p.National,
		}
		if p.City != "" {
			in.City = strPtr(p.City)
		}
		lookup, err := r.Resolve(ctx, in)
		if err != nil {
			if errors.Is(err, alias.ErrNoMatch) {
				fmt.Printf("  ✗ Wikipedia NoMatch — will use canonical fallback only\n")
			} else {
				fmt.Printf("  ✗ ERROR: %v\n", err)
				continue
			}
		}

		var aliases []string
		if err == nil {
			selectIn := alias.SelectInput{
				QID:           lookup.QID,
				IsNational:    p.National,
				CanonicalName: p.Team,
			}
			if p.City != "" {
				selectIn.VenueCity = strPtr(p.City)
			}
			aliases, err = r.Select(ctx, selectIn)
			if err != nil {
				fmt.Printf("  ✗ Select ERROR: %v\n", err)
				continue
			}
			fmt.Printf("  Wikidata QID:  %s [%s]\n", lookup.QID, lookup.Label)
			fmt.Printf("  Aliases (%d):  {%s}\n", len(aliases), strings.Join(aliases, ", "))
		}

		for _, playerName := range p.Players {
			q, buildErr := discovery.Build(playerName, p.Team, aliases)
			if buildErr != nil {
				fmt.Printf("  Player %q → ERROR: %v\n", playerName, buildErr)
				continue
			}
			fmt.Printf("\n  Player: %q\n", playerName)
			fmt.Printf("  Query:  %s\n", q)
			fmt.Printf("  Length: %d chars", len(q))
			if discovery.LengthWarn(q) {
				fmt.Printf(" — WARN")
			}
			fmt.Println()
			if len(q) > maxLen {
				maxLen = len(q)
			}
		}
		fmt.Println()
	}

	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Longest query in audit: %d chars (warn threshold: %d)\n",
		maxLen, discovery.QueryLengthWarnThreshold)
}
