// Unit tests for the ingest activities. Uses in-memory fake repos +
// a fake fixtureFetcher — no containers, no Temporal harness.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/team"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// ── fakes ──────────────────────────────────────────────────────

// fakeFetcher records the params it was called with + returns
// canned data. Supports both fetch shapes; tests set whichever
// response field applies.
type fakeFetcher struct {
	// ListFixtures behavior
	response          []apifootball.APIFixture
	responseByDate    map[string][]apifootball.APIFixture // optional per-date map keyed YYYY-MM-DD
	err               error
	lastCall          apifootball.FixtureListParams
	listFixturesCalls int

	// ListFixturesByIDs behavior
	byIDsResponse  []apifootball.APIFixture
	byIDsFailedIDs []int64 // set to non-nil to simulate partial failure
	byIDsErr       error
	byIDsLastCall  []int64

	// GetCurrentSeason behavior — per-league map. If not set, returns
	// a default (2026) so tests that don't care about season don't
	// need to configure it.
	seasonByLeague map[int]int
	seasonErr      error

	// ListTeamsForLeague behavior — per-(league,season) map. If not
	// set, returns nil (empty).
	teamsByLeague map[int][]apifootball.APITeam
	teamsErr      error
}

func (f *fakeFetcher) ListFixtures(_ context.Context, params apifootball.FixtureListParams) ([]apifootball.APIFixture, error) {
	f.lastCall = params
	f.listFixturesCalls++
	// If test set a per-date map, use it. Else fall back to the single
	// f.response (returned on the FIRST call only, empty after) so the
	// per-date loop doesn't multiply an intended-once response by N.
	if f.responseByDate != nil {
		if !params.Date.IsZero() {
			return f.responseByDate[params.Date.Format("2006-01-02")], f.err
		}
	}
	if f.listFixturesCalls > 1 {
		return nil, f.err
	}
	return f.response, f.err
}

func (f *fakeFetcher) ListFixturesByIDs(_ context.Context, ids []int64) (
	[]apifootball.APIFixture, []int64, error,
) {
	f.byIDsLastCall = ids
	return f.byIDsResponse, f.byIDsFailedIDs, f.byIDsErr
}

func (f *fakeFetcher) GetCurrentSeason(_ context.Context, leagueID int) (int, error) {
	if f.seasonErr != nil {
		return 0, f.seasonErr
	}
	if s, ok := f.seasonByLeague[leagueID]; ok {
		return s, nil
	}
	return 2026, nil // safe default for tests that don't care
}

func (f *fakeFetcher) ListTeamsForLeague(_ context.Context, leagueID, _ int) ([]apifootball.APITeam, error) {
	if f.teamsErr != nil {
		return nil, f.teamsErr
	}
	return f.teamsByLeague[leagueID], nil
}

// fakeFixtureRepo — in-memory Repo satisfying fixture.Repo.
// Only implements what the ingest activities call: Get, Upsert,
// PruneCompleted. Other methods panic — a test that reaches them
// signals scope drift.
type fakeFixtureRepo struct {
	mu   sync.Mutex
	data map[int64]*fixture.Fixture
}

func newFakeFixtureRepo() *fakeFixtureRepo {
	return &fakeFixtureRepo{data: make(map[int64]*fixture.Fixture)}
}

func (r *fakeFixtureRepo) Get(_ context.Context, id int64) (*fixture.Fixture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.data[id]
	if !ok {
		return nil, fixture.ErrNotFound
	}
	// Return a copy so callers don't mutate stored state.
	dup := *f
	return &dup, nil
}

func (r *fakeFixtureRepo) Upsert(_ context.Context, f *fixture.Fixture) error {
	if err := f.ValidateInvariants(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	dup := *f
	r.data[f.ID] = &dup
	return nil
}

func (r *fakeFixtureRepo) ListByState(context.Context, fixture.State) ([]*fixture.Fixture, error) {
	panic("fakeFixtureRepo.ListByState: not implemented (test scope drift)")
}
func (r *fakeFixtureRepo) ListActiveIDs(context.Context) ([]int64, error) {
	panic("fakeFixtureRepo.ListActiveIDs: not implemented (test scope drift)")
}
func (r *fakeFixtureRepo) ListStagingBeforeKickoff(context.Context, time.Time) ([]*fixture.Fixture, error) {
	panic("fakeFixtureRepo.ListStagingBeforeKickoff: not implemented (test scope drift)")
}
func (r *fakeFixtureRepo) FixtureReadyToComplete(context.Context, int64) (bool, error) {
	panic("fakeFixtureRepo.FixtureReadyToComplete: not implemented (test scope drift)")
}

func (r *fakeFixtureRepo) PruneCompleted(_ context.Context, threshold time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	deleted := 0
	for id, f := range r.data {
		if f.State == fixture.StateCompleted && f.CompletedAt != nil && f.CompletedAt.Before(threshold) {
			delete(r.data, id)
			deleted++
		}
	}
	return deleted, nil
}

// ListReclaimableEventIDs — the fake models no events or shares, so a
// clip-bearing event to reclaim never exists here. Returns empty; the
// PruneOldFixtures test asserts only the clipless Deleted count.
func (r *fakeFixtureRepo) ListReclaimableEventIDs(context.Context, time.Time) ([]uuid.UUID, error) {
	return nil, nil
}

// fakeAliasRepo — in-memory alias.Repo.
type fakeAliasRepo struct {
	mu   sync.Mutex
	data map[int]*alias.TeamAlias
}

func newFakeAliasRepo() *fakeAliasRepo {
	return &fakeAliasRepo{data: make(map[int]*alias.TeamAlias)}
}

func (r *fakeAliasRepo) Get(_ context.Context, teamID int) (*alias.TeamAlias, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ta, ok := r.data[teamID]
	if !ok {
		return nil, alias.ErrNotFound
	}
	dup := *ta
	return &dup, nil
}

func (r *fakeAliasRepo) BulkGet(_ context.Context, ids []int) (map[int]*alias.TeamAlias, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int]*alias.TeamAlias, len(ids))
	for _, id := range ids {
		if ta, ok := r.data[id]; ok {
			dup := *ta
			out[id] = &dup
		}
	}
	return out, nil
}

// UpsertVendorFields preserves any phase-2 resolution data already
// in the fake store (mirrors the pg adapter's COALESCE-ish semantics).
func (r *fakeAliasRepo) UpsertVendorFields(_ context.Context, ta *alias.TeamAlias) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	dup := *ta
	if existing, ok := r.data[ta.TeamID]; ok {
		// Preserve phase-2 fields from any prior resolution.
		dup.WikidataQID = existing.WikidataQID
		dup.Aliases = existing.Aliases
		dup.ResolvedAt = existing.ResolvedAt
	}
	r.data[ta.TeamID] = &dup
	return nil
}

// UpsertResolution writes a full row including phase-2 fields.
func (r *fakeAliasRepo) UpsertResolution(_ context.Context, ta *alias.TeamAlias) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	dup := *ta
	r.data[ta.TeamID] = &dup
	return nil
}

// ── helpers ────────────────────────────────────────────────────

// fakeTeamRepo — in-memory team.Repo. Tests pre-populate via Replace
// (either directly or by seeding fresh via RefreshTrackedTeamsIfStale
// through the activity). Empty by default; FetchFixturesForWindow's
// fail-open semantics kick in when List returns empty.
type fakeTeamRepo struct {
	mu    sync.Mutex
	teams []team.TrackedTeam
}

func newFakeTeamRepo() *fakeTeamRepo {
	return &fakeTeamRepo{}
}

func (r *fakeTeamRepo) List(_ context.Context) ([]team.TrackedTeam, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dup := make([]team.TrackedTeam, len(r.teams))
	copy(dup, r.teams)
	return dup, nil
}

// OldestRefreshedAt mirrors the real pg repo: MIN(refreshed_at) across the
// rows, so a preserved league's old timestamp keeps the cache "stale" and
// drives a retry.
func (r *fakeTeamRepo) OldestRefreshedAt(_ context.Context) (time.Time, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.teams) == 0 {
		return time.Time{}, false, nil
	}
	oldest := r.teams[0].RefreshedAt
	for _, t := range r.teams[1:] {
		if t.RefreshedAt.Before(oldest) {
			oldest = t.RefreshedAt
		}
	}
	return oldest, true, nil
}

func (r *fakeTeamRepo) Replace(_ context.Context, teams []team.TrackedTeam) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.teams = append(r.teams[:0], teams...)
	return nil
}

func mkAPIFixture(id int64, status string, kickoff time.Time, homeID, awayID int) apifootball.APIFixture {
	return apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID:   id,
			Date: kickoff,
			Status: apifootball.APIFixtureStatus{
				Short: apifootball.APIStatusCode(status),
				Long:  status,
			},
		},
		League: apifootball.APIFixtureLeague{ID: 39, Name: "Premier League", Season: 2026, Country: "England"},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: homeID, Name: fmt.Sprintf("Team-%d", homeID)},
			Away: apifootball.APIFixtureTeam{ID: awayID, Name: fmt.Sprintf("Team-%d", awayID)},
		},
	}
}

func newActivities(fetcher fixtureFetcher, fRepo fixture.Repo, aRepo alias.Repo, now time.Time) *Activities {
	return &Activities{
		APIFootball:           fetcher,
		FixtureRepo:           fRepo,
		AliasRepo:             aRepo,
		TeamRepo:              newFakeTeamRepo(), // empty by default; tests can pre-populate via the returned struct
		TrackedLeagueIDs:      []int{39, 140, 78, 135, 61, 1},
		TopFlightCacheHours:   24,
		FetchWindowFutureDays: 7,
		Now:                   func() time.Time { return now },
	}
}

// ── CategorizeAndUpsertFixtures — N6 change detection ──────────

// TestCategorizeAndUpsertFixtures_ChangedIDs — new fixtures always count as
// changed; a re-ingest reports only the ones whose meaningful fields moved (an
// identical row is NOT a fixture.update; a kickoff reschedule is).
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

// ── EnsureAliasPlaceholders ────────────────────────────────────

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

// ── PruneOldFixtures ───────────────────────────────────────────

func TestPruneOldFixtures(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()

	// Two completed fixtures at different completion times.
	old := fixture.New(1, fixture.APIStatus{Short: "ft"},
		now.Add(-30*24*time.Hour),
		fixture.Team{ID: 40}, fixture.Team{ID: 42},
		fixture.League{ID: 39, Season: 2026})
	if err := old.Activate(now.Add(-30 * 24 * time.Hour)); err != nil {
		t.Fatalf("seed activate: %v", err)
	}
	if err := old.Complete(now.Add(-30 * 24 * time.Hour)); err != nil {
		t.Fatalf("seed complete: %v", err)
	}
	fRepo.Upsert(context.Background(), old)

	recent := fixture.New(2, fixture.APIStatus{Short: "ft"},
		now.Add(-7*24*time.Hour),
		fixture.Team{ID: 33}, fixture.Team{ID: 50},
		fixture.League{ID: 39, Season: 2026})
	recent.Activate(now.Add(-7 * 24 * time.Hour))
	recent.Complete(now.Add(-7 * 24 * time.Hour))
	fRepo.Upsert(context.Background(), recent)

	a := newActivities(&fakeFetcher{}, fRepo, newFakeAliasRepo(), now)
	threshold := now.Add(-14 * 24 * time.Hour) // 14 days ago
	out, err := a.PruneOldFixtures(context.Background(), PruneOldFixturesInput{Threshold: threshold})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if out.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 (only the 30-day-old fixture)", out.Deleted)
	}
}

// ── LastPolledAt regression tests ──────────────────────────────

// TestCategorize_SetsLastPolledAt_OnFresh — fresh ingest MUST record
// that we just hit the API. Otherwise the future monitor's poll-
// bucket logic will re-poll every fixture on its first cycle.
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

// ── RefreshTrackedTeamsIfStale — partial-failure safety (audit P1-1) ─────────

// TestRefreshTrackedTeamsIfStale_PartialFailurePreservesLeague is the core
// regression guard: when one configured league returns an empty roster
// (season rollover) — or errors — its prior cached rows must be carried
// forward, NOT wiped, and keep their old timestamp so the next run retries.
// Before the fix, the whole-cache Replace dropped that league for 24h.
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
