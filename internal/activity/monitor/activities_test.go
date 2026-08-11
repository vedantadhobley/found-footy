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
	response  []apifootball.APIFixture
	failedIDs []int64 // simulate partial failure — set to non-nil to exercise the FailedIDs path
	err       error
	lastIDs   []int64
}

func (f *fakeFetcher) ListFixturesByIDs(_ context.Context, ids []int64) (
	[]apifootball.APIFixture, []int64, error,
) {
	f.lastIDs = ids
	return f.response, f.failedIDs, f.err
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

func (r *fakeFixtureRepo) ListReclaimableEventIDs(context.Context, time.Time) ([]uuid.UUID, error) {
	panic("fakeFixtureRepo.ListReclaimableEventIDs: not implemented (test scope drift)")
}

// FixtureReadyToComplete — in-memory version of the completion check.
// Mirrors the pg query semantics so completion tests see the same
// truth-table shape as production.
func (r *fakeFixtureRepo) FixtureReadyToComplete(_ context.Context, id int64) (bool, error) {
	r.mu.Lock()
	f, ok := r.data[id]
	r.mu.Unlock()
	if !ok {
		return false, fixture.ErrNotFound
	}
	if !f.APIStatus.Terminal() {
		return false, nil
	}
	if f.CompletionCounter < 3 && !f.HasDecidedWinner() {
		return false, nil
	}
	// Fake has no events map — the completion check for events
	// requires the fakeEventRepo. Callers that want event-level
	// coverage should wire a joint check via a scenario-level fake.
	// For unit tests here we treat "no events" as "all settled."
	return true, nil
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
	// Mirror the real repo: unknown-scorer events land as placeholders
	// (count 0, no seed vote); known scorers seed 1 + the first vote.
	initial := 1
	if !e.Player.Known() {
		initial = 0
	}
	e.DebounceCount = initial
	e.DownstreamTriggered = false
	dupE := *e
	r.events[e.ID] = &dupE
	r.byKey[fk] = e.ID
	if initial > 0 {
		r.presence[vkey(e.ID, workflowID)] = struct{}{}
	}
	return nil
}

// DeleteUnknownEvent hard-deletes a placeholder (debounce_count 0). Mirrors
// the real repo's guard: a no-op ErrNotFound for anything at count ≥1 so a
// confirmed event can never be removed here.
func (r *fakeEventRepo) DeleteUnknownEvent(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.events[id]
	if !ok || e.DebounceCount != 0 {
		return event.ErrNotFound
	}
	delete(r.byKey, fkey(e.FixtureID, e.NaturalKey))
	delete(r.events, id)
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
func (r *fakeEventRepo) EventsAwaitingDiscovery(_ context.Context, fixtureID int64) ([]*event.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*event.Event
	for _, e := range r.events {
		if e.FixtureID == fixtureID && e.DownstreamTriggered && !e.Removed {
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
func (r *fakeEventRepo) RegisterDownstreamWorkflow(_ context.Context, _ uuid.UUID, _, _ string) error {
	// Composer + Spawner nil in these tests → this method is not
	// reached; returning nil keeps interface satisfaction without
	// implementing test-side state that the tests don't inspect.
	return nil
}
func (r *fakeEventRepo) RegisterVideoValidationWorkflow(context.Context, uuid.UUID, string, string) (int, error) {
	panic("fakeEventRepo.RegisterVideoValidationWorkflow: not implemented (test scope drift)")
}

// ── helpers ────────────────────────────────────────────────────

func mkActiveFixture(id int64, kickoff time.Time) *fixture.Fixture {
	f := fixture.New(id,
		fixture.APIStatus{Short: "1h", Long: "First Half"},
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
		Type:   apifootball.EventTypeGoal,
		Detail: apifootball.DetailNormalGoal,
	}
}

// ── ActivateUpcoming ────────────────────────────────────────

func TestActivateUpcoming_ActivatesImminent(t *testing.T) {
	kickoff := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-10 * time.Minute)
	fRepo := newFakeFixtureRepo()
	staging := fixture.New(1,
		fixture.APIStatus{Short: "ns"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Season: 2026},
	)
	fRepo.Upsert(context.Background(), staging)

	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ActivateUpcoming(context.Background(), ActivateUpcomingInput{Lookahead: 30 * time.Minute})
	if err != nil {
		t.Fatalf("ActivateUpcoming: %v", err)
	}
	if out.Considered != 1 || out.Activated != 1 {
		t.Errorf("out = %+v, want Considered=1 Activated=1", out)
	}
	got, _ := fRepo.Get(context.Background(), 1)
	if got.State != fixture.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
}

func TestActivateUpcoming_SkipsFarFuture(t *testing.T) {
	kickoff := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-48 * time.Hour) // 2 days out
	fRepo := newFakeFixtureRepo()
	staging := fixture.New(1,
		fixture.APIStatus{Short: "ns"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Season: 2026},
	)
	fRepo.Upsert(context.Background(), staging)

	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, _ := acts.ActivateUpcoming(context.Background(), ActivateUpcomingInput{Lookahead: 30 * time.Minute})
	if out.Considered != 0 || out.Activated != 0 {
		t.Errorf("out = %+v, want zero (fixture too far out)", out)
	}
}

// ── PollStagingFixtures ────────────────────────────────────────

// stagingFixture returns a fresh staging fixture at the given kickoff.
// No last_polled_at (never polled).
func stagingFixture(id int64, kickoff time.Time) *fixture.Fixture {
	return fixture.New(id,
		fixture.APIStatus{Short: "ns", Long: "Not Started"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Name: "Premier League", Season: 2026},
	)
}

// mkAPIFixture builds a minimal APIFixture with just the fields
// PollStagingFixtures reads (ID, Status, Date).
func mkAPIFixture(id int64, statusShort apifootball.APIStatusCode, kickoff time.Time) apifootball.APIFixture {
	return apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID:   id,
			Date: kickoff,
			Status: apifootball.APIFixtureStatus{
				Short: statusShort,
				Long:  string(statusShort),
			},
		},
	}
}

func TestPollStagingFixtures_EmptyStaging_NoFetch(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	fetcher := &fakeFetcher{}
	acts := newActs(fetcher, fRepo, newFakeEventRepo(), now)

	out, err := acts.PollStagingFixtures(context.Background(),
		PollStagingFixturesInput{ActivationWindow: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PollStagingFixtures: %v", err)
	}
	if out.Considered != 0 || out.Polled != 0 {
		t.Errorf("out = %+v, want zero", out)
	}
	if fetcher.lastIDs != nil {
		t.Errorf("empty staging should NOT hit fetcher; got IDs=%v", fetcher.lastIDs)
	}
}

func TestPollStagingFixtures_LiveStatus_EmergencyActivates(t *testing.T) {
	// now = 12:23 UTC → bucket = 12*4 + 23/15 = 49.
	// staging fixture never polled (bucket ≠ 49 trivially) with kickoff
	// 2h away. API says match is 1H (in progress) — emergency activate.
	now := time.Date(2026, 7, 10, 12, 23, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	staging := stagingFixture(101, now.Add(2*time.Hour))
	_ = fRepo.Upsert(context.Background(), staging)

	fetcher := &fakeFetcher{
		response: []apifootball.APIFixture{
			mkAPIFixture(101, apifootball.StatusFirstHalf, staging.Kickoff),
		},
	}
	acts := newActs(fetcher, fRepo, newFakeEventRepo(), now)

	out, err := acts.PollStagingFixtures(context.Background(),
		PollStagingFixturesInput{ActivationWindow: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PollStagingFixtures: %v", err)
	}
	if out.EmergencyActivated != 1 {
		t.Errorf("EmergencyActivated = %d, want 1", out.EmergencyActivated)
	}
	if out.KickoffActivated != 0 {
		t.Errorf("KickoffActivated = %d, want 0", out.KickoffActivated)
	}
	if out.Polled != 1 {
		t.Errorf("Polled = %d, want 1", out.Polled)
	}

	got, _ := fRepo.Get(context.Background(), 101)
	if got.State != fixture.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
	if got.ActivatedAt == nil {
		t.Error("ActivatedAt should be set after emergency activation")
	}
}

func TestPollStagingFixtures_KickoffCorrected_Activates(t *testing.T) {
	// now = 13:00 UTC. Staging fixture stored with kickoff 3h out.
	// Vendor pushes corrected kickoff to 15 min from now (inside 30-min
	// activation window). Status is still NS. Kickoff activation should
	// fire.
	now := time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	staging := stagingFixture(202, now.Add(3*time.Hour))
	_ = fRepo.Upsert(context.Background(), staging)

	correctedKickoff := now.Add(15 * time.Minute)
	fetcher := &fakeFetcher{
		response: []apifootball.APIFixture{
			mkAPIFixture(202, apifootball.StatusNotStarted, correctedKickoff),
		},
	}
	acts := newActs(fetcher, fRepo, newFakeEventRepo(), now)

	out, err := acts.PollStagingFixtures(context.Background(),
		PollStagingFixturesInput{ActivationWindow: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PollStagingFixtures: %v", err)
	}
	if out.EmergencyActivated != 0 {
		t.Errorf("EmergencyActivated = %d, want 0", out.EmergencyActivated)
	}
	if out.KickoffActivated != 1 {
		t.Errorf("KickoffActivated = %d, want 1", out.KickoffActivated)
	}

	got, _ := fRepo.Get(context.Background(), 202)
	if got.State != fixture.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
	if !got.Kickoff.Equal(correctedKickoff) {
		t.Errorf("kickoff not updated: got %v, want %v", got.Kickoff, correctedKickoff)
	}
}

func TestPollStagingFixtures_NoStateChange_JustUpdatesFields(t *testing.T) {
	// Non-Live status, kickoff still far out. Should update APIStatus +
	// Kickoff + last_polled_at without transitioning to active.
	now := time.Date(2026, 7, 10, 14, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	staging := stagingFixture(303, now.Add(5*time.Hour))
	_ = fRepo.Upsert(context.Background(), staging)

	// Vendor confirms status still NS with same kickoff.
	fetcher := &fakeFetcher{
		response: []apifootball.APIFixture{
			mkAPIFixture(303, apifootball.StatusNotStarted, staging.Kickoff),
		},
	}
	acts := newActs(fetcher, fRepo, newFakeEventRepo(), now)

	out, err := acts.PollStagingFixtures(context.Background(),
		PollStagingFixturesInput{ActivationWindow: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PollStagingFixtures: %v", err)
	}
	if out.EmergencyActivated != 0 || out.KickoffActivated != 0 {
		t.Errorf("out = %+v, want no activations", out)
	}
	if out.Polled != 1 {
		t.Errorf("Polled = %d, want 1", out.Polled)
	}

	got, _ := fRepo.Get(context.Background(), 303)
	if got.State != fixture.StateStaging {
		t.Errorf("state = %q, want staging (no transition expected)", got.State)
	}
	if got.LastPolledAt == nil {
		t.Error("LastPolledAt should be set after poll")
	}
	// LastActivityAt should remain nil — RecordStagingPoll intentionally
	// doesn't touch it (matches Python's semantic).
	if got.LastActivityAt != nil {
		t.Errorf("LastActivityAt = %v, want nil (passive poll doesn't count as activity)", got.LastActivityAt)
	}
}

func TestPollStagingFixtures_PartialFailure_SurfacesMissed(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	staging1 := stagingFixture(401, now.Add(2*time.Hour))
	staging2 := stagingFixture(402, now.Add(2*time.Hour))
	_ = fRepo.Upsert(context.Background(), staging1)
	_ = fRepo.Upsert(context.Background(), staging2)

	// Fetcher returns 1 of 2; other is in failedIDs.
	fetcher := &fakeFetcher{
		response: []apifootball.APIFixture{
			mkAPIFixture(401, apifootball.StatusNotStarted, staging1.Kickoff),
		},
		failedIDs: []int64{402},
	}
	acts := newActs(fetcher, fRepo, newFakeEventRepo(), now)

	out, err := acts.PollStagingFixtures(context.Background(),
		PollStagingFixturesInput{ActivationWindow: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PollStagingFixtures: %v", err)
	}
	if out.MissedIDs != 1 {
		t.Errorf("MissedIDs = %d, want 1", out.MissedIDs)
	}
	if out.Polled != 1 {
		t.Errorf("Polled = %d, want 1 (only 401 was reconciled)", out.Polled)
	}
}

// ── ListActiveFixtureIDs ───────────────────────────────────────

func TestListActiveFixtureIDs(t *testing.T) {
	fRepo := newFakeFixtureRepo()
	fRepo.Upsert(context.Background(), mkActiveFixture(101, time.Now()))
	fRepo.Upsert(context.Background(), mkActiveFixture(102, time.Now()))
	staging := fixture.New(103,
		fixture.APIStatus{Short: "ns"},
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
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h", Long: "First Half"}},
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
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h"}},
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

// TestReconcileFixture_TerminalWithWinner_Completes exercises the
// full completion contract: fixture in active with FT status +
// winner data → completion check passes → state transitions to
// completed in the same reconcile pass. Fake repo's
// FixtureReadyToComplete mirrors the pg query's truth table for the
// no-events case; per-event completion checks land in the pg
// integration tests.
func TestReconcileFixture_TerminalWithWinner_Completes(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(105 * time.Minute) // post-FT
	fRepo := newFakeFixtureRepo()

	// Fixture in active state, ready for completion via winner fast-path.
	f := mkActiveFixture(999, kickoff)
	trueBool := true
	f.HomeWinner = &trueBool
	_ = fRepo.Upsert(context.Background(), f)

	// API response: FT status, no events.
	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID:     999,
			Status: apifootball.APIFixtureStatus{Short: "ft", Long: "Match Finished"},
		},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: apiFix, WorkflowID: "monitor-w1",
	})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if !out.Completed {
		t.Errorf("out.Completed = false, want true (winner fast-path)")
	}
	got, _ := fRepo.Get(context.Background(), 999)
	if got.State != fixture.StateCompleted {
		t.Errorf("state = %q, want completed", got.State)
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt should be set after completion")
	}
}

// TestReconcileFixture_TerminalCounterBelowThreshold_DoesNotComplete —
// FT status but only 1 Terminal poll observed. Counter is 1, no winner.
// Fixture should stay in active waiting for more Terminal polls.
func TestReconcileFixture_TerminalCounterBelowThreshold_DoesNotComplete(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(95 * time.Minute)
	fRepo := newFakeFixtureRepo()
	f := mkActiveFixture(888, kickoff)
	// No winner data set — must debounce via counter.
	_ = fRepo.Upsert(context.Background(), f)

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID:     888,
			Status: apifootball.APIFixtureStatus{Short: "ft"},
		},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: apiFix, WorkflowID: "monitor-w1",
	})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if out.Completed {
		t.Errorf("out.Completed = true, want false (counter = 1, no winner)")
	}
	got, _ := fRepo.Get(context.Background(), 888)
	if got.State != fixture.StateActive {
		t.Errorf("state = %q, want active (still debouncing)", got.State)
	}
	if got.CompletionCounter != 1 {
		t.Errorf("CompletionCounter = %d, want 1", got.CompletionCounter)
	}
}

func TestReconcileFixture_AbsenceHitZeroSoftDeletes(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(30 * time.Minute)
	fRepo := newFakeFixtureRepo()
	fRepo.Upsert(context.Background(), mkActiveFixture(999, kickoff))
	eRepo := newFakeEventRepo()
	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h"}},
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
