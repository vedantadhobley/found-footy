// scripts/probe_search/main.go — search-query A/B harness. Fires competing
// query STRUCTURES, built from the SAME distinctive terms (discovery.QueryTerms
// so structure is the only variable), at the live twitter /search and scores
// recall + precision against known official accounts. Answers the OR-everything
// vs player-AND-team question empirically instead of by reasoning — see
// docs/decisions.md 2026-08-15 (twitter search query) + the search lock-in note.
//
// Variants per case (all end in filter:videos):
//
//	current_OR       (surname OR "Team" OR ABBREV OR alias)        — deployed
//	player_AND_team  (surname) ("Team" OR ABBREV OR alias)         — Python-style
//	team_only        ("Team" OR ABBREV OR alias)                   — team recall
//	player_only      (surname)                                     — player recall
//
// Reads cases from stdin (TSV):
//
//	player <TAB> teamCanonical <TAB> alias1,alias2,... <TAB> officialUser1,... [<TAB> maxAgeMin]
//
// Targets the DEV twitter service (shares cookies with prod but a SEPARATE
// Firefox fleet) so it never competes with prod's live per-event searches.
// Runs SEQUENTIALLY — one /search in flight at a time (the service spins a
// Firefox per call).
//
// Run:
//
//	docker exec -i found-footy-dev-worker sh -c 'cd /src && go run -buildvcs=false ./scripts/probe_search' < cases.tsv
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/discovery"
)

// twitterAddr — the dev twitter service on the found-footy-dev network.
const twitterAddr = "http://found-footy-dev-twitter:8888"

// kase is one goal to A/B: the scorer + team + the pipeline's real aliases, plus
// the KNOWN official account(s) used as the precision ground truth.
type kase struct {
	player   string
	team     string
	aliases  []string
	official []string
	maxAge   int
}

// variant is one query structure to fire.
type variant struct {
	name  string
	query string
}

// scored is the per-fire tally: total results, how many from an official
// account (ground-truth recall of the authoritative clip), and how many
// team-relevant (precision proxy).
type scored struct {
	count    int
	official int
	relevant int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cases, err := readCases(os.Stdin)
	if err != nil {
		return fmt.Errorf("read cases: %w", err)
	}
	if len(cases) == 0 {
		return fmt.Errorf("no cases on stdin (TSV: player<TAB>team<TAB>aliasCSV<TAB>officialCSV[<TAB>maxAge])")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	hc := &http.Client{}

	agg := map[string]*scored{}
	order := []string{}
	bump := func(name string, s scored) {
		if agg[name] == nil {
			agg[name] = &scored{}
			order = append(order, name)
		}
		a := agg[name]
		a.count += s.count
		a.official += s.official
		a.relevant += s.relevant
	}

	for ci, c := range cases {
		in := discovery.QueryInput{PlayerName: c.player, TeamCanonicalName: c.team, TeamAliases: c.aliases, VideoOnly: true}
		player, team := discovery.QueryTerms(in)
		// teamBare = canonical name + derived abbrev only (aliases dropped) —
		// isolates what the (contaminated) resolved alias set actually adds.
		inBare := in
		inBare.TeamAliases = nil
		_, teamBare := discovery.QueryTerms(inBare)
		relTokens := relevanceTokens(player, team)
		fmt.Printf("\n████ CASE %d/%d  %s / %s   (official: %s)\n",
			ci+1, len(cases), c.player, c.team, strings.Join(c.official, ","))
		fmt.Printf("     terms: player=%v team=%v  (aliases add: %v)\n", player, teamBare, aliasDelta(team, teamBare))

		for _, v := range buildVariants(player, team, teamBare) {
			resp, err := fireSearch(ctx, hc, v.query, c.maxAge)
			if err != nil {
				fmt.Printf("  %-16s ✗ %v\n", v.name, err)
				continue
			}
			s := scoreResults(resp, c.official, relTokens)
			fmt.Printf("  %-16s %2d results  %2d official  %2d relevant   %s\n",
				v.name, s.count, s.official, s.relevant, topUsers(resp, 6))
			bump(v.name, s)
		}
	}

	fmt.Printf("\n══════════ AGGREGATE (sum across %d cases) ══════════\n", len(cases))
	fmt.Printf("%-16s %8s %9s %9s %10s\n", "variant", "results", "official", "relevant", "precision")
	for _, name := range order {
		a := agg[name]
		prec := 0.0
		if a.count > 0 {
			prec = 100 * float64(a.relevant) / float64(a.count)
		}
		fmt.Printf("%-16s %8d %9d %9d %9.0f%%\n", name, a.count, a.official, a.relevant, prec)
	}
	return nil
}

// buildVariants composes the query structures from the shared term sets. Skips
// a variant whose group is empty, and skips no_aliases when the alias set added
// nothing (teamBare == team) so it doesn't duplicate current_OR.
func buildVariants(player, team, teamBare []string) []variant {
	var vs []variant
	orAll := append(append(make([]string, 0, len(player)+len(team)), player...), team...)
	if len(orAll) > 0 {
		vs = append(vs, variant{"current_OR", "(" + strings.Join(orAll, " OR ") + ") filter:videos"})
	}
	// no_aliases: player + canonical + derived abbrev, NO resolved aliases — the
	// "rip the alias pipeline out" candidate. Only if aliases actually added terms.
	orBare := append(append(make([]string, 0, len(player)+len(teamBare)), player...), teamBare...)
	if len(orBare) > 0 && len(orBare) < len(orAll) {
		vs = append(vs, variant{"no_aliases", "(" + strings.Join(orBare, " OR ") + ") filter:videos"})
	}
	if len(player) > 0 && len(team) > 0 {
		vs = append(vs, variant{"player_AND_team",
			"(" + strings.Join(player, " OR ") + ") (" + strings.Join(team, " OR ") + ") filter:videos"})
	}
	if len(team) > 0 {
		vs = append(vs, variant{"team_only", "(" + strings.Join(team, " OR ") + ") filter:videos"})
	}
	if len(player) > 0 {
		vs = append(vs, variant{"player_only", "(" + strings.Join(player, " OR ") + ") filter:videos"})
	}
	return vs
}

// --- twitter /search over raw HTTP (same JSON contract as infra/twitter) ---

type searchReq struct {
	Query         string `json:"query"`
	MaxAgeMinutes int    `json:"max_age_minutes"`
}

type videoRef struct {
	TweetURL  string `json:"tweet_url"`
	TweetText string `json:"tweet_text"`
	Username  string `json:"username"`
}

type searchResp struct {
	Status string     `json:"status"`
	Videos []videoRef `json:"videos"`
	Count  int        `json:"count"`
}

func fireSearch(ctx context.Context, hc *http.Client, query string, maxAge int) (*searchResp, error) {
	body, _ := json.Marshal(searchReq{Query: query, MaxAgeMinutes: maxAge})
	sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(sctx, http.MethodPost, twitterAddr+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var sr searchResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	return &sr, nil
}

// scoreResults counts results, official-account hits (exact username), and
// team-relevant results (username or tweet text contains any distinctive term).
func scoreResults(sr *searchResp, official, relTokens []string) scored {
	s := scored{count: len(sr.Videos)}
	off := map[string]struct{}{}
	for _, o := range official {
		off[strings.ToLower(o)] = struct{}{}
	}
	for _, v := range sr.Videos {
		u := strings.ToLower(v.Username)
		if _, ok := off[u]; ok {
			s.official++
		}
		hay := u + " " + strings.ToLower(v.TweetText)
		for _, t := range relTokens {
			if t != "" && strings.Contains(hay, t) {
				s.relevant++
				break
			}
		}
	}
	return s
}

// relevanceTokens is the distinctive-word set (≥3 chars) from the player +
// team terms — de-quoted + split so "Nashville SC" contributes "nashville".
// Built from QueryTerms output, so generics are already excluded.
func relevanceTokens(player, team []string) []string {
	set := map[string]struct{}{}
	for _, grp := range [][]string{player, team} {
		for _, term := range grp {
			for _, w := range strings.Fields(strings.ToLower(strings.Trim(term, `"`))) {
				if len(w) >= 3 {
					set[w] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for w := range set {
		out = append(out, w)
	}
	return out
}

func topUsers(sr *searchResp, n int) string {
	us := make([]string, 0, n)
	for i, v := range sr.Videos {
		if i >= n {
			break
		}
		us = append(us, "@"+v.Username)
	}
	return strings.Join(us, " ")
}

// aliasDelta returns the terms in team but not teamBare — what the resolved
// alias set contributed on top of the canonical name + derived abbreviation.
func aliasDelta(team, teamBare []string) []string {
	bare := map[string]struct{}{}
	for _, t := range teamBare {
		bare[t] = struct{}{}
	}
	var out []string
	for _, t := range team {
		if _, ok := bare[t]; !ok {
			out = append(out, t)
		}
	}
	return out
}

func readCases(r io.Reader) ([]kase, error) {
	var out []kase
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "\t")
		if len(p) < 4 {
			continue
		}
		age := 1440
		if len(p) >= 5 {
			if n, err := strconv.Atoi(strings.TrimSpace(p[4])); err == nil {
				age = n
			}
		}
		out = append(out, kase{
			player:   strings.TrimSpace(p[0]),
			team:     strings.TrimSpace(p[1]),
			aliases:  splitCSV(p[2]),
			official: splitCSV(p[3]),
			maxAge:   age,
		})
	}
	return out, sc.Err()
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}
