// Unit tests for the ingest activities. Uses in-memory fake repos +
// a fake fixtureFetcher — no containers, no Temporal harness.
package ingest

import (
	"context"
	"fmt"
	"sync"
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

func (r *fakeFixtureRepo) StoreFromIngest(ctx context.Context, f *fixture.Fixture) (fixture.State, error) {
	if err := r.Upsert(ctx, f); err != nil {
		return "", err
	}
	return f.State, nil
}

func (r *fakeFixtureRepo) RefreshActivePoll(context.Context, *fixture.Fixture) (bool, error) {
	panic("fakeFixtureRepo.RefreshActivePoll: not implemented (test scope drift)")
}

func (r *fakeFixtureRepo) RefreshStagingPoll(context.Context, *fixture.Fixture) (bool, error) {
	panic("fakeFixtureRepo.RefreshStagingPoll: not implemented (test scope drift)")
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
func (r *fakeFixtureRepo) AssessCompletion(context.Context, int64, time.Time) (fixture.CompletionAssessment, error) {
	panic("fakeFixtureRepo.AssessCompletion: not implemented (test scope drift)")
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
