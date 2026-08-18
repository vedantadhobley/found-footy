// Fixture polling metadata and error-context activity tests.
package ingest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// TestCategorize_SetsLastPolledAt_OnFresh requires ingest to record the API
// observation time so the monitor does not immediately repoll the fixture.
func TestCategorize_SetsLastPolledAt_OnFresh(t *testing.T) {
	kickoff := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-48 * time.Hour)
	fRepo := newFakeFixtureRepo()
	a := newActivities(&fakeFetcher{}, fRepo, newFakeAliasRepo(), now)

	_, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "ns", kickoff, 40, 42)},
		ActivationWindow: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	stored, _ := fRepo.Get(context.Background(), 1)
	if stored.LastPolledAt == nil {
		t.Fatal("LastPolledAt nil on freshly-ingested fixture; ingest IS a poll")
	}
	if !stored.LastPolledAt.Equal(now) {
		t.Errorf("LastPolledAt = %v, want %v (injected now)", stored.LastPolledAt, now)
	}
}

// TestCategorize_UpdatesLastPolledAt_OnExisting — an existing
// fixture whose LastPolledAt was set 6 hours ago should be refreshed
// to `now` on re-ingest, without disturbing ActivatedAt.
func TestCategorize_UpdatesLastPolledAt_OnExisting(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(30 * time.Minute)
	sixHoursAgo := now.Add(-6 * time.Hour)
	fRepo := newFakeFixtureRepo()

	existing := fixture.New(1,
		fixture.APIStatus{Short: "1h", Long: "First Half"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Name: "PL", Season: 2026},
	)
	activatedAt := kickoff.Add(-30 * time.Minute)
	if err := existing.Activate(activatedAt); err != nil {
		t.Fatalf("seed Activate: %v", err)
	}
	existing.LastPolledAt = &sixHoursAgo
	if err := fRepo.Upsert(context.Background(), existing); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	a := newActivities(&fakeFetcher{}, fRepo, newFakeAliasRepo(), now)
	_, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "1h", kickoff, 40, 42)},
		ActivationWindow: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	after, _ := fRepo.Get(context.Background(), 1)
	if after.LastPolledAt == nil || !after.LastPolledAt.Equal(now) {
		t.Errorf("LastPolledAt = %v, want %v (refreshed)", after.LastPolledAt, now)
	}
	if after.ActivatedAt == nil || !after.ActivatedAt.Equal(activatedAt) {
		t.Errorf("ActivatedAt = %v, want %v (preserved)", after.ActivatedAt, activatedAt)
	}
}

// ── Errors []string with context ──────────────────────────────

// erroringFixtureRepo — fails every Upsert with a specific error.
// For the errors-carry-context test; embeds the good fake so
// non-Upsert methods still work.
type erroringFixtureRepo struct {
	*fakeFixtureRepo
	err error
}

func (r *erroringFixtureRepo) Upsert(context.Context, *fixture.Fixture) error {
	return r.err
}

// TestCategorize_ErrorsCarryFixtureContext — when a per-fixture
// Upsert fails, the CategorizeOutput.Errors []string entry must
// name the fixture ID so operators can find WHICH one failed
// without joining logs.
func TestCategorize_ErrorsCarryFixtureContext(t *testing.T) {
	kickoff := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-48 * time.Hour)
	fRepo := &erroringFixtureRepo{
		fakeFixtureRepo: newFakeFixtureRepo(),
		err:             fmt.Errorf("simulated pg pool exhausted"),
	}
	a := newActivities(&fakeFetcher{}, fRepo, newFakeAliasRepo(), now)

	out, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures: []apifootball.APIFixture{
			mkAPIFixture(111, "ns", kickoff, 40, 42),
			mkAPIFixture(222, "ns", kickoff, 33, 50),
		},
		ActivationWindow: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Categorize itself should NOT fail: %v", err)
	}
	if len(out.Errors) != 2 {
		t.Fatalf("expected 2 error entries, got %d: %+v", len(out.Errors), out.Errors)
	}
	// Both fixture IDs should appear in the error strings.
	if !strings.Contains(out.Errors[0]+out.Errors[1], "fixture=111") {
		t.Errorf("errors missing fixture=111 context: %+v", out.Errors)
	}
	if !strings.Contains(out.Errors[0]+out.Errors[1], "fixture=222") {
		t.Errorf("errors missing fixture=222 context: %+v", out.Errors)
	}
	if !strings.Contains(out.Errors[0]+out.Errors[1], "pool exhausted") {
		t.Errorf("errors missing underlying error text: %+v", out.Errors)
	}
}
