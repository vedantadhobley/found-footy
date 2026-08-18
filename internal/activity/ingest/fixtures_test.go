// Fixture fetch and categorization activity tests.
package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/team"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// TestCategorizeAndUpsertFixtures_ChangedIDs requires new fixtures and
// meaningful updates, but not identical refreshes, to emit fixture changes.
func TestCategorizeAndUpsertFixtures_ChangedIDs(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-3 * time.Hour)
	a := newActivities(&fakeFetcher{}, newFakeFixtureRepo(), newFakeAliasRepo(), now)

	// Cycle 1: two brand-new fixtures → both changed (inserted).
	out1, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures: []apifootball.APIFixture{
			mkAPIFixture(1, "ns", kickoff, 40, 42),
			mkAPIFixture(2, "ns", kickoff, 33, 50),
		},
		ActivationWindow: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if len(out1.ChangedIDs) != 2 {
		t.Errorf("cycle 1 ChangedIDs = %v, want 2 (both new)", out1.ChangedIDs)
	}

	// Cycle 2: fixture 1 identical (→ not changed), fixture 2's kickoff moved
	// a day (→ changed).
	out2, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures: []apifootball.APIFixture{
			mkAPIFixture(1, "ns", kickoff, 40, 42),
			mkAPIFixture(2, "ns", kickoff.Add(24*time.Hour), 33, 50),
		},
		ActivationWindow: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if len(out2.ChangedIDs) != 1 || out2.ChangedIDs[0] != 2 {
		t.Errorf("cycle 2 ChangedIDs = %v, want [2] (kickoff moved; fixture 1 unchanged)", out2.ChangedIDs)
	}
}

// ── FetchFixturesForWindow ─────────────────────────────────────

func TestFetchFixturesForWindow_HappyPath(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	// Per-date responses: 2 fixtures on 7/8, nothing on 7/7 or 7/9/7/10.
	fetcher := &fakeFetcher{responseByDate: map[string][]apifootball.APIFixture{
		"2026-07-08": {
			mkAPIFixture(1, "ns", kickoff, 40, 42),
			mkAPIFixture(2, "ns", kickoff, 33, 50),
		},
	}}
	a := newActivities(fetcher, newFakeFixtureRepo(), newFakeAliasRepo(), kickoff.Add(-3*time.Hour))
	// Seed tracked teams so the filter KEEPS the day's fixtures — the
	// empty cache now fails closed (#174), so the happy path must track.
	if err := a.TeamRepo.Replace(context.Background(), []team.TrackedTeam{
		{ID: 40, RefreshedAt: kickoff}, {ID: 42, RefreshedAt: kickoff},
		{ID: 33, RefreshedAt: kickoff}, {ID: 50, RefreshedAt: kickoff},
	}); err != nil {
		t.Fatalf("seed tracked teams: %v", err)
	}

	out, err := a.FetchFixturesForDay(context.Background(), FetchFixturesForDayInput{Date: kickoff})
	if err != nil {
		t.Fatalf("FetchFixturesForDay: %v", err)
	}
	if out.Count != 2 || len(out.Fixtures) != 2 {
		t.Errorf("out = %+v, want 2 fixtures", out)
	}
	// All 4 teams tracked → both fixtures kept, none dropped.
	if out.FilteredOut != 0 {
		t.Errorf("FilteredOut = %d; want 0 (all teams tracked)", out.FilteredOut)
	}
	if fetcher.listFixturesCalls != 1 {
		t.Errorf("listFixturesCalls = %d; want 1 (single day)", fetcher.listFixturesCalls)
	}
	// Date param normalized to midnight UTC.
	if got := fetcher.lastCall.Date.Format("2006-01-02"); got != "2026-07-08" {
		t.Errorf("last call Date = %s; want 2026-07-08", got)
	}
}

// TestFetchFixturesForDay_EmptyCache_FailsClosed — #174: an empty
// tracked-teams cache must fetch NOTHING (not the whole world) and set
// TrackedCacheEmpty so the workflow logs loudly.
func TestFetchFixturesForDay_EmptyCache_FailsClosed(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{responseByDate: map[string][]apifootball.APIFixture{
		"2026-07-08": {mkAPIFixture(1, "ns", kickoff, 40, 42), mkAPIFixture(2, "ns", kickoff, 33, 50)},
	}}
	// newActivities seeds an EMPTY TeamRepo — exactly the fail-closed trigger.
	a := newActivities(fetcher, newFakeFixtureRepo(), newFakeAliasRepo(), kickoff.Add(-3*time.Hour))

	out, err := a.FetchFixturesForDay(context.Background(), FetchFixturesForDayInput{Date: kickoff})
	if err != nil {
		t.Fatalf("FetchFixturesForDay: %v", err)
	}
	if out.Count != 0 || len(out.Fixtures) != 0 {
		t.Errorf("empty cache should fetch nothing; got Count=%d fixtures=%d", out.Count, len(out.Fixtures))
	}
	if !out.TrackedCacheEmpty {
		t.Error("TrackedCacheEmpty = false; want true (empty cache → fail closed)")
	}
	if out.FilteredOut != 2 {
		t.Errorf("FilteredOut = %d; want 2 (all vendor fixtures dropped)", out.FilteredOut)
	}
}

func TestFetchFixturesForDay_PropagatesError(t *testing.T) {
	fetcher := &fakeFetcher{err: errors.New("simulated transport failure")}
	a := newActivities(fetcher, newFakeFixtureRepo(), newFakeAliasRepo(), time.Now().UTC())
	_, err := a.FetchFixturesForDay(context.Background(), FetchFixturesForDayInput{Date: time.Now()})
	if err == nil {
		t.Fatal("expected error from fetcher, got nil")
	}
}

// ── FetchFixturesByIDs ─────────────────────────────────────────

func TestFetchFixturesByIDs_HappyPath(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{byIDsResponse: []apifootball.APIFixture{
		mkAPIFixture(1_515_514, "ns", kickoff, 40, 42),
		mkAPIFixture(1_515_515, "ns", kickoff, 33, 50),
	}}
	a := newActivities(fetcher, newFakeFixtureRepo(), newFakeAliasRepo(), kickoff.Add(-3*time.Hour))

	out, err := a.FetchFixturesByIDs(context.Background(), FetchFixturesByIDsInput{
		IDs: []int64{1_515_514, 1_515_515},
	})
	if err != nil {
		t.Fatalf("FetchFixturesByIDs: %v", err)
	}
	if out.Count != 2 || len(out.Fixtures) != 2 {
		t.Errorf("out = %+v, want 2 fixtures", out)
	}
	if len(fetcher.byIDsLastCall) != 2 || fetcher.byIDsLastCall[0] != 1_515_514 {
		t.Errorf("byIDsLastCall = %v, want [1515514, 1515515]", fetcher.byIDsLastCall)
	}
}

func TestFetchFixturesByIDs_PropagatesError(t *testing.T) {
	fetcher := &fakeFetcher{byIDsErr: errors.New("simulated api-sports failure")}
	a := newActivities(fetcher, newFakeFixtureRepo(), newFakeAliasRepo(), time.Now().UTC())
	_, err := a.FetchFixturesByIDs(context.Background(), FetchFixturesByIDsInput{IDs: []int64{1}})
	if err == nil {
		t.Fatal("expected error from fetcher, got nil")
	}
}

// ── CategorizeAndUpsertFixtures ────────────────────────────────

func TestCategorize_NewStagingKickoffFar_LandsStaging(t *testing.T) {
	kickoff := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-48 * time.Hour)
	fRepo := newFakeFixtureRepo()
	a := newActivities(&fakeFetcher{}, fRepo, newFakeAliasRepo(), now)

	out, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "ns", kickoff, 40, 42)},
		ActivationWindow: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	if out.Staging != 1 || out.Active != 0 || out.Completed != 0 {
		t.Errorf("counts = %+v, want Staging=1", out)
	}
	stored, _ := fRepo.Get(context.Background(), 1)
	if stored.State != fixture.StateStaging || stored.ActivatedAt != nil {
		t.Errorf("stored state wrong: %+v", stored)
	}
}

func TestCategorize_NewStagingKickoffImminent_LandsActive(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-10 * time.Minute) // within 30 min window
	fRepo := newFakeFixtureRepo()
	a := newActivities(&fakeFetcher{}, fRepo, newFakeAliasRepo(), now)

	out, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "ns", kickoff, 40, 42)},
		ActivationWindow: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	if out.Active != 1 {
		t.Errorf("counts = %+v, want Active=1", out)
	}
	stored, _ := fRepo.Get(context.Background(), 1)
	if stored.State != fixture.StateActive || stored.ActivatedAt == nil {
		t.Errorf("expected active with ActivatedAt set; got %+v", stored)
	}
}

func TestCategorize_APILive_LandsActive(t *testing.T) {
	// Emergency-activation case: API says match is already playing but
	// our DB doesn't have it. Insert as active immediately.
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(15 * time.Minute) // during match
	fRepo := newFakeFixtureRepo()
	a := newActivities(&fakeFetcher{}, fRepo, newFakeAliasRepo(), now)

	_, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "1h", kickoff, 40, 42)},
		ActivationWindow: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	stored, _ := fRepo.Get(context.Background(), 1)
	if stored.State != fixture.StateActive {
		t.Errorf("state = %q, want active (emergency activation)", stored.State)
	}
}

func TestCategorize_APITerminal_LandsCompleted(t *testing.T) {
	// Missed-the-match case: fixture ended yesterday, we ingest today.
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(48 * time.Hour)
	fRepo := newFakeFixtureRepo()
	a := newActivities(&fakeFetcher{}, fRepo, newFakeAliasRepo(), now)

	_, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "ft", kickoff, 40, 42)},
		ActivationWindow: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	stored, _ := fRepo.Get(context.Background(), 1)
	if stored.State != fixture.StateCompleted {
		t.Errorf("state = %q, want completed", stored.State)
	}
	if stored.ActivatedAt == nil || stored.CompletedAt == nil {
		t.Errorf("completed row missing timestamps: %+v", stored)
	}
}

// Load-bearing merge behavior: a fixture already active in our DB
// (activated_at set) must not have its activated_at cleared by
// re-ingest, even if the API's translated Fixture has nil.
func TestCategorize_ExistingActive_PreservesDomainFields(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(30 * time.Minute) // 30 min into the match
	fRepo := newFakeFixtureRepo()

	// Seed: fixture already active, activated 30 min before kickoff.
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
	if err := fRepo.Upsert(context.Background(), existing); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	// Now ingest fires; API returns 1H at 30' with score 1-0.
	a := newActivities(&fakeFetcher{}, fRepo, newFakeAliasRepo(), now)
	one := 1
	zero := 0
	elapsed := 30
	fx := mkAPIFixture(1, "1h", kickoff, 40, 42)
	fx.Fixture.Status.Elapsed = &elapsed
	fx.Goals.Home = &one
	fx.Goals.Away = &zero

	_, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures: []apifootball.APIFixture{fx}, ActivationWindow: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	after, _ := fRepo.Get(context.Background(), 1)
	if after.ActivatedAt == nil || !after.ActivatedAt.Equal(activatedAt) {
		t.Errorf("ActivatedAt should be preserved; got %v want %v", after.ActivatedAt, activatedAt)
	}
	if after.HomeScore == nil || *after.HomeScore != 1 {
		t.Errorf("HomeScore = %v, want 1 (refreshed from API)", after.HomeScore)
	}
	if after.APIElapsed == nil || *after.APIElapsed != 30 {
		t.Errorf("APIElapsed = %v, want 30 (refreshed)", after.APIElapsed)
	}
}

func TestCategorize_CollectsUniqueTeams(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-48 * time.Hour)
	a := newActivities(&fakeFetcher{}, newFakeFixtureRepo(), newFakeAliasRepo(), now)

	// Two fixtures, team 40 in both — should appear once in TeamRefs.
	out, err := a.CategorizeAndUpsertFixtures(context.Background(), CategorizeInput{
		Fixtures: []apifootball.APIFixture{
			mkAPIFixture(1, "ns", kickoff, 40, 42),
			mkAPIFixture(2, "ns", kickoff, 40, 33),
		},
		ActivationWindow: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	if len(out.TeamRefs) != 3 {
		t.Errorf("unique TeamRefs = %d, want 3 (teams 40, 42, 33)", len(out.TeamRefs))
	}
}
