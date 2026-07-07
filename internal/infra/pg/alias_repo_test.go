// Tests for the AliasRepo. Same setup helper pattern as FixtureRepo
// tests — real Postgres via testcontainers with schema.sql loaded.
package pg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// setupAliasRepo mirrors setupRepo (fixture) but returns an AliasRepo.
// Same runTestPostgres helper; per-test fresh schema.
func setupAliasRepo(t *testing.T) (context.Context, *pg.AliasRepo) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	connStr := runTestPostgres(ctx, t)
	fx := newTestFixture()
	pool, err := pg.New(ctx, config.PGConfig{
		DSN:            connStr,
		MaxConns:       5,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	}, fx.ins)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pg.NewAliasRepo(pool)
}

func strPtr(s string) *string { return &s }

func makeClubAlias(teamID int, name string) *alias.TeamAlias {
	return alias.New(
		teamID, name, false,
		strPtr("Spain"), strPtr("Madrid"),
		time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC),
	)
}

// Get -------------------------------------------------------------

func TestAliasRepo_Get_NotFound(t *testing.T) {
	ctx, repo := setupAliasRepo(t)
	_, err := repo.Get(ctx, 999_999)
	if !errors.Is(err, alias.ErrNotFound) {
		t.Errorf("Get miss returned %v, want alias.ErrNotFound", err)
	}
}

// Full roundtrip: input fields + resolved fields (Wikidata + Twitter
// arrays + LLM model). Non-null arrays, non-null country/city,
// populated wikidata_qid + twitter_aliases + llm_model.
func TestAliasRepo_UpsertThenGet_AllFieldsRoundtrip(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	at := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	ta := makeClubAlias(530, "Atlético de Madrid")
	ta.SetWikidataResolution("Q8701", []string{"Atletico Madrid", "El Atleti", "ATM", "Colchoneros"}, at.Add(1*time.Minute))
	if err := ta.SetTwitterAliases([]string{"Atletico", "Atleti", "ATM"}, "Qwen3-VL-8B", at.Add(2*time.Minute)); err != nil {
		t.Fatalf("SetTwitterAliases: %v", err)
	}

	if err := repo.Upsert(ctx, ta); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := repo.Get(ctx, 530)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.TeamID != 530 || got.TeamName != "Atlético de Madrid" || got.IsNational {
		t.Errorf("input fields wrong: %+v", got)
	}
	if got.Country == nil || *got.Country != "Spain" {
		t.Errorf("Country = %v, want Spain", got.Country)
	}
	if got.City == nil || *got.City != "Madrid" {
		t.Errorf("City = %v, want Madrid", got.City)
	}
	if got.WikidataQID == nil || *got.WikidataQID != "Q8701" {
		t.Errorf("WikidataQID = %v, want Q8701", got.WikidataQID)
	}
	if len(got.WikidataAliases) != 4 {
		t.Errorf("WikidataAliases len = %d, want 4", len(got.WikidataAliases))
	}
	if len(got.TwitterAliases) != 3 || got.TwitterAliases[0] != "Atletico" {
		t.Errorf("TwitterAliases = %v", got.TwitterAliases)
	}
	if got.LLMModel == nil || *got.LLMModel != "Qwen3-VL-8B" {
		t.Errorf("LLMModel = %v, want Qwen3-VL-8B", got.LLMModel)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps not populated by DB defaults / trigger")
	}
}

// A national team with country=nil, city=nil, no Wikidata resolution
// yet. Verifies the nullable-pointer path scans back as (*string)(nil).
func TestAliasRepo_UpsertThenGet_UnresolvedNationalTeam(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	at := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	ta := alias.New(20, "Netherlands", true, nil, nil, at)

	if err := repo.Upsert(ctx, ta); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := repo.Get(ctx, 20)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.IsNational {
		t.Error("IsNational should roundtrip as true")
	}
	if got.Country != nil {
		t.Errorf("Country = %v, want nil", got.Country)
	}
	if got.City != nil {
		t.Errorf("City = %v, want nil", got.City)
	}
	if got.WikidataQID != nil {
		t.Errorf("WikidataQID = %v, want nil (unresolved)", got.WikidataQID)
	}
	if got.HasWikidataResolution() {
		t.Error("HasWikidataResolution should be false")
	}
	if got.HasTwitterAliases() {
		t.Error("HasTwitterAliases should be false")
	}
	// Arrays are NOT NULL DEFAULT '{}' — scan should give empty slice
	// (or non-nil zero-len; both are semantically empty).
	if len(got.WikidataAliases) != 0 {
		t.Errorf("WikidataAliases = %v, want empty", got.WikidataAliases)
	}
	if len(got.TwitterAliases) != 0 {
		t.Errorf("TwitterAliases = %v, want empty", got.TwitterAliases)
	}
}

// Second Upsert on the same team_id updates resolved fields but keeps
// created_at (mirrors the fixture repo discipline).
func TestAliasRepo_Upsert_UpdatesExisting_PreservesCreatedAt(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	at := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	ta := makeClubAlias(541, "Real Madrid")
	if err := repo.Upsert(ctx, ta); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	first, err := repo.Get(ctx, 541)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	origCreated := first.CreatedAt

	first.SetWikidataResolution("Q8682", []string{"Real Madrid", "Los Blancos"}, at.Add(5*time.Minute))
	if err := repo.Upsert(ctx, first); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	after, err := repo.Get(ctx, 541)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if after.WikidataQID == nil || *after.WikidataQID != "Q8682" {
		t.Errorf("resolved WikidataQID = %v, want Q8682", after.WikidataQID)
	}
	if !after.CreatedAt.Equal(origCreated) {
		t.Errorf("CreatedAt drifted: was %v, now %v", origCreated, after.CreatedAt)
	}
	if !after.UpdatedAt.After(origCreated) {
		t.Errorf("UpdatedAt didn't advance past CreatedAt: got %v vs %v", after.UpdatedAt, origCreated)
	}
}

// BulkGet ---------------------------------------------------------

func TestAliasRepo_BulkGet_MixedHitsMisses(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	// Insert 40 + 42; ask for 40, 42, 999.
	if err := repo.Upsert(ctx, makeClubAlias(40, "Liverpool")); err != nil {
		t.Fatalf("upsert 40: %v", err)
	}
	if err := repo.Upsert(ctx, makeClubAlias(42, "Arsenal")); err != nil {
		t.Fatalf("upsert 42: %v", err)
	}

	got, err := repo.BulkGet(ctx, []int{40, 42, 999})
	if err != nil {
		t.Fatalf("BulkGet: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("map len = %d, want 2", len(got))
	}
	if got[40] == nil || got[40].TeamName != "Liverpool" {
		t.Errorf("40 missing/wrong: %+v", got[40])
	}
	if got[42] == nil || got[42].TeamName != "Arsenal" {
		t.Errorf("42 missing/wrong: %+v", got[42])
	}
	if _, present := got[999]; present {
		t.Error("999 shouldn't be in the map (no row)")
	}
}

// Zero-input case: no round trip, empty map, no error.
func TestAliasRepo_BulkGet_EmptyInput(t *testing.T) {
	ctx, repo := setupAliasRepo(t)
	got, err := repo.BulkGet(ctx, nil)
	if err != nil {
		t.Fatalf("BulkGet: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("BulkGet(nil) = %+v, want empty map", got)
	}
}

// The nil-slice-vs-empty-slice defense: Upsert's normalizer converts
// nil arrays to []string{} so the NOT NULL DEFAULT '{}' constraint
// holds regardless of caller discipline.
func TestAliasRepo_Upsert_NilArrays_Normalized(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	at := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	ta := alias.New(60, "Sample FC", false, nil, nil, at)
	// Both arrays are nil ([]string(nil)) — the default zero value.
	if ta.WikidataAliases != nil || ta.TwitterAliases != nil {
		t.Fatalf("test setup: arrays should start nil, got %+v / %+v",
			ta.WikidataAliases, ta.TwitterAliases)
	}
	if err := repo.Upsert(ctx, ta); err != nil {
		t.Fatalf("Upsert with nil arrays: %v", err)
	}
	got, err := repo.Get(ctx, 60)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Empty arrays roundtrip; length 0 is the correct outcome.
	if len(got.WikidataAliases) != 0 || len(got.TwitterAliases) != 0 {
		t.Errorf("empty-array roundtrip failed: WikidataAliases=%v TwitterAliases=%v",
			got.WikidataAliases, got.TwitterAliases)
	}
}
