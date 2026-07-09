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

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// ── fakes ──────────────────────────────────────────────────────

// fakeFetcher records the params it was called with + returns
// canned data. Supports both fetch shapes; tests set whichever
// response field applies.
type fakeFetcher struct {
	// ListFixtures behavior
	response []apifootball.APIFixture
	err      error
	lastCall apifootball.FixtureListParams

	// ListFixturesByIDs behavior
	byIDsResponse  []apifootball.APIFixture
	byIDsFailedIDs []int64 // set to non-nil to simulate partial failure
	byIDsErr       error
	byIDsLastCall  []int64
}

func (f *fakeFetcher) ListFixtures(_ context.Context, params apifootball.FixtureListParams) ([]apifootball.APIFixture, error) {
	f.lastCall = params
	return f.response, f.err
}

func (f *fakeFetcher) ListFixturesByIDs(_ context.Context, ids []int64) (
	[]apifootball.APIFixture, []int64, error,
) {
	f.byIDsLastCall = ids
	return f.byIDsResponse, f.byIDsFailedIDs, f.byIDsErr
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

func (r *fakeAliasRepo) Upsert(_ context.Context, ta *alias.TeamAlias) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	dup := *ta
	r.data[ta.TeamID] = &dup
	return nil
}

// ── helpers ────────────────────────────────────────────────────

func mkAPIFixture(id int64, status string, kickoff time.Time, homeID, awayID int) apifootball.APIFixture {
	return apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID:   id,
			Date: kickoff,
			Status: apifootball.APIFixtureStatus{
				Short: status,
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
		APIFootball: fetcher,
		FixtureRepo: fRepo,
		AliasRepo:   aRepo,
		Now:         func() time.Time { return now },
	}
}

// ── FetchFixturesForWindow ─────────────────────────────────────

func TestFetchFixturesForWindow_HappyPath(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{response: []apifootball.APIFixture{
		mkAPIFixture(1, "NS", kickoff, 40, 42),
		mkAPIFixture(2, "NS", kickoff, 33, 50),
	}}
	a := newActivities(fetcher, newFakeFixtureRepo(), newFakeAliasRepo(), kickoff.Add(-3*time.Hour))

	out, err := a.FetchFixturesForWindow(context.Background(), FetchFixturesInput{
		From: kickoff.Add(-24 * time.Hour),
		To:   kickoff.Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatalf("FetchFixturesForWindow: %v", err)
	}
	if out.Count != 2 || len(out.Fixtures) != 2 {
		t.Errorf("out = %+v, want 2 fixtures", out)
	}
	if !fetcher.lastCall.From.Equal(kickoff.Add(-24 * time.Hour)) {
		t.Errorf("From mismatch: %v", fetcher.lastCall.From)
	}
}

func TestFetchFixturesForWindow_PropagatesError(t *testing.T) {
	fetcher := &fakeFetcher{err: errors.New("simulated transport failure")}
	a := newActivities(fetcher, newFakeFixtureRepo(), newFakeAliasRepo(), time.Now().UTC())
	_, err := a.FetchFixturesForWindow(context.Background(), FetchFixturesInput{
		From: time.Now(), To: time.Now().Add(24 * time.Hour),
	})
	if err == nil {
		t.Fatal("expected error from fetcher, got nil")
	}
}

// ── FetchFixturesByIDs ─────────────────────────────────────────

func TestFetchFixturesByIDs_HappyPath(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{byIDsResponse: []apifootball.APIFixture{
		mkAPIFixture(1_515_514, "NS", kickoff, 40, 42),
		mkAPIFixture(1_515_515, "NS", kickoff, 33, 50),
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
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "NS", kickoff, 40, 42)},
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
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "NS", kickoff, 40, 42)},
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
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "1H", kickoff, 40, 42)},
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
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "FT", kickoff, 40, 42)},
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
		fixture.APIStatus{Short: "1H", Long: "First Half"},
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
	fx := mkAPIFixture(1, "1H", kickoff, 40, 42)
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
			mkAPIFixture(1, "NS", kickoff, 40, 42),
			mkAPIFixture(2, "NS", kickoff, 40, 33),
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
	seed := alias.New(40, "Liverpool", false, nil, nil, now.Add(-24*time.Hour))
	if err := aRepo.Upsert(context.Background(), seed); err != nil {
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
	if ta.HasWikidataResolution() || ta.HasTwitterAliases() {
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
	old := fixture.New(1, fixture.APIStatus{Short: "FT"},
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

	recent := fixture.New(2, fixture.APIStatus{Short: "FT"},
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
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "NS", kickoff, 40, 42)},
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
		fixture.APIStatus{Short: "1H", Long: "First Half"},
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
		Fixtures:         []apifootball.APIFixture{mkAPIFixture(1, "1H", kickoff, 40, 42)},
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
			mkAPIFixture(111, "NS", kickoff, 40, 42),
			mkAPIFixture(222, "NS", kickoff, 33, 50),
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
