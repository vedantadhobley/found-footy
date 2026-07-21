// scripts/probe_aliases/main.go — audit alias quality against
// fan-known nicknames. Runs Resolver.Resolve → Resolver.Select for a
// hand-crafted roster of famous clubs + nationals and prints the
// resulting alias tokens.
//
// Usage (from repo root):
//
//	docker run --rm --network=host -v $PWD:/src -w /src \
//	  golang:1.25-bookworm go run ./scripts/probe_aliases
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
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

func strPtr(s string) *string { return &s }

// probe carries a team + the fan-known nicknames we EXPECT to see in
// the output, for readable audit lines ("kept: X | missing: Y").
type probe struct {
	Name     string
	Country  string
	City     string
	National bool
	// Expected: tokens (lowercased) fans commonly use for this team.
	// Not an exhaustive list — just the marquee ones. Missing =
	// coverage gap; present = pipeline captured the fan vocabulary.
	Expected []string
}

var roster = []probe{
	// Big English clubs — heavy fan-nickname usage
	{Name: "Arsenal", Country: "England", City: "London", Expected: []string{"arsenal", "gunners", "afc"}},
	{Name: "Liverpool", Country: "England", City: "Liverpool", Expected: []string{"liverpool", "reds", "lfc"}},
	{Name: "Manchester City", Country: "England", City: "Manchester", Expected: []string{"city", "manchester", "cityzens", "sky", "blues", "mcfc"}},
	{Name: "Manchester United", Country: "England", City: "Manchester", Expected: []string{"united", "manchester", "utd", "mufc", "devils", "red"}},
	{Name: "Tottenham", Country: "England", City: "London", Expected: []string{"tottenham", "spurs", "thfc", "coys"}},

	// Big continental clubs
	{Name: "Real Madrid", Country: "Spain", City: "Madrid", Expected: []string{"real", "madrid", "blancos", "merengues"}},
	{Name: "Barcelona", Country: "Spain", City: "Barcelona", Expected: []string{"barcelona", "barca", "blaugrana", "culers", "fcb"}},
	{Name: "Bayern Munich", Country: "Germany", City: "Munich", Expected: []string{"bayern", "fcb", "munchen", "roten"}},
	{Name: "Paris Saint Germain", Country: "France", City: "Paris", Expected: []string{"psg", "paris", "parisiens"}},

	// Nationals — should get demonyms + fan nicknames
	{Name: "Argentina", National: true, Country: "Argentina", Expected: []string{"argentina", "argentine", "argentinian", "albiceleste"}},
	{Name: "Brazil", National: true, Country: "Brazil", Expected: []string{"brazil", "brazilian", "selecao", "canarinho"}},
	{Name: "France", National: true, Country: "France", Expected: []string{"france", "french", "bleus"}},
	{Name: "Germany", National: true, Country: "Germany", Expected: []string{"germany", "german", "mannschaft"}},
	{Name: "England", National: true, Country: "England", Expected: []string{"england", "english", "lions", "three"}},
	{Name: "Netherlands", National: true, Country: "Netherlands", Expected: []string{"netherlands", "dutch", "oranje", "elftal"}},
	{Name: "Norway", National: true, Country: "Norway", Expected: []string{"norway", "norwegian"}},

	// Cape Verde — api-football calls it "Cape Verde Islands"
	{Name: "Cape Verde Islands", National: true, Country: "Cape-Verde-Islands", Expected: []string{"cape", "verde", "cabo", "tubaroes"}},
}

func main() {
	log := &logging.TestEmitter{}
	reg := metrics.New()
	wdIns := wikidata.RegisterMetrics(reg, log)
	wd, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint:  "https://query.wikidata.org/sparql",
		WWWHost:   "https://www.wikidata.org",
		UserAgent: "FoundFooty/1.0 (research; https://github.com/vedantadhobley/found-footy) probe_aliases",
		Timeout:   20 * time.Second,
	}, wdIns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wikidata.NewClient: %v\n", err)
		os.Exit(1)
	}
	wpIns := wikipedia.RegisterMetrics(reg, log)
	wp, err := wikipedia.NewClient(config.WikipediaConfig{
		Host:      "https://en.wikipedia.org",
		UserAgent: "FoundFooty/1.0 (research; https://github.com/vedantadhobley/found-footy) probe_aliases",
		Timeout:   20 * time.Second,
	}, wpIns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wikipedia.NewClient: %v\n", err)
		os.Exit(1)
	}
	r := alias.NewResolver(wd, wp)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	for i, p := range roster {
		if i > 0 {
			time.Sleep(2 * time.Second) // gentle throttle
		}
		fmt.Printf("\n─── %s (%s) %s ───\n", p.Name, ternary(p.National, "national", "club"), p.Country)

		in := alias.LookupInput{
			CanonicalName: p.Name,
			Country:       strPtr(p.Country),
			IsNational:    p.National,
		}
		if p.City != "" {
			in.City = strPtr(p.City)
		}

		lookup, err := r.Resolve(ctx, in)
		if err != nil {
			if errors.Is(err, alias.ErrNoMatch) {
				fmt.Printf("  ✗ NoMatch\n")
			} else {
				fmt.Printf("  ✗ ERROR: %v\n", err)
			}
			continue
		}
		fmt.Printf("  → %s  [%s]\n", lookup.QID, lookup.Label)

		selectIn := alias.SelectInput{
			QID:           lookup.QID,
			IsNational:    p.National,
			CanonicalName: p.Name,
		}
		if p.City != "" {
			selectIn.VenueCity = strPtr(p.City)
		}
		aliases, err := r.Select(ctx, selectIn)
		if err != nil {
			fmt.Printf("  ✗ Select ERROR: %v\n", err)
			continue
		}

		aliasSet := make(map[string]struct{}, len(aliases))
		for _, a := range aliases {
			aliasSet[a] = struct{}{}
		}

		got := "{" + strings.Join(aliases, ", ") + "}"
		fmt.Printf("  aliases (%d): %s\n", len(aliases), got)

		// Expected coverage report
		var hit, miss []string
		for _, exp := range p.Expected {
			if _, ok := aliasSet[exp]; ok {
				hit = append(hit, exp)
			} else {
				miss = append(miss, exp)
			}
		}
		if len(hit) > 0 {
			fmt.Printf("  ✓ have: %s\n", strings.Join(hit, ", "))
		}
		if len(miss) > 0 {
			fmt.Printf("  ✗ MISSING: %s\n", strings.Join(miss, ", "))
		}
	}
}

func ternary[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}
