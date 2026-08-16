// scripts/probe_playerslot/main.go — player-slot A/B for HARD names. Tonight's
// MLS goals were all "F. Surname" (last-token works), so this can't be tested on
// live goals — instead it fires player-ONLY queries for known-hard European
// names where the deployed last-token logic breaks (Son Heung-min → "min",
// Vinícius Júnior → "junior", Alexander-Arnold → "arnold") and measures which
// player-term shape surfaces the player's clips vs noise.
//
// Isolates the player slot: player terms only, no team OR'd in. Precision proxy
// = a result whose text/username mentions the player's TEAM (independent of the
// player tokens under test — "min" noise won't mention Tottenham). 30-day
// window because there may be no goal today; opening weekend should still give
// volume.
//
// Variants per name (all filter:videos):
//   last_token     deployed — TokenizePlayerName's last token
//   nosuffix_last  strip generational suffix, then last token (heuristic surname)
//   all_tokens     every significant token OR'd (original design)
//   all_nosuffix   every token minus generational suffix, OR'd
//
// Run: docker exec found-footy-dev-worker sh -c 'cd /src && go run -buildvcs=false ./scripts/probe_playerslot'
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
)

const twitterAddr = "http://found-footy-dev-twitter:8888"
const maxAgeMin = 43200 // 30 days

// genSuffix — generational suffixes that must never be the searched surname.
var genSuffix = map[string]struct{}{
	"junior": {}, "jr": {}, "sr": {}, "filho": {}, "neto": {}, "ii": {}, "iii": {},
}

// pcase is a hard-name player + distinctive TEAM tokens for the precision
// proxy (team-only, so they don't overlap the player tokens under test).
type pcase struct {
	name      string
	teamTruth []string
}

var cases = []pcase{
	{"Son Heung-min", []string{"tottenham", "spurs", "thfc"}},
	{"Vinícius Júnior", []string{"madrid", "rmcf", "halamadrid", "bernabeu"}},
	{"Trent Alexander-Arnold", []string{"liverpool", "lfc", "ynwa", "reds"}},
	{"Mohamed Salah", []string{"liverpool", "lfc", "ynwa", "reds"}}, // control: last-token already correct
}

type variant struct{ label, query string }

func buildVariants(name string) []variant {
	toks := alias.TokenizePlayerName(name)
	if len(toks) == 0 {
		return nil
	}
	ns := stripGen(toks)
	last := toks[len(toks)-1]
	nsLast := last
	if len(ns) > 0 {
		nsLast = ns[len(ns)-1]
	}
	return []variant{
		{"last_token", "(" + last + ") filter:videos"},
		{"nosuffix_last", "(" + nsLast + ") filter:videos"},
		{"all_tokens", "(" + strings.Join(toks, " OR ") + ") filter:videos"},
		{"all_nosuffix", "(" + strings.Join(ns, " OR ") + ") filter:videos"},
	}
}

func stripGen(toks []string) []string {
	out := append([]string(nil), toks...)
	for len(out) > 0 {
		if _, ok := genSuffix[out[len(out)-1]]; ok {
			out = out[:len(out)-1]
		} else {
			break
		}
	}
	return out
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	hc := &http.Client{}

	for _, c := range cases {
		toks := alias.TokenizePlayerName(c.name)
		fmt.Printf("\n████ %s   tokens=%v\n", c.name, toks)
		for _, v := range buildVariants(c.name) {
			resp, err := fireSearch(ctx, hc, v.query)
			if err != nil {
				fmt.Printf("  %-14s %-40s ✗ %v\n", v.label, v.query, err)
				continue
			}
			total := len(resp.Videos)
			team := 0
			for _, vid := range resp.Videos {
				hay := strings.ToLower(vid.Username + " " + vid.TweetText)
				for _, t := range c.teamTruth {
					if strings.Contains(hay, t) {
						team++
						break
					}
				}
			}
			fmt.Printf("  %-14s %-34s %2d results  %2d mention-team   %s\n",
				v.label, v.query, total, team, topUsers(resp, 6))
		}
	}
	return nil
}

// --- twitter /search over raw HTTP ---

type searchReq struct {
	Query         string `json:"query"`
	MaxAgeMinutes int    `json:"max_age_minutes"`
}

type videoRef struct {
	TweetText string `json:"tweet_text"`
	Username  string `json:"username"`
}

type searchResp struct {
	Status string     `json:"status"`
	Videos []videoRef `json:"videos"`
}

func fireSearch(ctx context.Context, hc *http.Client, query string) (*searchResp, error) {
	body, _ := json.Marshal(searchReq{Query: query, MaxAgeMinutes: maxAgeMin})
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
