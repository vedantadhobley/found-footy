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
		strPtr("ATM"), strPtr("Spain"), strPtr("Madrid"),
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

// Full phase-1 + phase-2 roundtrip using UpsertResolution.
func TestAliasRepo_UpsertResolutionThenGet_AllFieldsRoundtrip(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	at := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	ta := makeClubAlias(530, "Atlético de Madrid")
	ta.SetResolution("Q8701", []string{"atletico", "atleti", "atm"}, at.Add(1*time.Minute))

	if err := repo.UpsertResolution(ctx, ta); err != nil {
		t.Fatalf("UpsertResolution: %v", err)
	}
	got, err := repo.Get(ctx, 530)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.TeamID != 530 || got.CanonicalName != "Atlético de Madrid" || got.IsNational {
		t.Errorf("phase-1 fields wrong: %+v", got)
	}
	if got.TeamCode == nil || *got.TeamCode != "ATM" {
		t.Errorf("TeamCode = %v, want ATM", got.TeamCode)
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
	if len(got.Aliases) != 3 {
		t.Errorf("Aliases len = %d, want 3", len(got.Aliases))
	}
	if got.ResolvedAt == nil {
		t.Error("ResolvedAt should be set after UpsertResolution")
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps not populated by DB defaults / trigger")
	}
	if !got.IsResolved() {
		t.Error("IsResolved should be true after UpsertResolution roundtrip")
	}
}

// A national team as unresolved placeholder: country=nil, city=nil,
// no Wikidata resolution yet. Verifies UpsertVendorFields leaves
// phase-2 fields as scan-back nil.
func TestAliasRepo_UpsertVendorFieldsThenGet_UnresolvedNationalTeam(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	at := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	ta := alias.New(20, "Netherlands", true, nil, nil, nil, at)

	if err := repo.UpsertVendorFields(ctx, ta); err != nil {
		t.Fatalf("UpsertVendorFields: %v", err)
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
	if got.ResolvedAt != nil {
		t.Errorf("ResolvedAt = %v, want nil (unresolved)", got.ResolvedAt)
	}
	if got.IsResolved() {
		t.Error("IsResolved should be false for a placeholder")
	}
	// aliases is NOT NULL DEFAULT '{}' — scan should give empty slice.
	if len(got.Aliases) != 0 {
		t.Errorf("Aliases = %v, want empty for placeholder", got.Aliases)
	}
}

// Placeholder-preserves-resolution semantics: after resolution has
// populated phase-2 fields, an Ingest-triggered UpsertVendorFields
// (e.g., daily refresh with an updated venue city) MUST NOT wipe
// wikidata_qid, aliases, or resolved_at.
func TestAliasRepo_UpsertVendorFields_PreservesResolution(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	// Phase 1: placeholder.
	at := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	ta := makeClubAlias(541, "Real Madrid")
	if err := repo.UpsertVendorFields(ctx, ta); err != nil {
		t.Fatalf("first UpsertVendorFields: %v", err)
	}

	// Phase 2: resolution runs.
	got, err := repo.Get(ctx, 541)
	if err != nil {
		t.Fatalf("Get after placeholder: %v", err)
	}
	got.SetResolution("Q8682", []string{"real", "madrid", "blancos"}, at.Add(5*time.Minute))
	if err := repo.UpsertResolution(ctx, got); err != nil {
		t.Fatalf("UpsertResolution: %v", err)
	}

	// Ingest re-runs with a "fresh" placeholder (e.g., a new venue city
	// picked up from the vendor). Its UpsertVendorFields must NOT wipe
	// the resolution data.
	refreshed := alias.New(541, "Real Madrid", false,
		strPtr("RMA"), strPtr("Spain"), strPtr("Madrid"), at.Add(24*time.Hour))
	if err := repo.UpsertVendorFields(ctx, refreshed); err != nil {
		t.Fatalf("second UpsertVendorFields: %v", err)
	}

	after, err := repo.Get(ctx, 541)
	if err != nil {
		t.Fatalf("Get after second placeholder: %v", err)
	}
	// Phase-2 fields intact.
	if after.WikidataQID == nil || *after.WikidataQID != "Q8682" {
		t.Errorf("resolution wiped by second UpsertVendorFields: WikidataQID = %v", after.WikidataQID)
	}
	if len(after.Aliases) != 3 {
		t.Errorf("resolution wiped by second UpsertVendorFields: Aliases = %v", after.Aliases)
	}
	if after.ResolvedAt == nil {
		t.Error("resolution wiped by second UpsertVendorFields: ResolvedAt = nil")
	}
	// Phase-1 fields refreshed.
	if after.TeamCode == nil || *after.TeamCode != "RMA" {
		t.Errorf("TeamCode not refreshed by second UpsertVendorFields: %v", after.TeamCode)
	}
}

// UpsertResolution twice with the same team_id preserves created_at
// (trigger discipline).
func TestAliasRepo_UpsertResolution_UpdatesExisting_PreservesCreatedAt(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	at := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	ta := makeClubAlias(541, "Real Madrid")
	ta.SetResolution("Q8682", []string{"real", "madrid"}, at.Add(1*time.Minute))
	if err := repo.UpsertResolution(ctx, ta); err != nil {
		t.Fatalf("first UpsertResolution: %v", err)
	}
	first, err := repo.Get(ctx, 541)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	origCreated := first.CreatedAt

	first.SetResolution("Q8682", []string{"real", "madrid", "blancos"}, at.Add(6*time.Minute))
	if err := repo.UpsertResolution(ctx, first); err != nil {
		t.Fatalf("second UpsertResolution: %v", err)
	}
	after, err := repo.Get(ctx, 541)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if !after.CreatedAt.Equal(origCreated) {
		t.Errorf("CreatedAt drifted: was %v, now %v", origCreated, after.CreatedAt)
	}
	if !after.UpdatedAt.After(origCreated) {
		t.Errorf("UpdatedAt didn't advance past CreatedAt: got %v vs %v", after.UpdatedAt, origCreated)
	}
	if len(after.Aliases) != 3 {
		t.Errorf("second Aliases len = %d, want 3", len(after.Aliases))
	}
}

// BulkGet ---------------------------------------------------------

func TestAliasRepo_BulkGet_MixedHitsMisses(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	if err := repo.UpsertVendorFields(ctx, makeClubAlias(40, "Liverpool")); err != nil {
		t.Fatalf("upsert 40: %v", err)
	}
	if err := repo.UpsertVendorFields(ctx, makeClubAlias(42, "Arsenal")); err != nil {
		t.Fatalf("upsert 42: %v", err)
	}

	got, err := repo.BulkGet(ctx, []int{40, 42, 999})
	if err != nil {
		t.Fatalf("BulkGet: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("map len = %d, want 2", len(got))
	}
	if got[40] == nil || got[40].CanonicalName != "Liverpool" {
		t.Errorf("40 missing/wrong: %+v", got[40])
	}
	if got[42] == nil || got[42].CanonicalName != "Arsenal" {
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

// Nil-slice defense: UpsertResolution normalizes nil aliases to
// []string{} so the NOT NULL DEFAULT '{}' constraint holds.
func TestAliasRepo_UpsertResolution_NilAliases_Normalized(t *testing.T) {
	ctx, repo := setupAliasRepo(t)

	at := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	ta := alias.New(60, "Sample FC", false, nil, nil, nil, at)
	ta.SetResolution("Q99999", nil, at.Add(1*time.Minute)) // nil aliases
	if err := repo.UpsertResolution(ctx, ta); err != nil {
		t.Fatalf("UpsertResolution with nil aliases: %v", err)
	}
	got, err := repo.Get(ctx, 60)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Aliases) != 0 {
		t.Errorf("empty-aliases roundtrip failed: Aliases=%v", got.Aliases)
	}
	// Empty aliases + non-nil ResolvedAt is the "we ran, nothing matched"
	// state — IsResolved should still be true.
	if !got.IsResolved() {
		t.Error("resolution with empty aliases should still count as resolved")
	}
}
