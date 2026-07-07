// scripts/smoke_repos/main.go — dev-only smoke test for the pg repos.
//
// Purpose: verify that pg.FixtureRepo and pg.AliasRepo actually work
// against the LIVE dev postgres (not testcontainers), which catches
// "dev pg has drifted from schema.sql" regressions the unit tests
// can't. Exercises the same query shapes IngestWorkflow will use.
//
// Run:
//
//	docker exec found-footy-dev-worker sh -c 'cd /src && go run ./scripts/smoke_repos'
//
// Requires PG_DSN in the container's env (already set via .env for the
// dev worker). Test rows use IDs 900_000+ and are cleaned up on exit.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		fatal("PG_DSN env not set", nil)
	}

	reg := metrics.New()
	log := &logging.TestEmitter{}
	ins := pg.RegisterMetrics(reg, log)

	pool, err := pg.New(ctx, config.PGConfig{
		DSN:            dsn,
		MaxConns:       3,
		MinConns:       1,
		ConnectTimeout: 5 * time.Second,
	}, ins)
	if err != nil {
		fatal("pg.New", err)
	}
	defer pool.Close()

	fmt.Println("── FixtureRepo ──")
	smokeFixture(ctx, pool)

	fmt.Println("── AliasRepo ──")
	smokeAlias(ctx, pool)

	fmt.Println("\n✓ SMOKE TEST OK")
}

func smokeFixture(ctx context.Context, pool *pg.Pool) {
	repo := pg.NewFixtureRepo(pool)
	const testID = 900_001

	if _, err := pool.Exec(ctx, "DELETE FROM fixtures WHERE id = $1", testID); err != nil {
		fatal("cleanup pre-run", err)
	}

	if _, err := repo.Get(ctx, testID); !errors.Is(err, fixture.ErrNotFound) {
		fatal("Get miss should return fixture.ErrNotFound", err)
	}
	fmt.Println("  Get miss → ErrNotFound ✓")

	f := fixture.New(testID,
		fixture.APIStatus{Short: "NS", Long: "Not Started"},
		time.Date(2026, 12, 25, 15, 0, 0, 0, time.UTC),
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Name: "Premier League", Season: 2026},
	)
	if err := repo.Upsert(ctx, f); err != nil {
		fatal("Upsert insert", err)
	}
	fmt.Println("  Upsert insert ✓")

	got, err := repo.Get(ctx, testID)
	if err != nil {
		fatal("Get after upsert", err)
	}
	if got.ID != testID || got.State != fixture.StateStaging {
		fatal("roundtrip state", fmt.Errorf("got ID=%d state=%q", got.ID, got.State))
	}
	if got.Home.Name != "Liverpool" || got.Away.Name != "Arsenal" {
		fatal("roundtrip teams", fmt.Errorf("home=%q away=%q", got.Home.Name, got.Away.Name))
	}
	fmt.Println("  Get roundtrip ✓")

	if err := got.Activate(time.Now().UTC()); err != nil {
		fatal("Activate", err)
	}
	if err := repo.Upsert(ctx, got); err != nil {
		fatal("Upsert update", err)
	}
	after, err := repo.Get(ctx, testID)
	if err != nil {
		fatal("Get after activate", err)
	}
	if after.State != fixture.StateActive || after.ActivatedAt == nil {
		fatal("post-activate state", fmt.Errorf("state=%q activated_at=%v", after.State, after.ActivatedAt))
	}
	fmt.Println("  Upsert update (staging→active) ✓")

	if after.ShouldActivateNow(time.Now().UTC(), 30*time.Minute) {
		fatal("ShouldActivateNow on active fixture", fmt.Errorf("should be false"))
	}
	fmt.Println("  ShouldActivateNow=false on active ✓")

	activeList, err := repo.ListByState(ctx, fixture.StateActive)
	if err != nil {
		fatal("ListByState", err)
	}
	foundIt := false
	for _, f := range activeList {
		if f.ID == testID {
			foundIt = true
			break
		}
	}
	if !foundIt {
		fatal("ListByState missing test fixture", fmt.Errorf("got %d fixtures", len(activeList)))
	}
	fmt.Println("  ListByState includes test fixture ✓")

	if _, err := pool.Exec(ctx, "DELETE FROM fixtures WHERE id = $1", testID); err != nil {
		fatal("cleanup", err)
	}
	fmt.Println("  cleanup ✓")
}

func smokeAlias(ctx context.Context, pool *pg.Pool) {
	repo := pg.NewAliasRepo(pool)
	const testID = 900_002

	if _, err := pool.Exec(ctx, "DELETE FROM team_aliases WHERE team_id = $1", testID); err != nil {
		fatal("cleanup pre-run", err)
	}

	if _, err := repo.Get(ctx, testID); !errors.Is(err, alias.ErrNotFound) {
		fatal("Get miss should return alias.ErrNotFound", err)
	}
	fmt.Println("  Get miss → ErrNotFound ✓")

	// The footgun scenario: Upsert with nil arrays (default zero value).
	// If the nil-normalize in AliasRepo.Upsert is working, this succeeds;
	// if it were missing, we'd get a NOT NULL constraint violation.
	spain := "Spain"
	ta := alias.New(testID, "Test Club FC", false, &spain, nil, time.Now().UTC())
	if err := repo.Upsert(ctx, ta); err != nil {
		fatal("Upsert with nil arrays (footgun regression)", err)
	}
	fmt.Println("  Upsert insert (nil arrays normalized) ✓")

	got, err := repo.Get(ctx, testID)
	if err != nil {
		fatal("Get after upsert", err)
	}
	if got.TeamName != "Test Club FC" || got.HasWikidataResolution() || got.HasTwitterAliases() {
		fatal("roundtrip", fmt.Errorf("got %+v", got))
	}
	if len(got.WikidataAliases) != 0 || len(got.TwitterAliases) != 0 {
		fatal("nil arrays didn't roundtrip as empty", fmt.Errorf("wd=%v tw=%v", got.WikidataAliases, got.TwitterAliases))
	}
	fmt.Println("  Get roundtrip (empty arrays preserved) ✓")

	got.SetWikidataResolution("Q999999", []string{"Test Club", "TC", "The Testers"}, time.Now().UTC())
	if err := got.SetTwitterAliases([]string{"Test", "TC"}, "smoke-model", time.Now().UTC()); err != nil {
		fatal("SetTwitterAliases", err)
	}
	if err := repo.Upsert(ctx, got); err != nil {
		fatal("Upsert update with resolution", err)
	}
	after, err := repo.Get(ctx, testID)
	if err != nil {
		fatal("Get after resolution", err)
	}
	if !after.HasWikidataResolution() || len(after.WikidataAliases) != 3 {
		fatal("Wikidata roundtrip", fmt.Errorf("qid=%v aliases=%v", after.WikidataQID, after.WikidataAliases))
	}
	if !after.HasTwitterAliases() || after.LLMModel == nil || *after.LLMModel != "smoke-model" {
		fatal("Twitter/LLM roundtrip", fmt.Errorf("twitter=%v model=%v", after.TwitterAliases, after.LLMModel))
	}
	fmt.Println("  Upsert update with resolution ✓")

	bulk, err := repo.BulkGet(ctx, []int{testID, 900_999})
	if err != nil {
		fatal("BulkGet", err)
	}
	if len(bulk) != 1 || bulk[testID] == nil {
		fatal("BulkGet result", fmt.Errorf("got %d entries: %+v", len(bulk), bulk))
	}
	fmt.Println("  BulkGet mixed hits/misses ✓")

	if _, err := pool.Exec(ctx, "DELETE FROM team_aliases WHERE team_id = $1", testID); err != nil {
		fatal("cleanup", err)
	}
	fmt.Println("  cleanup ✓")
}

func fatal(msg string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ SMOKE TEST FAILED at %q: %v\n", msg, err)
	} else {
		fmt.Fprintf(os.Stderr, "\n✗ SMOKE TEST FAILED: %s\n", msg)
	}
	os.Exit(1)
}
