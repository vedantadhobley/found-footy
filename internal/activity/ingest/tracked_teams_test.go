// Tracked-team cache refresh activity tests.
package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/team"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// TestRefreshTrackedTeamsIfStale_PartialFailurePreservesLeague requires an
// empty or failed league refresh to retain its old rows and stale timestamp.
func TestRefreshTrackedTeamsIfStale_PartialFailurePreservesLeague(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour) // stale → triggers a refresh

	tr := newFakeTeamRepo()
	tr.teams = []team.TrackedTeam{
		{ID: 39001, Name: "old-PL", LeagueID: 39, Season: 2025, RefreshedAt: old},
		{ID: 140001, Name: "old-LL", LeagueID: 140, Season: 2025, RefreshedAt: old},
		{ID: 78001, Name: "old-Bundes", LeagueID: 78, Season: 2025, RefreshedAt: old},
	}
	fetcher := &fakeFetcher{
		teamsByLeague: map[int][]apifootball.APITeam{
			39:  {{ID: 39100, Name: "Arsenal"}},
			140: {{ID: 140100, Name: "Barcelona"}},
			// 78 omitted → ListTeamsForLeague returns nil → empty roster.
		},
	}
	a := &Activities{
		APIFootball:         fetcher,
		TeamRepo:            tr,
		TrackedLeagueIDs:    []int{39, 140, 78},
		TopFlightCacheHours: 24,
		Now:                 func() time.Time { return now },
	}

	out, err := a.RefreshTrackedTeamsIfStale(ctx, RefreshTrackedTeamsIfStaleInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Refreshed {
		t.Fatal("expected Refreshed=true on partial success")
	}
	if out.PreservedLeagues[78] != 1 {
		t.Errorf("PreservedLeagues[78] = %d, want 1", out.PreservedLeagues[78])
	}

	got, _ := tr.List(ctx)
	byLeague := map[int]team.TrackedTeam{}
	for _, tm := range got {
		byLeague[tm.LeagueID] = tm
	}
	if len(got) != 3 {
		t.Fatalf("cache has %d teams, want 3 (2 fresh + 1 preserved): %+v", len(got), got)
	}
	// 39 refreshed → new team id, stamped now.
	if byLeague[39].ID != 39100 || !byLeague[39].RefreshedAt.Equal(now) {
		t.Errorf("league 39 = %+v, want fresh id 39100 @ now", byLeague[39])
	}
	// 78 preserved → OLD id, OLD timestamp (so the next cycle retries it).
	if byLeague[78].ID != 78001 || !byLeague[78].RefreshedAt.Equal(old) {
		t.Errorf("league 78 = %+v, want preserved id 78001 @ old", byLeague[78])
	}
	// Oldest reflects the stale preserved league → cache is not masked fresh.
	oldest, ok, _ := tr.OldestRefreshedAt(ctx)
	if !ok || !oldest.Equal(old) {
		t.Errorf("OldestRefreshedAt = %v (ok=%v), want %v", oldest, ok, old)
	}
}

// TestRefreshTrackedTeamsIfStale_AllEmptyLeavesCacheUntouched — when NO league
// returns teams (total failure/empty), abort with an error and leave the prior
// cache exactly as it was, rather than wiping it to nothing.
func TestRefreshTrackedTeamsIfStale_AllEmptyLeavesCacheUntouched(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)

	tr := newFakeTeamRepo()
	seeded := []team.TrackedTeam{
		{ID: 39001, Name: "old-PL", LeagueID: 39, RefreshedAt: old},
		{ID: 78001, Name: "old-Bundes", LeagueID: 78, RefreshedAt: old},
	}
	tr.teams = append([]team.TrackedTeam(nil), seeded...)

	fetcher := &fakeFetcher{teamsByLeague: map[int][]apifootball.APITeam{}} // every league empty
	a := &Activities{
		APIFootball:         fetcher,
		TeamRepo:            tr,
		TrackedLeagueIDs:    []int{39, 140, 78},
		TopFlightCacheHours: 24,
		Now:                 func() time.Time { return now },
	}

	if _, err := a.RefreshTrackedTeamsIfStale(ctx, RefreshTrackedTeamsIfStaleInput{}); err == nil {
		t.Fatal("expected error when no league returns teams")
	}
	got, _ := tr.List(ctx)
	if len(got) != len(seeded) {
		t.Fatalf("cache mutated on total failure: have %d, want %d untouched", len(got), len(seeded))
	}
}

// TestRefreshTrackedTeamsIfStale_AllFreshFullReplace — the happy path is
// unchanged: every league returns teams, nothing is preserved, all rows are
// stamped now.
func TestRefreshTrackedTeamsIfStale_AllFreshFullReplace(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)

	tr := newFakeTeamRepo() // empty cache → refresh runs
	fetcher := &fakeFetcher{
		teamsByLeague: map[int][]apifootball.APITeam{
			39:  {{ID: 39100, Name: "Arsenal"}},
			140: {{ID: 140100, Name: "Barcelona"}},
		},
	}
	a := &Activities{
		APIFootball:         fetcher,
		TeamRepo:            tr,
		TrackedLeagueIDs:    []int{39, 140},
		TopFlightCacheHours: 24,
		Now:                 func() time.Time { return now },
	}

	out, err := a.RefreshTrackedTeamsIfStale(ctx, RefreshTrackedTeamsIfStaleInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.PreservedLeagues) != 0 {
		t.Errorf("expected no preserved leagues on full success, got %+v", out.PreservedLeagues)
	}
	got, _ := tr.List(ctx)
	if len(got) != 2 {
		t.Fatalf("cache has %d, want 2", len(got))
	}
	for _, tm := range got {
		if !tm.RefreshedAt.Equal(now) {
			t.Errorf("team %d refreshedAt=%v, want now (%v)", tm.ID, tm.RefreshedAt, now)
		}
	}
}
