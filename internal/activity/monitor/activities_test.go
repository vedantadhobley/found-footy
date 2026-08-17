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

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
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

type recordingSpawner struct {
	workflowID string
	input      discoveryactivity.EventWorkflowInput
}

func (s *recordingSpawner) SpawnEvent(_ context.Context, workflowID string, in discoveryactivity.EventWorkflowInput) error {
	s.workflowID = workflowID
	s.input = in
	return nil
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
	if f.CompletionCounter < 3 {
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
func (r *fakeEventRepo) UpdateMutableFields(_ context.Context, id uuid.UUID, fresh *event.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.events[id]
	if !ok {
		return event.ErrNotFound
	}
	e.Assist = fresh.Assist
	e.Minute = fresh.Minute
	e.Extra = fresh.Extra
	e.Detail = fresh.Detail
	return nil
}

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
func (r *fakeEventRepo) ListByFixture(_ context.Context, fixtureID int64) ([]*event.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*event.Event
	for _, e := range r.events {
		if e.FixtureID == fixtureID && !e.Removed {
			dup := *e
			out = append(out, &dup)
		}
	}
	return out, nil
}
func (r *fakeEventRepo) ListAllByFixture(_ context.Context, fixtureID int64) ([]*event.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*event.Event
	for _, e := range r.events {
		if e.FixtureID == fixtureID {
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

func TestRegisterAndSpawnEventCarriesFirstSeenTimestamp(t *testing.T) {
	firstSeen := time.Date(2026, 8, 17, 16, 30, 15, 0, time.UTC)
	playerID, playerName := 111, "M. Salah"
	existing := &event.Event{ID: uuid.New(), FirstSeenAt: firstSeen}
	domainEvent := &event.Event{
		Team:   event.Team{ID: 40, Name: "Liverpool"},
		Player: event.Player{ID: &playerID, Name: &playerName},
		Minute: 30,
	}
	spawner := &recordingSpawner{}
	activities := &Activities{EventRepo: newFakeEventRepo(), Spawner: spawner}

	if err := activities.registerAndSpawnEvent(context.Background(), existing, domainEvent, 12345); err != nil {
		t.Fatalf("registerAndSpawnEvent: %v", err)
	}
	if spawner.workflowID != "event-"+existing.ID.String() {
		t.Fatalf("workflow ID = %q", spawner.workflowID)
	}
	if !spawner.input.FirstSeenAt.Equal(firstSeen) {
		t.Fatalf("FirstSeenAt = %v, want %v", spawner.input.FirstSeenAt, firstSeen)
	}
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
	// A goal is a structural change (new event + a score move).
	if !out.Structural {
		t.Error("Structural = false, want true (a goal was inserted)")
	}
}

// ── N4 classification signals ──────────────────────────────────
//
// mkActiveN4Fixture seeds an active fixture with a known prior clock/score so
// the snapshot-diff has a concrete baseline (mkActiveFixture leaves elapsed +
// scores nil, which any poll would count as "changed").
func mkActiveN4Fixture(id int64, kickoff time.Time, elapsed, home, away int) *fixture.Fixture {
	f := mkActiveFixture(id, kickoff)
	e, h, a := elapsed, home, away
	f.APIElapsed = &e
	f.HomeScore, f.AwayScore = &h, &a
	return f
}

func pi(n int) *int { return &n }

// TestReconcileFixture_ClockAdvance_ClockOnly — the minute advances and nothing
// else: ClockChanged, NOT Structural. This is the fixture.clock tick case.
func TestReconcileFixture_ClockAdvance_ClockOnly(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(46 * time.Minute)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveN4Fixture(999, kickoff, 45, 0, 0))

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h", Long: "First Half", Elapsed: pi(46)}},
		Goals:   apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if !out.ClockChanged {
		t.Error("ClockChanged = false, want true (45→46)")
	}
	if out.Structural {
		t.Error("Structural = true, want false (only the clock moved)")
	}
	if out.Minute != 46 {
		t.Errorf("Minute = %d, want 46", out.Minute)
	}
}

// TestReconcileFixture_FrozenPoll_NeitherSignal — an identical re-poll (stalled
// minute, no changes): neither signal fires → no message that cycle.
func TestReconcileFixture_FrozenPoll_NeitherSignal(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(45 * time.Minute)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveN4Fixture(999, kickoff, 45, 0, 0))

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h", Long: "First Half", Elapsed: pi(45)}},
		Goals:   apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if out.ClockChanged || out.Structural {
		t.Errorf("ClockChanged=%v Structural=%v, want both false (nothing changed)", out.ClockChanged, out.Structural)
	}
}

// TestReconcileFixture_Halftime_StructuralNotClock — the status flips 1H→HT with
// the clock frozen: Structural (a full-refetch change), NOT ClockChanged. Proves
// a status change rides fixture.update even when the minute doesn't move.
func TestReconcileFixture_Halftime_StructuralNotClock(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(45 * time.Minute)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveN4Fixture(999, kickoff, 45, 0, 0))

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "ht", Long: "Halftime", Elapsed: pi(45)}},
		Goals:   apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if !out.Structural {
		t.Error("Structural = false, want true (status 1H→HT)")
	}
	if out.ClockChanged {
		t.Error("ClockChanged = true, want false (clock frozen at HT)")
	}
}

// TestReconcileFixture_ScoreChange_Structural — a score move with no event in the
// same poll (vendor eventual consistency) still classifies as Structural.
func TestReconcileFixture_ScoreChange_Structural(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(46 * time.Minute)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveN4Fixture(999, kickoff, 45, 0, 0))

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 999, Status: apifootball.APIFixtureStatus{Short: "1h", Long: "First Half", Elapsed: pi(46)}},
		Goals:   apifootball.APIFixtureGoals{Home: pi(1), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("ReconcileFixture: %v", err)
	}
	if !out.Structural {
		t.Error("Structural = false, want true (score 0→1)")
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

// TestReconcileFixture_TerminalWithWinnerRequiresCoherentDebounce proves that
// vendor winner data remains display/result data and cannot bypass three
// coherent terminal responses.
func TestReconcileFixture_TerminalWithWinnerRequiresCoherentDebounce(t *testing.T) {
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(105 * time.Minute) // post-FT
	fRepo := newFakeFixtureRepo()

	// Fixture in active state with winner data already present.
	f := mkActiveFixture(999, kickoff)
	trueBool := true
	f.HomeWinner = &trueBool
	_ = fRepo.Upsert(context.Background(), f)

	// API response: coherent 0-0 FT snapshot with no events.
	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID:     999,
			Status: apifootball.APIFixtureStatus{Short: "ft", Long: "Match Finished"},
		},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
	}
	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	for cycle := 1; cycle <= 3; cycle++ {
		out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
			APIFixture: apiFix, WorkflowID: fmt.Sprintf("monitor-w%d", cycle),
		})
		if err != nil {
			t.Fatalf("ReconcileFixture cycle %d: %v", cycle, err)
		}
		if cycle < 3 && out.Completed {
			t.Fatalf("cycle %d completed despite counter below 3", cycle)
		}
		if cycle == 3 && !out.Completed {
			t.Fatal("cycle 3 did not complete after three coherent terminal snapshots")
		}
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
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
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
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals:  apifootball.APIFixtureGoals{Home: pi(1), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}

	acts := newActs(&fakeFetcher{}, fRepo, eRepo, now)
	// Insert event
	_, _ = acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})

	// Now the event vanishes from the API — one absence brings count 1→0
	empty := apiFix
	empty.Events = nil
	empty.Goals.Home = pi(0) // aggregate score correction is the VAR evidence
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: empty, WorkflowID: "w2"})
	if err != nil {
		t.Fatalf("absence cycle: %v", err)
	}
	if len(out.EventsRemoved) != 1 {
		t.Errorf("EventsRemoved = %v, want 1", out.EventsRemoved)
	}
}

// TestReconcileFixture_GoalAbsenceHeldWhenScoreRequiresIt reproduces the
// Lazio-Mantova failure shape: the provider drops the event-array element but
// retains the aggregate score. The stored goal must receive no absence vote.
func TestReconcileFixture_GoalAbsenceHeldWhenScoreRequiresIt(t *testing.T) {
	kickoff := time.Date(2026, 8, 16, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564801, kickoff))
	eRepo := newFakeEventRepo()

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564801, Status: apifootball.APIFixtureStatus{Short: "2h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(0), Away: pi(1)},
		Events: []apifootball.APIFixtureEvent{
			mkAPIGoal(42, 222, 90),
		},
	}
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(96*time.Minute))
	_, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("insert cycle: %v", err)
	}

	omitted := apiFix
	omitted.Events = nil
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: omitted, WorkflowID: "w2"})
	if err != nil {
		t.Fatalf("omission cycle: %v", err)
	}
	if len(out.GoalAbsencesHeld) != 1 {
		t.Fatalf("GoalAbsencesHeld = %v, want one protected goal", out.GoalAbsencesHeld)
	}
	if len(out.EventsRemoved) != 0 {
		t.Fatalf("EventsRemoved = %v, want none", out.EventsRemoved)
	}

	stored, err := eRepo.GetByNaturalKey(context.Background(), 1564801, "42_222_goal_1")
	if err != nil {
		t.Fatalf("get protected event: %v", err)
	}
	if stored.DebounceCount != 1 || stored.Removed {
		t.Fatalf("protected event = count %d removed %v, want count 1 removed false", stored.DebounceCount, stored.Removed)
	}
}

// TestReconcileFixture_ReplacementGoalAllowsOldIdentityToDecay proves that a
// same-team replacement event accounts for the unchanged score. The old
// player-keyed identity may then follow the absence path instead of being held.
func TestReconcileFixture_ReplacementGoalAllowsOldIdentityToDecay(t *testing.T) {
	kickoff := time.Date(2026, 8, 16, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564802, kickoff))
	eRepo := newFakeEventRepo()

	apiFix := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564802, Status: apifootball.APIFixtureStatus{Short: "2h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(1), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{
			mkAPIGoal(40, 111, 30),
		},
	}
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(31*time.Minute))
	_, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: apiFix, WorkflowID: "w1"})
	if err != nil {
		t.Fatalf("original cycle: %v", err)
	}

	replacement := apiFix
	replacement.Events = []apifootball.APIFixtureEvent{mkAPIGoal(40, 222, 30)}
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: replacement, WorkflowID: "w2"})
	if err != nil {
		t.Fatalf("replacement cycle: %v", err)
	}
	if len(out.GoalAbsencesHeld) != 0 {
		t.Fatalf("GoalAbsencesHeld = %v, want none", out.GoalAbsencesHeld)
	}
	if len(out.EventsRemoved) != 1 || out.NewEventsDetected != 1 {
		t.Fatalf("removed=%v new=%d, want one old removal and one replacement", out.EventsRemoved, out.NewEventsDetected)
	}
}

// TestReconcileFixture_BraceKeepsLaterSequenceAfterFirstGoalVAR reproduces
// audit P1-2. Removing the earlier goal must not renumber the surviving later
// goal onto the tombstoned key, and a subsequent goal must allocate above the
// complete active + removed sequence history.
func TestReconcileFixture_BraceKeepsLaterSequenceAfterFirstGoalVAR(t *testing.T) {
	kickoff := time.Date(2026, 8, 17, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564901, kickoff))
	eRepo := newFakeEventRepo()
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(61*time.Minute))

	brace := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564901, Status: apifootball.APIFixtureStatus{Short: "2h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(2), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{
			mkAPIGoal(40, 111, 30),
			mkAPIGoal(40, 111, 60),
		},
	}
	if _, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: brace, WorkflowID: "brace-1",
	}); err != nil {
		t.Fatalf("brace insert: %v", err)
	}

	firstRemoved := brace
	firstRemoved.Goals.Home = pi(1)
	firstRemoved.Events = []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 60)}
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: firstRemoved, WorkflowID: "brace-2",
	})
	if err != nil {
		t.Fatalf("first-goal VAR: %v", err)
	}
	if len(out.EventsRemoved) != 1 || out.EventsRemoved[0] != "40_111_goal_1" {
		t.Fatalf("removed keys = %v, want first goal sequence", out.EventsRemoved)
	}
	survivor, err := eRepo.GetByNaturalKey(context.Background(), 1564901, "40_111_goal_2")
	if err != nil {
		t.Fatalf("get surviving second goal: %v", err)
	}
	if survivor.Removed || survivor.Minute != 60 || survivor.DebounceCount != 2 {
		t.Fatalf("survivor = removed %v minute %d count %d, want false/60/2",
			survivor.Removed, survivor.Minute, survivor.DebounceCount)
	}

	thirdGoal := firstRemoved
	thirdGoal.Goals.Home = pi(2)
	thirdGoal.Events = []apifootball.APIFixtureEvent{
		mkAPIGoal(40, 111, 60),
		mkAPIGoal(40, 111, 80),
	}
	out, err = acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: thirdGoal, WorkflowID: "brace-3",
	})
	if err != nil {
		t.Fatalf("third goal: %v", err)
	}
	if out.NewEventsDetected != 1 {
		t.Fatalf("new events = %d, want one third goal", out.NewEventsDetected)
	}
	if _, err := eRepo.GetByNaturalKey(context.Background(), 1564901, "40_111_goal_3"); err != nil {
		t.Fatalf("third goal did not allocate sequence 3: %v", err)
	}
}

// TestReconcileFixture_BraceArrayReorderDoesNotSwapRows ensures provider array
// order is not identity. The stored first and second goals retain their clocks
// when the same response arrives in reverse order.
func TestReconcileFixture_BraceArrayReorderDoesNotSwapRows(t *testing.T) {
	kickoff := time.Date(2026, 8, 17, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564902, kickoff))
	eRepo := newFakeEventRepo()
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(61*time.Minute))

	brace := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564902, Status: apifootball.APIFixtureStatus{Short: "2h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(2), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{
			mkAPIGoal(40, 111, 30),
			mkAPIGoal(40, 111, 60),
		},
	}
	if _, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: brace, WorkflowID: "reorder-1",
	}); err != nil {
		t.Fatalf("brace insert: %v", err)
	}

	brace.Events[0], brace.Events[1] = brace.Events[1], brace.Events[0]
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: brace, WorkflowID: "reorder-2",
	})
	if err != nil {
		t.Fatalf("reordered brace: %v", err)
	}
	if out.NewEventsDetected != 0 || len(out.EventsRemoved) != 0 {
		t.Fatalf("reordered result = new %d removed %v", out.NewEventsDetected, out.EventsRemoved)
	}
	first, err := eRepo.GetByNaturalKey(context.Background(), 1564902, "40_111_goal_1")
	if err != nil {
		t.Fatalf("get first stored goal: %v", err)
	}
	second, err := eRepo.GetByNaturalKey(context.Background(), 1564902, "40_111_goal_2")
	if err != nil {
		t.Fatalf("get second stored goal: %v", err)
	}
	if first.Minute != 30 || second.Minute != 60 {
		t.Fatalf("stored clocks swapped: seq1=%d seq2=%d", first.Minute, second.Minute)
	}
}

// TestReconcileFixture_IncompleteGoalInventoryDoesNotConsumeNearbyIdentity
// protects FF-014 and brace matching together. When score proves one goal is
// omitted, a nearby same-scorer goal must be inserted rather than treated as a
// mutable-clock correction of the stored missing goal.
func TestReconcileFixture_IncompleteGoalInventoryDoesNotConsumeNearbyIdentity(t *testing.T) {
	kickoff := time.Date(2026, 8, 17, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564903, kickoff))
	eRepo := newFakeEventRepo()
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(35*time.Minute))

	first := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564903, Status: apifootball.APIFixtureStatus{Short: "1h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals:  apifootball.APIFixtureGoals{Home: pi(1), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}
	if _, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: first, WorkflowID: "incomplete-1",
	}); err != nil {
		t.Fatalf("first goal: %v", err)
	}

	omittedWithNewGoal := first
	omittedWithNewGoal.Goals.Home = pi(2)
	omittedWithNewGoal.Events = []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 34)}
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: omittedWithNewGoal, WorkflowID: "incomplete-2",
	})
	if err != nil {
		t.Fatalf("incomplete inventory: %v", err)
	}
	if out.NewEventsDetected != 1 || len(out.GoalAbsencesHeld) != 1 {
		t.Fatalf("result = new %d held %v, want one new and one held",
			out.NewEventsDetected, out.GoalAbsencesHeld)
	}
	storedFirst, err := eRepo.GetByNaturalKey(context.Background(), 1564903, "40_111_goal_1")
	if err != nil {
		t.Fatalf("get omitted stored goal: %v", err)
	}
	storedSecond, err := eRepo.GetByNaturalKey(context.Background(), 1564903, "40_111_goal_2")
	if err != nil {
		t.Fatalf("nearby goal was not assigned a new identity: %v", err)
	}
	if storedFirst.Minute != 30 || storedSecond.Minute != 34 {
		t.Fatalf("stored clocks = seq1:%d seq2:%d, want 30/34", storedFirst.Minute, storedSecond.Minute)
	}
}

// TestReconcileFixture_ClockCorrectionKeepsNaturalKey proves FF-027 retains
// the reason sequence identity existed: a small provider clock correction
// updates mutable fields on the original row instead of inserting a duplicate.
func TestReconcileFixture_ClockCorrectionKeepsNaturalKey(t *testing.T) {
	kickoff := time.Date(2026, 8, 17, 19, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	_ = fRepo.Upsert(context.Background(), mkActiveFixture(1564904, kickoff))
	eRepo := newFakeEventRepo()
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, kickoff.Add(32*time.Minute))

	fixturePoll := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: 1564904, Status: apifootball.APIFixtureStatus{Short: "1h"}},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 40},
			Away: apifootball.APIFixtureTeam{ID: 42},
		},
		Goals:  apifootball.APIFixtureGoals{Home: pi(1), Away: pi(0)},
		Events: []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 30)},
	}
	if _, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: fixturePoll, WorkflowID: "correction-1",
	}); err != nil {
		t.Fatalf("first poll: %v", err)
	}

	fixturePoll.Events = []apifootball.APIFixtureEvent{mkAPIGoal(40, 111, 31)}
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
		APIFixture: fixturePoll, WorkflowID: "correction-2",
	})
	if err != nil {
		t.Fatalf("corrected poll: %v", err)
	}
	if out.NewEventsDetected != 0 || len(out.EventsRemoved) != 0 || !out.Structural {
		t.Fatalf("correction result = new %d removed %v structural %v",
			out.NewEventsDetected, out.EventsRemoved, out.Structural)
	}
	stored, err := eRepo.GetByNaturalKey(context.Background(), 1564904, "40_111_goal_1")
	if err != nil {
		t.Fatalf("corrected row: %v", err)
	}
	if stored.Minute != 31 {
		t.Fatalf("corrected minute = %d, want 31", stored.Minute)
	}
}
