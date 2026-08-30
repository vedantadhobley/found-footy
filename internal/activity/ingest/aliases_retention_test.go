// Alias-placeholder activity tests.
package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
)

func TestEnsureAliasPlaceholders_MixedExistingAndNew(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	aRepo := newFakeAliasRepo()
	// Seed one team already cached.
	seed := alias.New(40, "Liverpool", false, nil, nil, nil, now.Add(-24*time.Hour))
	if err := aRepo.UpsertVendorFields(context.Background(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	a := newActivities(&fakeFetcher{}, newFakeFixtureRepo(), aRepo, now)
	out, err := a.EnsureAliasPlaceholders(context.Background(), EnsureAliasPlaceholdersInput{
		Teams: []TeamRef{
			{TeamID: 40, TeamName: "Liverpool"},
			{TeamID: 42, TeamName: "Arsenal"},
			{TeamID: 33, TeamName: "Manchester United"},
		},
	})
	if err != nil {
		t.Fatalf("EnsureAliasPlaceholders: %v", err)
	}
	if out.Existing != 1 || out.Inserted != 2 || len(out.Errors) != 0 {
		t.Errorf("out = %+v, want Existing=1, Inserted=2, Errors=[]", out)
	}
	// Verify 42 landed as an unresolved placeholder.
	ta, err := aRepo.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("Get placeholder: %v", err)
	}
	if ta.IsResolved() {
		t.Errorf("placeholder should be unresolved: %+v", ta)
	}
}

func TestEnsureAliasPlaceholders_EmptyInput(t *testing.T) {
	a := newActivities(&fakeFetcher{}, newFakeFixtureRepo(), newFakeAliasRepo(), time.Now().UTC())
	out, err := a.EnsureAliasPlaceholders(context.Background(), EnsureAliasPlaceholdersInput{Teams: nil})
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if out.Existing != 0 || out.Inserted != 0 || len(out.Errors) != 0 {
		t.Errorf("empty input counts non-zero: %+v", out)
	}
}
