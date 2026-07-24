// scripts/resolver_roster_test/main.go — resolves EVERY team in
// tracked_teams_cache through the alias Resolver and reports the chosen
// Wikidata entity. Validates the 2026-07-24 name-match selection fix
// (does Sporting CP land on the senior side now?) across the whole roster,
// and flags any team whose resolved label looks like a B/reserve/variant
// for human review.
//
// Resolve-only (no Select) — the fix is about ENTITY selection, and
// Resolve is the step it changed; this keeps Wikidata load light.
//
// Run (from the worker container — has pg + internet):
//
//	docker exec -e PG_URL='postgres://ffuser:CHANGE_ME@postgres:5432/found_footy?sslmode=disable' \
//	  found-footy-dev-worker sh -c 'cd /src && go run ./scripts/resolver_roster_test'
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

// leagueCountry maps the tracked league IDs to the country term used for
// disambiguation. League 1 is the national-teams competition.
var leagueCountry = map[int]string{
	39: "England", 40: "England", 45: "England",
	140: "Spain", 141: "Spain",
	78: "Germany", 79: "Germany",
	135: "Italy",
	61: "France",
}

// variantMarkers flag a resolved label that looks like a reserve / youth /
// women / B side rather than a senior first team — surfaced for review.
var variantMarkers = []string{
	" b", " ii", " iii", " c", "reserve", "youth", "academy", "women",
	"(w)", "femen", "femin", "u18", "u19", "u20", "u21", "u23", "castilla",
	"futuro", "atletic ", // Barça Atlètic-style reserve names
}

func strPtr(s string) *string { return &s }

type team struct {
	id       int
	name     string
	leagueID int
}

func main() {
	pgURL := os.Getenv("PG_URL")
	if pgURL == "" {
		pgURL = "postgres://ffuser:ffpass@postgres:5432/found_footy?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	conn, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pg connect: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx,
		`SELECT team_id, team_name, league_id FROM tracked_teams_cache ORDER BY league_id, team_name`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query roster: %v\n", err)
		os.Exit(1)
	}
	var roster []team
	for rows.Next() {
		var t team
		if err := rows.Scan(&t.id, &t.name, &t.leagueID); err != nil {
			fmt.Fprintf(os.Stderr, "scan: %v\n", err)
			os.Exit(1)
		}
		roster = append(roster, t)
	}
	rows.Close()

	r := newResolver()

	out, err := os.Create("/src/scratch-resolver-roster.txt")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create output: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = out.Close() }()
	report := func(line string) {
		fmt.Println(line)
		fmt.Fprintln(out, line)
	}

	report(fmt.Sprintf("resolver roster test — %d teams", len(roster)))
	var flagged, noMatch, errored int
	for i, t := range roster {
		if i > 0 {
			time.Sleep(2 * time.Second) // gentle throttle
		}
		isNational := t.leagueID == 1
		in := alias.LookupInput{CanonicalName: t.name, IsNational: isNational}
		if isNational {
			in.Country = strPtr(t.name)
		} else if c, ok := leagueCountry[t.leagueID]; ok {
			in.Country = strPtr(c)
		}

		lookup, err := r.Resolve(ctx, in)
		if err != nil {
			if errors.Is(err, alias.ErrNoMatch) {
				noMatch++
				report(fmt.Sprintf("  [L%-3d] %-28s → ✗ NoMatch", t.leagueID, t.name))
			} else {
				errored++
				report(fmt.Sprintf("  [L%-3d] %-28s → ✗ ERROR: %v", t.leagueID, t.name, err))
			}
			continue
		}
		flag := ""
		if isVariantLabel(lookup.Label) {
			flag = "   ⚠ VARIANT? review"
			flagged++
		}
		report(fmt.Sprintf("  [L%-3d] %-28s → %-10s [%s]%s",
			t.leagueID, t.name, lookup.QID, lookup.Label, flag))
	}

	report("")
	report(fmt.Sprintf("summary: %d teams | %d flagged-variant | %d no-match | %d errored",
		len(roster), flagged, noMatch, errored))
	report("output written to /src/scratch-resolver-roster.txt")
}

// isVariantLabel reports whether a resolved Wikipedia label carries a
// reserve/youth/women marker — a candidate mis-resolution to review.
func isVariantLabel(label string) bool {
	l := strings.ToLower(label)
	for _, m := range variantMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

func newResolver() *alias.Resolver {
	log := &logging.TestEmitter{}
	reg := metrics.New()
	wd, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint:  "https://query.wikidata.org/sparql",
		WWWHost:   "https://www.wikidata.org",
		UserAgent: "FoundFooty/1.0 (research; https://github.com/vedantadhobley/found-footy) roster_test",
		Timeout:   20 * time.Second,
	}, wikidata.RegisterMetrics(reg, log))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wikidata.NewClient: %v\n", err)
		os.Exit(1)
	}
	wp, err := wikipedia.NewClient(config.WikipediaConfig{
		Host:      "https://en.wikipedia.org",
		UserAgent: "FoundFooty/1.0 (research; https://github.com/vedantadhobley/found-footy) roster_test",
		Timeout:   20 * time.Second,
	}, wikipedia.RegisterMetrics(reg, log))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wikipedia.NewClient: %v\n", err)
		os.Exit(1)
	}
	return alias.NewResolver(wd, wp)
}

// (sort imported to keep output deterministic if we ever group; currently
// ordering is done in SQL. Kept to avoid an import churn if grouping lands.)
var _ = sort.Strings
