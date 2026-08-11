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
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/infra/wikipedia"
)

// stubWD is a minimal alias.WikidataFetcher — always empty. Combined
// with stubWP (below), lets us exercise ResolveAliasesForTeams
// end-to-end (including the GetTeamProfile enrichment call and the
// vendor-field upsert path) without depending on live Wikidata or
// Wikipedia. Wikidata-side outcomes end up as NoMatch, which is fine —
// this test asserts on the enrichment side.
type stubWD struct{}

func (stubWD) GetEntity(_ context.Context, qid string) (*wikidata.Entity, error) {
	return nil, fmt.Errorf("stubWD.GetEntity(%s): no entity", qid)
}

func (stubWD) BatchGetP31(_ context.Context, _ []string) (map[string][]string, error) {
	return map[string][]string{}, nil
}

// stubWP is a minimal alias.WikipediaResolver — always empty. Same
// role as stubWD for the Wikipedia side of the lookup pipeline. Every
// Resolve call ends in NoMatch, exercising the enrichment path without
// depending on live vendor endpoints.
type stubWP struct{}

func (stubWP) SearchAndResolve(_ context.Context, _ string, _ wikipedia.SearchOpts) ([]wikipedia.Hit, error) {
	return nil, nil
}

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

	// GetTeamProfile behavior — per-team profile map. If not set,
	// returns (nil, error), which the activity soft-fails on.
	profileByID  map[int64]*apifootball.APITeamEnvelope
	profileErr   error
	profileCalls []int64
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

func (f *fakeFetcher) GetTeamProfile(_ context.Context, teamID int64) (*apifootball.APITeamEnvelope, error) {
	f.profileCalls = append(f.profileCalls, teamID)
	if f.profileErr != nil {
		return nil, f.profileErr
	}
	p, ok := f.profileByID[teamID]
	if !ok {
		return nil, fmt.Errorf("fakeFetcher.GetTeamProfile: no canned profile for team %d", teamID)
	}
	return p, nil
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
	mu          sync.Mutex
	teams       []team.TrackedTeam
	refreshedAt time.Time
	hasRefresh  bool
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

func (r *fakeTeamRepo) OldestRefreshedAt(_ context.Context) (time.Time, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshedAt, r.hasRefresh, nil
}

func (r *fakeTeamRepo) Replace(_ context.Context, teams []team.TrackedTeam, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.teams = append(r.teams[:0], teams...)
	r.refreshedAt = at
	r.hasRefresh = true
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

	out, err := a.FetchFixturesForDay(context.Background(), FetchFixturesForDayInput{Date: kickoff})
	if err != nil {
		t.Fatalf("FetchFixturesForDay: %v", err)
	}
	if out.Count != 2 || len(out.Fixtures) != 2 {
		t.Errorf("out = %+v, want 2 fixtures", out)
	}
	// Empty tracked-teams cache → fail-open (no filter). Verify.
	if out.FilteredOut != 0 {
		t.Errorf("FilteredOut = %d; want 0 (empty tracked cache)", out.FilteredOut)
	}
	if fetcher.listFixturesCalls != 1 {
		t.Errorf("listFixturesCalls = %d; want 1 (single day)", fetcher.listFixturesCalls)
	}
	// Date param normalized to midnight UTC.
	if got := fetcher.lastCall.Date.Format("2006-01-02"); got != "2026-07-08" {
		t.Errorf("last call Date = %s; want 2026-07-08", got)
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

// ── ResolveAliasesForTeams: enrichment path ──────────────────────

// strPtr — local pointer-lifter for compact test setup.
func strPtr(s string) *string { return &s }

// TestResolveAliasesForTeams_EnrichesFromTeamProfile — cache-miss teams
// must have GetTeamProfile called; the returned venue.city, team.country,
// team.national, team.code must be persisted to team_aliases via
// UpsertVendorFields even when Wikidata resolution yields no match. This
// is the Python-parity behavior: enrichment is captured independently of
// alias resolution success.
func TestResolveAliasesForTeams_EnrichesFromTeamProfile(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{
		profileByID: map[int64]*apifootball.APITeamEnvelope{
			// AC Milan — venue.city "Milano" (native), country "Italy",
			// team.code "MIL", not national.
			489: {
				Team: apifootball.APITeam{
					ID:       489,
					Name:     "AC Milan",
					Code:     strPtr("MIL"),
					Country:  "Italy",
					National: false,
				},
				Venue: apifootball.APITeamVenue{
					Name: strPtr("Stadio Giuseppe Meazza"),
					City: strPtr("Milano"),
				},
			},
			// Japan national — team.national=true, venue.city "Saitama".
			12: {
				Team: apifootball.APITeam{
					ID:       12,
					Name:     "Japan",
					Code:     strPtr("JPN"),
					Country:  "Japan",
					National: true,
				},
				Venue: apifootball.APITeamVenue{
					Name: strPtr("Saitama Stadium 2002"),
					City: strPtr("Saitama"),
				},
			},
		},
	}
	aRepo := newFakeAliasRepo()
	a := newActivities(fetcher, newFakeFixtureRepo(), aRepo, now)
	a.AliasResolver = alias.NewResolver(stubWD{}, stubWP{})
	// Zero throttle keeps the test fast.
	a.AliasThrottle = 0

	// Pre-seed placeholders WITHOUT enrichment (like EnsureAliasPlaceholders
	// does today with just league.country as the country hint).
	seedCountry := strPtr("EnglandFromLeague") // wrong on purpose — enrichment MUST overwrite
	seedTeams := []TeamRef{
		{TeamID: 489, TeamName: "AC Milan", Country: seedCountry},
		{TeamID: 12, TeamName: "Japan", Country: seedCountry, IsNational: false /* wrong; will be flipped */},
	}
	for _, tr := range seedTeams {
		placeholder := alias.New(tr.TeamID, tr.TeamName, tr.IsNational, nil, tr.Country, nil, now)
		if err := aRepo.UpsertVendorFields(context.Background(), placeholder); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	out, err := a.ResolveAliasesForTeams(context.Background(), ResolveAliasesForTeamsInput{Teams: seedTeams})
	if err != nil {
		t.Fatalf("ResolveAliasesForTeams: %v", err)
	}
	// Stub WD returns nothing → both teams register as NoMatch. We're
	// asserting on enrichment side, not resolution.
	if out.NoMatch != 2 {
		t.Errorf("NoMatch = %d; want 2 (stub WD returns nothing)", out.NoMatch)
	}
	if out.Resolved != 0 {
		t.Errorf("Resolved = %d; want 0", out.Resolved)
	}

	// GetTeamProfile MUST have been called once per cache-miss team.
	if len(fetcher.profileCalls) != 2 {
		t.Fatalf("profile calls = %v; want [489, 12]", fetcher.profileCalls)
	}

	// AC Milan row: enrichment overwrote league.country ("EnglandFromLeague"
	// → "Italy") and populated city ("Milano") + team_code ("MIL").
	milan, err := aRepo.Get(context.Background(), 489)
	if err != nil {
		t.Fatalf("Get AC Milan: %v", err)
	}
	if milan.Country == nil || *milan.Country != "Italy" {
		t.Errorf("AC Milan country = %v; want Italy (enriched from profile)", milan.Country)
	}
	if milan.City == nil || *milan.City != "Milano" {
		t.Errorf("AC Milan city = %v; want Milano (from profile.venue.city)", milan.City)
	}
	if milan.TeamCode == nil || *milan.TeamCode != "MIL" {
		t.Errorf("AC Milan team_code = %v; want MIL (from profile.team.code)", milan.TeamCode)
	}
	if milan.IsNational != false {
		t.Errorf("AC Milan is_national = %v; want false", milan.IsNational)
	}

	// Japan row: is_national flipped false→true from profile.
	japan, err := aRepo.Get(context.Background(), 12)
	if err != nil {
		t.Fatalf("Get Japan: %v", err)
	}
	if japan.IsNational != true {
		t.Errorf("Japan is_national = %v; want true (from profile.team.national)", japan.IsNational)
	}
	if japan.Country == nil || *japan.Country != "Japan" {
		t.Errorf("Japan country = %v; want Japan", japan.Country)
	}
	if japan.City == nil || *japan.City != "Saitama" {
		t.Errorf("Japan city = %v; want Saitama", japan.City)
	}
	if japan.TeamCode == nil || *japan.TeamCode != "JPN" {
		t.Errorf("Japan team_code = %v; want JPN", japan.TeamCode)
	}
}

// TestResolveAliasesForTeams_ProfileFetchSoftFails — if GetTeamProfile
// returns an error, the activity keeps going with the TeamRef values it
// already had (city/country/is_national from Ingest's fixture snapshot).
// The failure is recorded in Errors but doesn't stop the loop.
func TestResolveAliasesForTeams_ProfileFetchSoftFails(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{
		// No profileByID entries → every call returns an error.
	}
	aRepo := newFakeAliasRepo()
	a := newActivities(fetcher, newFakeFixtureRepo(), aRepo, now)
	a.AliasResolver = alias.NewResolver(stubWD{}, stubWP{})
	a.AliasThrottle = 0

	teams := []TeamRef{
		{TeamID: 999, TeamName: "Nowhere FC", Country: strPtr("Nowhere")},
	}
	out, err := a.ResolveAliasesForTeams(context.Background(), ResolveAliasesForTeamsInput{Teams: teams})
	if err != nil {
		t.Fatalf("ResolveAliasesForTeams: %v", err)
	}
	if len(out.Errors) == 0 {
		t.Fatal("expected GetTeamProfile failure to surface in Errors")
	}
	if !strings.Contains(strings.Join(out.Errors, "\n"), "GetTeamProfile team=999") {
		t.Errorf("Errors missing GetTeamProfile team=999 context: %+v", out.Errors)
	}
	// The vendor row should still exist with the fallback (TeamRef) values.
	row, err := aRepo.Get(context.Background(), 999)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Country == nil || *row.Country != "Nowhere" {
		t.Errorf("Country = %v; want fallback 'Nowhere' from TeamRef", row.Country)
	}
	// Wikidata stub returns no match → NoMatch=1, Resolved=0.
	if out.NoMatch != 1 || out.Resolved != 0 {
		t.Errorf("counts = NoMatch=%d Resolved=%d; want NoMatch=1 Resolved=0", out.NoMatch, out.Resolved)
	}
}

// TestResolveAliasesForTeams_CacheHitSkipsProfileFetch — teams that
// already have wikidata_qid set must NOT get GetTeamProfile called.
// The whole point of the cache is to avoid the vendor round-trip.
func TestResolveAliasesForTeams_CacheHitSkipsProfileFetch(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{}
	aRepo := newFakeAliasRepo()

	// Seed a fully-resolved row for team 489.
	resolved := alias.New(489, "AC Milan", false, strPtr("MIL"), strPtr("Italy"), strPtr("Milano"), now.Add(-24*time.Hour))
	resolved.SetResolution("Q1543", []string{"milan", "rossoneri"}, now.Add(-24*time.Hour))
	if err := aRepo.UpsertResolution(context.Background(), resolved); err != nil {
		t.Fatalf("seed: %v", err)
	}

	a := newActivities(fetcher, newFakeFixtureRepo(), aRepo, now)
	a.AliasResolver = alias.NewResolver(stubWD{}, stubWP{})
	a.AliasThrottle = 0

	out, err := a.ResolveAliasesForTeams(context.Background(), ResolveAliasesForTeamsInput{
		Teams: []TeamRef{{TeamID: 489, TeamName: "AC Milan"}},
	})
	if err != nil {
		t.Fatalf("ResolveAliasesForTeams: %v", err)
	}
	if out.CacheHits != 1 {
		t.Errorf("CacheHits = %d; want 1", out.CacheHits)
	}
	if len(fetcher.profileCalls) != 0 {
		t.Errorf("GetTeamProfile called %v times on cache-hit; want 0", len(fetcher.profileCalls))
	}
}
