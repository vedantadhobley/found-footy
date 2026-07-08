// Unit tests for the monitor activities. In-memory fakes for
// event.Repo, fixture.Repo, and the fixtureFetcher. Deterministic
// clock via Activities.Now.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// ── fakes ──────────────────────────────────────────────────────

type fakeFetcher struct {
	response []apifootball.APIFixture
	err      error
	lastIDs  []int64
}

func (f *fakeFetcher) ListFixturesByIDs(_ context.Context, ids []int64) ([]apifootball.APIFixture, error) {
	f.lastIDs = ids
	return f.response, f.err
}

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
func (r *fakeFixtureRepo) ListByState(_ context.Context, state fixture.State) ([]*fixture.Fixture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*fixture.Fixture
	for _, f := range r.data {
		if f.State == state {
			dup := *f
			out = append(out, &dup)
		}
	}
	return out, nil
}
func (r *fakeFixtureRepo) ListActiveIDs(_ context.Context) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []int64
	for id, f := range r.data {
		if f.State == fixture.StateActive {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (r *fakeFixtureRepo) ListStagingBeforeKickoff(_ context.Context, threshold time.Time) ([]*fixture.Fixture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*fixture.Fixture
	for _, f := range r.data {
		if f.State == fixture.StateStaging && !f.Kickoff.After(threshold) {
			dup := *f
			out = append(out, &dup)
		}
	}
	return out, nil
}
func (r *fakeFixtureRepo) PruneCompleted(context.Context, time.Time) (int, error) {
	panic("fakeFixtureRepo.PruneCompleted: not implemented (test scope drift)")
}

// fakeEventRepo — in-memory event.Repo that supports the symmetric
// counter model. Tracks votes via a map keyed by (event_id,
// workflow_id) for idempotency.
type fakeEventRepo struct {
	mu       sync.Mutex
	events   map[uuid.UUID]*event.Event
	byKey    map[string]uuid.UUID // (fixture_id, natural_key) → id
	presence map[string]struct{}  // (event_id, workflow_id)
	absence  map[string]struct{}  // (event_id, workflow_id)
}

func newFakeEventRepo() *fakeEventRepo {
	return &fakeEventRepo{
		events:   make(map[uuid.UUID]*event.Event),
		byKey:    make(map[string]uuid.UUID),
		presence: make(map[string]struct{}),
		absence:  make(map[string]struct{}),
	}
}

func fkey(fixtureID int64, naturalKey string) string {
	return fmt.Sprintf("%d/%s", fixtureID, naturalKey)
}
func vkey(eventID uuid.UUID, workflowID string) string {
	return eventID.String() + "/" + workflowID
}

func (r *fakeEventRepo) Get(_ context.Context, id uuid.UUID) (*event.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.events[id]
	if !ok {
		return nil, event.ErrNotFound
	}
	dup := *e
	return &dup, nil
}
func (r *fakeEventRepo) GetByNaturalKey(_ context.Context, fixtureID int64, naturalKey string) (*event.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byKey[fkey(fixtureID, naturalKey)]
	if !ok {
		return nil, event.ErrNotFound
	}
	dup := *r.events[id]
	return &dup, nil
}
func (r *fakeEventRepo) Insert(_ context.Context, e *event.Event, workflowID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	fk := fkey(e.FixtureID, e.NaturalKey)
	if _, dup := r.byKey[fk]; dup {
		return errors.New("duplicate natural_key (fake)")
	}
	e.DebounceCount = 1
	e.DownstreamTriggered = false
	dupE := *e
	r.events[e.ID] = &dupE
	r.byKey[fk] = e.ID
	r.presence[vkey(e.ID, workflowID)] = struct{}{}
	return nil
}
func (r *fakeEventRepo) Upsert(_ context.Context, e *event.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	dupE := *e
	r.events[e.ID] = &dupE
	return nil
}
func (r *fakeEventRepo) ListPending(_ context.Context, fixtureID int64) ([]*event.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*event.Event
	for _, e := range r.events {
		if e.FixtureID == fixtureID && !e.Removed && (!e.MonitorComplete || !e.DownloadComplete) {
			dup := *e
			out = append(out, &dup)
		}
	}
	return out, nil
}
func (r *fakeEventRepo) RegisterEventPresence(_ context.Context, eventID uuid.UUID, workflowID string) (int, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.events[eventID]
	if !ok {
		return 0, false, event.ErrNotFound
	}
	vk := vkey(eventID, workflowID)
	if _, dupVote := r.presence[vk]; dupVote {
		return e.DebounceCount, false, nil
	}
	r.presence[vk] = struct{}{}
	if e.DebounceCount < 3 {
		e.DebounceCount++
	}
	justTriggered := false
	if e.DebounceCount == 3 && !e.DownstreamTriggered {
		e.DownstreamTriggered = true
		justTriggered = true
	}
	return e.DebounceCount, justTriggered, nil
}
func (r *fakeEventRepo) RegisterEventAbsence(_ context.Context, eventID uuid.UUID, workflowID string) (int, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.events[eventID]
	if !ok {
		return 0, false, event.ErrNotFound
	}
	vk := vkey(eventID, workflowID)
	if _, dupVote := r.absence[vk]; dupVote {
		return e.DebounceCount, false, nil
	}
	r.absence[vk] = struct{}{}
	if e.DebounceCount > 0 {
		e.DebounceCount--
	}
	hitZero := false
	if e.DebounceCount == 0 && !e.Removed {
		e.Removed = true
		reason := event.RemovalVAR
		e.RemovedReason = &reason
		now := time.Now().UTC()
		e.RemovedAt = &now
		hitZero = true
	}
	return e.DebounceCount, hitZero, nil
}
func (r *fakeEventRepo) RegisterVideoValidationWorkflow(context.Context, uuid.UUID, string, string) (int, error) {
	panic("fakeEventRepo.RegisterVideoValidationWorkflow: not implemented (test scope drift)")
}

// ── helpers ────────────────────────────────────────────────────

func mkActiveFixture(id int64, kickoff time.Time) *fixture.Fixture {
	f := fixture.New(id,
		fixture.APIStatus{Short: "1H", Long: "First Half"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Name: "Premier League", Season: 2026},
	)
	_ = f.Activate(kickoff)
	return f
}

func newActs(fetcher fixtureFetcher, fRepo fixture.Repo, eRepo event.Repo, now time.Time) *Activities {
	return &Activities{
		APIFootball: fetcher,
		FixtureRepo: fRepo,
		EventRepo:   eRepo,
		Now:         func() time.Time { return now },
	}
}

func mkAPIGoal(teamID int, playerID int, minute int) apifootball.APIFixtureEvent {
	pid := playerID
	pname := fmt.Sprintf("Player-%d", playerID)
	return apifootball.APIFixtureEvent{
		Time:   apifootball.APIFixtureEventTime{Elapsed: minute},
		Team:   apifootball.APIFixtureTeam{ID: teamID, Name: fmt.Sprintf("Team-%d", teamID)},
		Player: apifootball.APIFixturePlayerRef{ID: &pid, Name: &pname},
		Type:   "Goal",
		Detail: "Normal Goal",
	}
}

// ── PreActivateUpcoming ────────────────────────────────────────

func TestPreActivateUpcoming_ActivatesImminent(t *testing.T) {
	kickoff := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-10 * time.Minute)
	fRepo := newFakeFixtureRepo()
	staging := fixture.New(1,
		fixture.APIStatus{Short: "NS"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Season: 2026},
	)
	fRepo.Upsert(context.Background(), staging)

	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.PreActivateUpcoming(context.Background(), PreActivateUpcomingInput{Lookahead: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PreActivateUpcoming: %v", err)
	}
	if out.Considered != 1 || out.Activated != 1 {
		t.Errorf("out = %+v, want Considered=1 Activated=1", out)
	}
	got, _ := fRepo.Get(context.Background(), 1)
	if got.State != fixture.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
}

func TestPreActivateUpcoming_SkipsFarFuture(t *testing.T) {
	kickoff := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-48 * time.Hour) // 2 days out
	fRepo := newFakeFixtureRepo()
	staging := fixture.New(1,
		fixture.APIStatus{Short: "NS"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Season: 2026},
	)
	fRepo.Upsert(context.Background(), staging)

	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, _ := acts.PreActivateUpcoming(context.Background(), PreActivateUpcomingInput{Lookahead: 30 * time.Minute})
	if out.Considered != 0 || out.Activated != 0 {
		t.Errorf("out = %+v, want zero (fixture too far out)", out)
	}
}

// ── ListActiveFixtureIDs ───────────────────────────────────────

func TestListActiveFixtureIDs(t *testing.T) {
	fRepo := newFakeFixtureRepo()
	fRepo.Upsert(context.Background(), mkActiveFixture(101, time.Now()))
	fRepo.Upsert(context.Background(), mkActiveFixture(102, time.Now()))
	staging := fixture.New(103,
		fixture.APIStatus{Short: "NS"},
		time.Now().Add(24*time.Hour),
		fixture.Team{ID: 40}, fixture.Team{ID: 42},
		fixture.League{ID: 39, Season: 2026},
	)
	fRepo.Upsert(context.Background(), staging)

	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), time.Now().UTC())
	out, err := acts.ListActiveFixtureIDs(context.Background())
	if err != nil {
		t.Fatalf("ListActiveFixtureIDs: %v", err)
	}
	if len(out.IDs) != 2 {
		t.Errorf("got %d IDs, want 2 (staging excluded)", len(out.IDs))
	}
}

// ── FetchLiveFixtures ──────────────────────────────────────────

func TestFetchLiveFixtures_EmptyShortCircuits(t *testing.T) {
	fetcher := &fakeFetcher{}
	acts := newActs(fetcher, newFakeFixtureRepo(), newFakeEventRepo(), time.Now().UTC())
	out, err := acts.FetchLiveFixtures(context.Background(), FetchLiveFixturesInput{IDs: nil})
	if err != nil {
		t.Fatalf("FetchLiveFixtures: %v", err)
	}
	if len(out.Fixtures) != 0 {
		t.Errorf("expected empty; fetcher called: %v", fetcher.lastIDs)
	}
	if fetcher.lastIDs != nil {
		t.Error("empty input should NOT hit fetcher")
	}
}

// ── ReconcileFixture ───────────────────────────────────────────

func TestReconcileFixture_NewGoalInserted_CountIs1(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(30 * time.Minute)
	fRepo := newFakeFixtureRepo()
	fRepo.Upsert(context.Background(), mkActiveFixture(999, kickoff))
	eRepo := newFakeEventRepo()

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1H", Long: "First Half"}},
		Events:  []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: apiFix, WorkflowID: "monitor-w1",
	})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if out.NewEventsDetected != 1 {
		t.Errorf("NewEventsDetected = %d, want 1", out.NewEventsDetected)
	}
	if len(out.EventsBecameStable) != 0 {
		t.Errorf("EventsBecameStable = %v, want empty (count is 1, not 3)", out.EventsBecameStable)
	}
}

func TestReconcileFixture_ThreeCyclesTriggersDownstream(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(30 * time.Minute)
	fRepo := newFakeFixtureRepo()
	fRepo.Upsert(context.Background(), mkActiveFixture(999, kickoff))
	eRepo := newFakeEventRepo()

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1H"}},
		Events:  []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, now)

	// Cycle 1 — insert (count = 1)
	_, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	// Cycle 2 — presence vote (count = 2)
	out2, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w2"})
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if len(out2.EventsBecameStable) != 0 {
		t.Errorf("cycle 2 should not have triggered; got %v", out2.EventsBecameStable)
	}
	// Cycle 3 — presence vote (count = 3, TRIGGERS)
	out3, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w3"})
	if err != nil {
		t.Fatalf("cycle 3: %v", err)
	}
	if len(out3.EventsBecameStable) != 1 {
		t.Errorf("cycle 3 EventsBecameStable = %v, want 1 event", out3.EventsBecameStable)
	}
}

func TestReconcileFixture_AbsenceHitZeroSoftDeletes(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(30 * time.Minute)
	fRepo := newFakeFixtureRepo()
	fRepo.Upsert(context.Background(), mkActiveFixture(999, kickoff))
	eRepo := newFakeEventRepo()
	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1H"}},
		Events:  []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}

	acts := newActs(&fakeFetcher{}, fRepo, eRepo, now)
	// Insert event
	_, _ = acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})

	// Now the event vanishes from the API — one absence brings count 1→0
	empty := apiFix
	empty.Events = nil
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: empty, WorkflowID: "w2"})
	if err != nil {
		t.Fatalf("absence cycle: %v", err)
	}
	if len(out.EventsRemoved) != 1 {
		t.Errorf("EventsRemoved = %v, want 1", out.EventsRemoved)
	}
}
