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

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
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
	input      discoverycontract.EventWorkflowInput
}

func (s *recordingSpawner) SpawnEvent(_ context.Context, workflowID string, in discoverycontract.EventWorkflowInput) error {
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
	mu     sync.Mutex
	data   map[int64]*fixture.Fixture
	audits []auditlog.Record
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
func (r *fakeFixtureRepo) StoreFromIngest(ctx context.Context, f *fixture.Fixture) (fixture.State, error) {
	if err := r.Upsert(ctx, f); err != nil {
		return "", err
	}
	return f.State, nil
}
func (r *fakeFixtureRepo) RefreshActivePoll(ctx context.Context, f *fixture.Fixture) (bool, error) {
	r.mu.Lock()
	stored, ok := r.data[f.ID]
	r.mu.Unlock()
	if !ok || stored.State != fixture.StateActive {
		return false, nil
	}
	return true, r.Upsert(ctx, f)
}
func (r *fakeFixtureRepo) RefreshStagingPoll(ctx context.Context, f *fixture.Fixture) (bool, error) {
	r.mu.Lock()
	stored, ok := r.data[f.ID]
	r.mu.Unlock()
	if !ok || stored.State != fixture.StateStaging {
		return false, nil
	}
	return true, r.Upsert(ctx, f)
}
func (r *fakeFixtureRepo) TransitionWithAudit(ctx context.Context, f *fixture.Fixture, record auditlog.Record) (bool, error) {
	if !record.Valid() {
		return false, errors.New("invalid audit record")
	}
	r.mu.Lock()
	stored, ok := r.data[f.ID]
	r.mu.Unlock()
	if !ok {
		return false, fixture.ErrNotFound
	}
	if stored.State == f.State {
		return false, nil
	}
	if err := r.Upsert(ctx, f); err != nil {
		return false, err
	}
	r.mu.Lock()
	r.audits = append(r.audits, record)
	r.mu.Unlock()
	return true, nil
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

// AssessCompletion is the fixture-only portion of the production assessment.
// Event/downstream truth tables live in the pg integration tests.
func (r *fakeFixtureRepo) AssessCompletion(
	_ context.Context,
	id int64,
	terminalBefore time.Time,
) (fixture.CompletionAssessment, error) {
	r.mu.Lock()
	f, ok := r.data[id]
	r.mu.Unlock()
	if !ok {
		return fixture.CompletionAssessment{}, fixture.ErrNotFound
	}
	assessment := fixture.CompletionAssessment{}
	if f.APIStatus.Short == apifootball.StatusFullTime ||
		f.APIStatus.Short == apifootball.StatusAfterExtra ||
		f.APIStatus.Short == apifootball.StatusPenaltyDone {
		parity := true
		assessment.DurableScoreEventParity = &parity
	}
	assessment.Ready = f.State == fixture.StateActive && f.APIStatus.Terminal() &&
		f.TerminalObservedAt != nil && !f.TerminalObservedAt.After(terminalBefore)
	return assessment, nil
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
	audits   []auditlog.Record
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
func (r *fakeEventRepo) InsertWithAudit(ctx context.Context, e *event.Event, workflowID string, record auditlog.Record) error {
	if !record.Valid() {
		return errors.New("invalid audit record")
	}
	if err := r.Insert(ctx, e, workflowID); err != nil {
		return err
	}
	r.mu.Lock()
	r.audits = append(r.audits, record)
	r.mu.Unlock()
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
func (r *fakeEventRepo) RegisterEventPresenceWithAudit(ctx context.Context, eventID uuid.UUID, workflowID string, record auditlog.Record) (int, bool, error) {
	count, transitioned, err := r.RegisterEventPresence(ctx, eventID, workflowID)
	if err == nil && transitioned {
		r.mu.Lock()
		r.audits = append(r.audits, record)
		r.mu.Unlock()
	}
	return count, transitioned, err
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
func (r *fakeEventRepo) RegisterEventAbsenceWithAudit(ctx context.Context, eventID uuid.UUID, workflowID string, record auditlog.Record) (int, bool, error) {
	count, transitioned, err := r.RegisterEventAbsence(ctx, eventID, workflowID)
	if err == nil && transitioned {
		r.mu.Lock()
		r.audits = append(r.audits, record)
		r.mu.Unlock()
	}
	return count, transitioned, err
}
func (r *fakeEventRepo) RegisterDownstreamWorkflow(_ context.Context, _ uuid.UUID, _, _ string) error {
	// Spawner nil in these tests → this method is not
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
		APIFootball:         fetcher,
		FixtureRepo:         fRepo,
		EventRepo:           eRepo,
		TerminalGracePeriod: time.Hour,
		Now:                 func() time.Time { return now },
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
