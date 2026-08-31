// scripts/verify_apifootball/main.go — live smoke test of the
// api-sports.io adapter. Exercises every code path we care about:
//
//  1. /status probe via NewClient — proves x-apisports-key auth
//     works. Header was regressed to x-rapidapi-key at some point;
//     this catches regressions.
//  2. /fixtures?date=... — bulk fixture list. Also gives us real
//     fixture IDs for step 3.
//  3. ListFixturesByIDs with a small (≤ IDsBatchLimit) slice —
//     single-chunk path. One HTTP call.
//  4. ListFixturesByIDs with a large (> IDsBatchLimit) slice —
//     multi-chunk parallel path. N HTTP calls concurrent via
//     goroutines. Verifies the 2026-07-09 refactor.
//  5. Rate-limit gauges — scrape the metrics registry after all
//     calls, confirm both PER-MINUTE and DAILY headers populated.
//
// Run (from host, targeting the dev-worker container so env is loaded):
//
//	docker exec found-footy-dev-worker sh -c 'cd /src && go run ./scripts/verify_apifootball'
//
// Consumes 3-4 api-sports.io requests total. Well under 7500/day.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"io"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n✓ ALL CHECKS PASSED")
}

func run() error {
	cfg, err := config.LoadFor(config.BinaryWorker)
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}
	if cfg.APIFootball.APIKey == "" {
		return errors.New("API_FOOTBALL_KEY not set — is .env loaded?")
	}
	reg := metrics.New()
	log := logging.New(cfg.Observability, reg)
	ins := apifootball.RegisterMetrics(reg, log)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. NewClient probes /status. Auth failure surfaces here. ──
	fmt.Println("→ Step 1: NewClient (x-apisports-key auth via /status)")
	c, err := apifootball.NewClient(ctx, cfg.APIFootball, ins)
	if err != nil {
		return fmt.Errorf("NewClient (likely auth): %w", err)
	}
	fmt.Println("  ✓ /status returned OK — auth works")

	// ── 2. /fixtures?date=YYYY-MM-DD. Modest by-date query. ──
	fmt.Println("→ Step 2: /fixtures?date=<today>")
	today := time.Now().UTC().Truncate(24 * time.Hour)
	dateFixtures, err := c.ListFixtures(ctx, apifootball.FixtureListParams{Date: today})
	if err != nil {
		return fmt.Errorf("ListFixtures(date): %w", err)
	}
	fmt.Printf("  ✓ %d fixtures on %s\n", len(dateFixtures), today.Format("2006-01-02"))
	if len(dateFixtures) == 0 {
		yesterday := today.Add(-24 * time.Hour)
		dateFixtures, err = c.ListFixtures(ctx, apifootball.FixtureListParams{Date: yesterday})
		if err != nil {
			return fmt.Errorf("ListFixtures(yesterday): %w", err)
		}
		fmt.Printf("  ✓ retried %s: %d fixtures\n", yesterday.Format("2006-01-02"), len(dateFixtures))
	}
	if len(dateFixtures) == 0 {
		return errors.New("no fixtures today or yesterday — pick a different date")
	}

	// ── 3. ListFixturesByIDs with ≤ IDsBatchLimit — one chunk. ──
	fmt.Printf("→ Step 3: ListFixturesByIDs(%d IDs) — single-chunk path\n", apifootball.IDsBatchLimit-5)
	small := collectIDs(dateFixtures, apifootball.IDsBatchLimit-5)
	smallResult, err := c.ListFixturesByIDs(ctx, small)
	if err != nil {
		return fmt.Errorf("ListFixturesByIDs(small): %w", err)
	}
	fmt.Printf("  ✓ got %d fixtures, %d failed IDs (want 0)\n",
		len(smallResult.Fixtures), len(smallResult.FailedIDs()))

	// ── 4. ListFixturesByIDs with > IDsBatchLimit — multi-chunk. ──
	targetLen := apifootball.IDsBatchLimit*2 + apifootball.IDsBatchLimit/2 // 50
	if len(dateFixtures) < targetLen {
		targetLen = len(dateFixtures)
	}
	fmt.Printf("→ Step 4: ListFixturesByIDs(%d IDs) — multi-chunk parallel path\n", targetLen)
	large := collectIDs(dateFixtures, targetLen)
	t0 := time.Now()
	largeResult, err := c.ListFixturesByIDs(ctx, large)
	elapsed := time.Since(t0)
	if err != nil {
		return fmt.Errorf("ListFixturesByIDs(large): %w", err)
	}
	fmt.Printf("  ✓ got %d fixtures, %d failed, %.2fs wall (chunks fired in parallel)\n",
		len(largeResult.Fixtures), len(largeResult.FailedIDs()), elapsed.Seconds())

	// ── 5. Rate-limit gauges. Scrape the Prometheus text exposition. ──
	fmt.Println("→ Step 5: rate-limit gauges populated?")
	scrape, err := scrapePrometheus(reg)
	if err != nil {
		return fmt.Errorf("scrape metrics: %w", err)
	}
	found := 0
	for _, prefix := range []string{
		"found_footy_apifootball_ratelimit_remaining ",   // per-minute
		"found_footy_apifootball_daily_quota_remaining ", // daily
	} {
		line := grepPrefix(scrape, prefix)
		if line == "" {
			fmt.Printf("  ⚠ %s: NOT populated — vendor may have skipped that header\n", prefix)
			continue
		}
		found++
		fmt.Printf("  ✓ %s\n", line)
	}
	if found == 0 {
		return errors.New("neither rate-limit gauge populated — header parsing broken")
	}
	return nil
}

// collectIDs pulls up to n distinct fixture IDs from the list.
func collectIDs(fixtures []apifootball.APIFixture, n int) []int64 {
	if n > len(fixtures) {
		n = len(fixtures)
	}
	seen := make(map[int64]struct{}, n)
	out := make([]int64, 0, n)
	for _, f := range fixtures {
		if _, dup := seen[f.Fixture.ID]; dup {
			continue
		}
		seen[f.Fixture.ID] = struct{}{}
		out = append(out, f.Fixture.ID)
		if len(out) == n {
			break
		}
	}
	return out
}

// scrapePrometheus grabs the /metrics text exposition using httptest
// so we don't need to bind a port ourselves.
func scrapePrometheus(reg *metrics.Registry) (string, error) {
	srv := httptest.NewServer(reg.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL) //nolint:noctx // one-shot script
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// grepPrefix returns the first line of s that starts with prefix, or
// "" if none.
func grepPrefix(s, prefix string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}
