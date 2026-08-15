// Tests for the EventRepo — testcontainer Postgres with the app
// schema loaded (same runTestPostgres helper as pool_test.go +
// fixture_repo_test.go).
//
// Fix 3a scope: basic CRUD only (Get, GetByNaturalKey, Insert,
// Upsert, ListPending). Debounce methods land in 3b tests.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// setupEventRepo mirrors setupRepo — spins up a fresh Postgres via
// runTestPostgres, returns the pool + EventRepo + FixtureRepo (events
// require a parent fixture row per the FK).
func setupEventRepo(t *testing.T) (context.Context, *pg.Pool, *pg.EventRepo, *pg.FixtureRepo) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	connStr := runTestPostgres(ctx, t)
	fx := newTestFixture()
	pool, err := pg.New(ctx, config.PGConfig{
		DSN:            connStr,
		MaxConns:       5,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	}, fx.ins)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool, pg.NewEventRepo(pool), pg.NewFixtureRepo(pool)
}

// seedFixture inserts a parent fixture row so events have a valid FK.
func seedFixture(t *testing.T, ctx context.Context, repo *pg.FixtureRepo, id int64) {
	t.Helper()
	f := makeStaging(id, time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC))
	// Events are typically detected during active play — activate the
	// fixture so it's in the state where events actually arrive.
	if err := f.Activate(time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed Activate: %v", err)
	}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}
}

// makeGoalEvent — helper for a standard goal event on a given fixture.
func makeGoalEvent(fixtureID int64, seq int) *event.Event {
	playerID := 999
	playerName := "Test Scorer"
	at := time.Date(2026, 7, 8, 15, 30, 0, 0, time.UTC)
	return event.New(
		fixtureID,
		event.Team{ID: 40, Name: "Liverpool"},
		event.Player{ID: &playerID, Name: &playerName},
		event.TypeGoal,
		"Normal Goal",
		30,
		nil, // no extra time
		seq,
		at,
	)
}

// Get ------------------------------------------------------------

func TestEventRepo_Get_NotFound(t *testing.T) {
	ctx, _, repo, _ := setupEventRepo(t)
	_, err := repo.Get(ctx, uuid.New())
	if !errors.Is(err, event.ErrNotFound) {
		t.Errorf("Get(new UUID) = %v, want event.ErrNotFound", err)
	}
}

// Insert then Get must roundtrip the assist player — populated when the vendor
// reports an assister, nil/nil otherwise. Assist rides alongside player_id/
// player_name (the search-by-assist backing); this covers the Insert $20/$21 +
// scanEvent column mapping.
func TestEventRepo_Insert_Assist(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7101)

	withAssist := makeGoalEvent(7101, 1)
	aid, aname := 555, "Assister"
	withAssist.Assist = event.Player{ID: &aid, Name: &aname}
	if err := repo.Insert(ctx, withAssist, "wf-assist-1"); err != nil {
		t.Fatalf("Insert (with assist): %v", err)
	}
	got, err := repo.Get(ctx, withAssist.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Assist.ID == nil || *got.Assist.ID != 555 || got.Assist.Name == nil || *got.Assist.Name != "Assister" {
		t.Errorf("Assist roundtrip = %+v, want {555, Assister}", got.Assist)
	}

	noAssist := makeGoalEvent(7101, 2)
	if err := repo.Insert(ctx, noAssist, "wf-assist-2"); err != nil {
		t.Fatalf("Insert (no assist): %v", err)
	}
	got2, err := repo.Get(ctx, noAssist.ID)
	if err != nil {
		t.Fatalf("Get (no assist): %v", err)
	}
	if got2.Assist.ID != nil || got2.Assist.Name != nil {
		t.Errorf("no-assist event should have nil Assist, got %+v", got2.Assist)
	}
}

func TestEventRepo_InsertThenGet(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7001)

	e := makeGoalEvent(7001, 1)
	if err := repo.Insert(ctx, e, "wf-test"); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Roundtrip verification of every scanEvent field mapping.
	if got.FixtureID != 7001 {
		t.Errorf("FixtureID = %d, want 7001", got.FixtureID)
	}
	if got.NaturalKey != e.NaturalKey {
		t.Errorf("NaturalKey = %q, want %q", got.NaturalKey, e.NaturalKey)
	}
	if got.Type != event.TypeGoal {
		t.Errorf("Type = %q, want Goal", got.Type)
	}
	if got.Detail != "Normal Goal" {
		t.Errorf("Detail = %q, want Normal Goal", got.Detail)
	}
	if got.Team.ID != 40 || got.Team.Name != "Liverpool" {
		t.Errorf("Team = %+v", got.Team)
	}
	if got.Player.ID == nil || *got.Player.ID != 999 {
		t.Errorf("Player.ID = %v, want 999", got.Player.ID)
	}
	if got.Player.Name == nil || *got.Player.Name != "Test Scorer" {
		t.Errorf("Player.Name = %v", got.Player.Name)
	}
	if got.Minute != 30 {
		t.Errorf("Minute = %d, want 30", got.Minute)
	}
	if got.Extra != nil {
		t.Errorf("Extra = %v, want nil", got.Extra)
	}
	if got.MonitorComplete || got.DownloadComplete || got.Removed {
		t.Errorf("state flags should be false on fresh insert: %+v", got)
	}
	// Insert seeds debounce_count to 1 (per user's symmetric-counter
	// spec — see decisions.md 2026-07-07). Never 0 for a live event.
	if got.DebounceCount != 1 {
		t.Errorf("DebounceCount = %d, want 1 (seeded by Insert)", got.DebounceCount)
	}
	if got.DownstreamTriggered {
		t.Error("DownstreamTriggered should be false on fresh insert")
	}
}

func TestEventRepo_Insert_DuplicateNaturalKeyFails(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7002)

	first := makeGoalEvent(7002, 1)
	if err := repo.Insert(ctx, first, "wf-first"); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	// Second event with same (fixture_id, natural_key) — should fail
	// with unique_violation. The concurrent-detection-race case.
	second := makeGoalEvent(7002, 1)
	err := repo.Insert(ctx, second, "wf-second")
	if err == nil {
		t.Fatal("expected unique_violation on duplicate natural_key, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("expected pgconn.PgError code 23505, got %v", err)
	}
	if !strings.Contains(err.Error(), "events") {
		t.Errorf("error missing 'events' context: %v", err)
	}
}

// GetByNaturalKey -----------------------------------------------

func TestEventRepo_GetByNaturalKey(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7003)

	e := makeGoalEvent(7003, 1)
	if err := repo.Insert(ctx, e, "wf-test"); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.GetByNaturalKey(ctx, 7003, e.NaturalKey)
	if err != nil {
		t.Fatalf("GetByNaturalKey: %v", err)
	}
	if got.ID != e.ID {
		t.Errorf("ID = %v, want %v", got.ID, e.ID)
	}
}

func TestEventRepo_GetByNaturalKey_NotFound(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7004)
	_, err := repo.GetByNaturalKey(ctx, 7004, "40_999_Goal_99")
	if !errors.Is(err, event.ErrNotFound) {
		t.Errorf("GetByNaturalKey miss = %v, want ErrNotFound", err)
	}
}

// Upsert (state changes) -----------------------------------------

func TestEventRepo_Upsert_UpdatesStateFields(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7005)

	e := makeGoalEvent(7005, 1)
	if err := repo.Insert(ctx, e, "wf-test"); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Simulate: monitor debounce passed → flip monitor_complete.
	e.MonitorComplete = true
	if err := repo.Upsert(ctx, e); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get after Upsert: %v", err)
	}
	if !got.MonitorComplete {
		t.Errorf("MonitorComplete = false, want true after Upsert")
	}
	if got.DownloadComplete || got.Removed {
		t.Errorf("other state flags shouldn't flip: %+v", got)
	}
}

func TestEventRepo_Upsert_SoftDeleteRoundtrip(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7006)

	e := makeGoalEvent(7006, 1)
	if err := repo.Insert(ctx, e, "wf-test"); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Mark VAR-removed.
	e.Removed = true
	reason := event.RemovalVAR
	e.RemovedReason = &reason
	removedAt := time.Date(2026, 7, 8, 15, 32, 0, 0, time.UTC)
	e.RemovedAt = &removedAt
	if err := repo.Upsert(ctx, e); err != nil {
		t.Fatalf("Upsert soft-delete: %v", err)
	}

	got, err := repo.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get after soft-delete: %v", err)
	}
	if !got.Removed || got.RemovedReason == nil || *got.RemovedReason != event.RemovalVAR {
		t.Errorf("removed state wrong: %+v", got)
	}
	if got.RemovedAt == nil || !got.RemovedAt.Equal(removedAt) {
		t.Errorf("RemovedAt = %v, want %v", got.RemovedAt, removedAt)
	}
}

// ListPending ----------------------------------------------------

func TestEventRepo_ListPending(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7007)

	// Three events: pending, monitor-complete-but-not-downloaded (still pending),
	// fully-complete, removed. Only the first two should surface.
	pending := makeGoalEvent(7007, 1)
	if err := repo.Insert(ctx, pending, "wf-pending"); err != nil {
		t.Fatalf("Insert pending: %v", err)
	}
	monitorOnly := makeGoalEvent(7007, 2)
	if err := repo.Insert(ctx, monitorOnly, "wf-monitorOnly"); err != nil {
		t.Fatalf("Insert monitorOnly: %v", err)
	}
	monitorOnly.MonitorComplete = true
	if err := repo.Upsert(ctx, monitorOnly); err != nil {
		t.Fatalf("Upsert monitorOnly: %v", err)
	}
	fullyDone := makeGoalEvent(7007, 3)
	if err := repo.Insert(ctx, fullyDone, "wf-fullyDone"); err != nil {
		t.Fatalf("Insert fullyDone: %v", err)
	}
	fullyDone.MonitorComplete = true
	fullyDone.DownloadComplete = true
	if err := repo.Upsert(ctx, fullyDone); err != nil {
		t.Fatalf("Upsert fullyDone: %v", err)
	}
	removed := makeGoalEvent(7007, 4)
	if err := repo.Insert(ctx, removed, "wf-removed"); err != nil {
		t.Fatalf("Insert removed: %v", err)
	}
	removed.Removed = true
	reason := event.RemovalVAR
	removed.RemovedReason = &reason
	now := time.Now().UTC()
	removed.RemovedAt = &now
	if err := repo.Upsert(ctx, removed); err != nil {
		t.Fatalf("Upsert removed: %v", err)
	}

	pendingList, err := repo.ListPending(ctx, 7007)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pendingList) != 2 {
		t.Fatalf("ListPending returned %d, want 2 (pending + monitorOnly): %+v", len(pendingList), pendingList)
	}
	// Ordered by first_seen_at — both used the same time in makeGoalEvent,
	// so verify the returned IDs cover exactly {pending, monitorOnly}.
	gotIDs := map[uuid.UUID]bool{pendingList[0].ID: true, pendingList[1].ID: true}
	if !gotIDs[pending.ID] || !gotIDs[monitorOnly.ID] {
		t.Errorf("ListPending returned wrong events: %+v", pendingList)
	}
}

func TestEventRepo_ListPending_Empty(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7008)
	pending, err := repo.ListPending(ctx, 7008)
	if err != nil {
		t.Fatalf("ListPending empty: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected empty pending, got %d events", len(pending))
	}
}

// ── Symmetric debounce ─────────────────────────────────────────

// TestEventRepo_Presence_ClimbTo3TriggersDownstream — three distinct
// workflows vote presence in sequence. Count climbs 1→2→3. Only the
// third call returns justTriggered=true.
func TestEventRepo_Presence_ClimbTo3TriggersDownstream(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8001)
	e := makeGoalEvent(8001, 1)
	if err := repo.Insert(ctx, e, "cycle-1"); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Insert seeded count=1. Cycle-2 presence bumps to 2 (no trigger).
	c, triggered, err := repo.RegisterEventPresence(ctx, e.ID, "cycle-2")
	if err != nil {
		t.Fatalf("Presence cycle-2: %v", err)
	}
	if c != 2 || triggered {
		t.Errorf("cycle-2 result = (%d, %v), want (2, false)", c, triggered)
	}
	c, triggered, err = repo.RegisterEventPresence(ctx, e.ID, "cycle-3")
	if err != nil {
		t.Fatalf("Presence cycle-3: %v", err)
	}
	if c != 3 || !triggered {
		t.Errorf("cycle-3 result = (%d, %v), want (3, true — first flip)", c, triggered)
	}
	// Verify downstream_triggered persisted.
	got, _ := repo.Get(ctx, e.ID)
	if !got.DownstreamTriggered {
		t.Error("DownstreamTriggered not persisted to TRUE")
	}
}

// TestEventRepo_Presence_NoRetrigger — after downstream_triggered=TRUE,
// subsequent presence votes cap at 3 and never return justTriggered
// again.
func TestEventRepo_Presence_NoRetrigger(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8002)
	e := makeGoalEvent(8002, 1)
	if err := repo.Insert(ctx, e, "cycle-1"); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	_, _, _ = repo.RegisterEventPresence(ctx, e.ID, "cycle-2")
	// cycle-3 triggers
	if _, tr, _ := repo.RegisterEventPresence(ctx, e.ID, "cycle-3"); !tr {
		t.Fatal("cycle-3 should have triggered")
	}
	// cycle-4 must NOT retrigger + count stays at 3
	c, tr, err := repo.RegisterEventPresence(ctx, e.ID, "cycle-4")
	if err != nil {
		t.Fatalf("cycle-4: %v", err)
	}
	if c != 3 {
		t.Errorf("cycle-4 count = %d, want 3 (capped)", c)
	}
	if tr {
		t.Error("cycle-4 must NOT retrigger — downstream already fired")
	}
}

// TestEventRepo_Presence_Idempotent — same workflow_id voting twice is
// a no-op. Count doesn't change on the second attempt.
func TestEventRepo_Presence_Idempotent(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8003)
	e := makeGoalEvent(8003, 1)
	if err := repo.Insert(ctx, e, "cycle-1"); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Attempt-1: cycle-2 presence → count=2
	c1, _, _ := repo.RegisterEventPresence(ctx, e.ID, "cycle-2")
	// Attempt-2: same workflow retries → idempotent, no change
	c2, tr, err := repo.RegisterEventPresence(ctx, e.ID, "cycle-2")
	if err != nil {
		t.Fatalf("second Presence: %v", err)
	}
	if c1 != c2 {
		t.Errorf("idempotency failed: c1=%d, c2=%d", c1, c2)
	}
	if tr {
		t.Error("idempotent retry must not report justTriggered")
	}
}

// TestEventRepo_Absence_HitsZeroSoftDeletes — three consecutive
// absences from freshly-Inserted event brings count 1→0 (well, 1
// absence would). Verify soft-delete atomic on hitZero.
func TestEventRepo_Absence_HitsZeroSoftDeletes(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8004)
	e := makeGoalEvent(8004, 1)
	if err := repo.Insert(ctx, e, "cycle-1"); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// count=1 after insert. One absence brings us to 0.
	c, hitZero, err := repo.RegisterEventAbsence(ctx, e.ID, "cycle-2")
	if err != nil {
		t.Fatalf("Absence: %v", err)
	}
	if c != 0 || !hitZero {
		t.Errorf("cycle-2 result = (%d, %v), want (0, true)", c, hitZero)
	}
	got, _ := repo.Get(ctx, e.ID)
	if !got.Removed {
		t.Error("event should be soft-deleted on hitZero")
	}
	if got.RemovedReason == nil || *got.RemovedReason != event.RemovalVAR {
		t.Errorf("removed_reason = %v, want 'var'", got.RemovedReason)
	}
	if got.RemovedAt == nil {
		t.Error("removed_at should be set on hitZero")
	}
}

// TestEventRepo_Absence_AfterTrigger — event at count=3 (post-trigger).
// Three consecutive absences bring it to 0. Verify:
//   - count decrements as expected
//   - downstream_triggered stays TRUE throughout
//   - only the LAST absence returns hitZero=true
func TestEventRepo_Absence_AfterTrigger(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8005)
	e := makeGoalEvent(8005, 1)
	_ = repo.Insert(ctx, e, "c1")
	_, _, _ = repo.RegisterEventPresence(ctx, e.ID, "c2")
	_, _, _ = repo.RegisterEventPresence(ctx, e.ID, "c3") // triggers, count=3

	c, hz, _ := repo.RegisterEventAbsence(ctx, e.ID, "c4")
	if c != 2 || hz {
		t.Errorf("c4 = (%d, %v), want (2, false)", c, hz)
	}
	c, hz, _ = repo.RegisterEventAbsence(ctx, e.ID, "c5")
	if c != 1 || hz {
		t.Errorf("c5 = (%d, %v), want (1, false)", c, hz)
	}
	c, hz, _ = repo.RegisterEventAbsence(ctx, e.ID, "c6")
	if c != 0 || !hz {
		t.Errorf("c6 = (%d, %v), want (0, true)", c, hz)
	}
	got, _ := repo.Get(ctx, e.ID)
	if !got.Removed || !got.DownstreamTriggered {
		t.Errorf("post-hitZero state wrong: removed=%v triggered=%v", got.Removed, got.DownstreamTriggered)
	}
}

// TestEventRepo_Flicker_NoConsecutiveHardReset — the whole point of
// the symmetric counter: presence between two absences does NOT reset
// the drop tally like Python. Count moves 1 step at a time.
func TestEventRepo_Flicker_NoConsecutiveHardReset(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8006)
	e := makeGoalEvent(8006, 1)
	_ = repo.Insert(ctx, e, "c1")
	_, _, _ = repo.RegisterEventPresence(ctx, e.ID, "c2") // count=2
	_, _, _ = repo.RegisterEventPresence(ctx, e.ID, "c3") // count=3, trigger

	// Flicker: absent, present, absent — Python would reset drop
	// counter on the present. Ours: 3→2→3→2, single step each way.
	c, _, _ := repo.RegisterEventAbsence(ctx, e.ID, "c4")
	if c != 2 {
		t.Errorf("c4 count = %d, want 2", c)
	}
	c, _, _ = repo.RegisterEventPresence(ctx, e.ID, "c5")
	if c != 3 {
		t.Errorf("c5 count = %d, want 3 (recovered)", c)
	}
	c, _, _ = repo.RegisterEventAbsence(ctx, e.ID, "c6")
	if c != 2 {
		t.Errorf("c6 count = %d, want 2", c)
	}
	// Not removed at this point.
	got, _ := repo.Get(ctx, e.ID)
	if got.Removed {
		t.Error("event should NOT be removed after flicker")
	}
}

// TestEventRepo_Absence_Idempotent — same workflow voting absence
// twice is a no-op.
func TestEventRepo_Absence_Idempotent(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8007)
	e := makeGoalEvent(8007, 1)
	_ = repo.Insert(ctx, e, "c1")
	_, _, _ = repo.RegisterEventPresence(ctx, e.ID, "c2") // count=2

	c1, _, _ := repo.RegisterEventAbsence(ctx, e.ID, "c3") // count=1
	c2, hz, err := repo.RegisterEventAbsence(ctx, e.ID, "c3") // idempotent
	if err != nil {
		t.Fatalf("absence idempotent retry: %v", err)
	}
	if c1 != c2 {
		t.Errorf("absence idempotency failed: c1=%d, c2=%d", c1, c2)
	}
	if hz {
		t.Error("idempotent retry must not report hitZero")
	}
}

// TestEventRepo_Absence_RemovedRowNoOp — once soft-deleted, subsequent
// absence votes don't re-trigger removal, don't error.
func TestEventRepo_Absence_RemovedRowNoOp(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8008)
	e := makeGoalEvent(8008, 1)
	_ = repo.Insert(ctx, e, "c1")
	// One absence brings count to 0 + soft-deletes.
	_, hz, _ := repo.RegisterEventAbsence(ctx, e.ID, "c2")
	if !hz {
		t.Fatal("c2 should hitZero")
	}
	// Another absence — different workflow, but event already removed.
	c, hz2, err := repo.RegisterEventAbsence(ctx, e.ID, "c3")
	if err != nil {
		t.Fatalf("post-remove absence: %v", err)
	}
	if c != 0 {
		t.Errorf("count = %d, want 0 (stays floor)", c)
	}
	if hz2 {
		t.Error("hitZero=true should fire only ONCE, not on subsequent absences")
	}
}

// insertAndTrigger inserts a goal event and climbs it to 3 presence votes
// so downstream_triggered flips. Helper for the downstream tests below.
func insertAndTrigger(t *testing.T, ctx context.Context, repo *pg.EventRepo, e *event.Event) {
	t.Helper()
	if err := repo.Insert(ctx, e, "c1"); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	_, _, _ = repo.RegisterEventPresence(ctx, e.ID, "c2")
	if _, tr, _ := repo.RegisterEventPresence(ctx, e.ID, "c3"); !tr {
		t.Fatalf("event %s should have triggered on 3rd presence", e.ID)
	}
}

// TestEventRepo_Absence_RemovalClosesDownstream — a VAR-removed event's
// still-pending downstream workflow row gets completed_at + outcome
// 'event_removed' in the same transaction, so fixture completion isn't
// blocked forever waiting on a discovery for an event that's gone.
// Fix for audit-2026-07-26 P1 #1.
func TestEventRepo_Absence_RemovalClosesDownstream(t *testing.T) {
	ctx, pool, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8010)
	e := makeGoalEvent(8010, 1)
	insertAndTrigger(t, ctx, repo, e)
	if err := repo.RegisterDownstreamWorkflow(ctx, e.ID, "discovery", "discovery-"+e.ID.String()); err != nil {
		t.Fatalf("RegisterDownstreamWorkflow: %v", err)
	}
	// Count is 3 after trigger; three absences drive it to zero → removed.
	for i, wf := range []string{"a1", "a2", "a3"} {
		_, hitZero, err := repo.RegisterEventAbsence(ctx, e.ID, wf)
		if err != nil {
			t.Fatalf("absence %s: %v", wf, err)
		}
		if (i == 2) != hitZero {
			t.Fatalf("absence %s: hitZero=%v, want %v", wf, hitZero, i == 2)
		}
	}
	var completedAt *time.Time
	var outcome *string
	if err := pool.QueryRow(ctx, `
		SELECT completed_at, outcome_class FROM event_downstream_workflows
		WHERE event_id = $1 AND workflow_type = 'discovery'`, e.ID).Scan(&completedAt, &outcome); err != nil {
		t.Fatalf("query downstream row: %v", err)
	}
	if completedAt == nil {
		t.Error("downstream completed_at still NULL after removal — fixture would hang forever")
	}
	if outcome == nil || *outcome != string(event.OutcomeEventRemoved) {
		t.Errorf("outcome_class = %v, want %q", outcome, event.OutcomeEventRemoved)
	}
}

// TestEventRepo_EventsAwaitingDiscovery — returns triggered, not-removed
// events whose discovery hasn't completed (spawn failed or in flight),
// and excludes completed / untriggered / removed. Drives the spawn-
// recovery pass for audit-2026-07-26 P1 #3.
func TestEventRepo_EventsAwaitingDiscovery(t *testing.T) {
	ctx, pool, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8011)

	// (a) triggered, no discovery row → awaiting (register never landed).
	ea := makeGoalEvent(8011, 1)
	insertAndTrigger(t, ctx, repo, ea)

	// (b) triggered, discovery completed → excluded.
	eb := makeGoalEvent(8011, 2)
	insertAndTrigger(t, ctx, repo, eb)
	_ = repo.RegisterDownstreamWorkflow(ctx, eb.ID, "discovery", "d-b")
	if _, err := pool.Exec(ctx, `UPDATE event_downstream_workflows
		SET completed_at = NOW(), outcome_class = 'success' WHERE event_id = $1`, eb.ID); err != nil {
		t.Fatalf("complete b: %v", err)
	}

	// (c) triggered, discovery pending (spawn failed or still running) → awaiting.
	ec := makeGoalEvent(8011, 3)
	insertAndTrigger(t, ctx, repo, ec)
	_ = repo.RegisterDownstreamWorkflow(ctx, ec.ID, "discovery", "d-c")

	// (d) not triggered → excluded.
	ed := makeGoalEvent(8011, 4)
	if err := repo.Insert(ctx, ed, "c1"); err != nil {
		t.Fatalf("insert d: %v", err)
	}

	// (e) triggered then removed → excluded.
	ee := makeGoalEvent(8011, 5)
	insertAndTrigger(t, ctx, repo, ee)
	for _, wf := range []string{"a1", "a2", "a3"} {
		_, _, _ = repo.RegisterEventAbsence(ctx, ee.ID, wf)
	}

	got, err := repo.EventsAwaitingDiscovery(ctx, 8011)
	if err != nil {
		t.Fatalf("EventsAwaitingDiscovery: %v", err)
	}
	ids := map[uuid.UUID]bool{}
	for _, e := range got {
		ids[e.ID] = true
	}
	if !ids[ea.ID] {
		t.Error("(a) triggered-no-row should be awaiting")
	}
	if !ids[ec.ID] {
		t.Error("(c) triggered-pending should be awaiting")
	}
	if ids[eb.ID] {
		t.Error("(b) triggered-completed must be excluded")
	}
	if ids[ed.ID] {
		t.Error("(d) untriggered must be excluded")
	}
	if ids[ee.ID] {
		t.Error("(e) removed must be excluded")
	}
	if len(got) != 2 {
		t.Errorf("awaiting count = %d, want 2 (a + c)", len(got))
	}
}

// seedCompletedFixture inserts a fixture that already reached FT — the state a
// LATE-match goal's fixture is in while its EventWorkflow is still searching.
func seedCompletedFixture(t *testing.T, ctx context.Context, repo *pg.FixtureRepo, id int64) {
	t.Helper()
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	f := makeStaging(id, kickoff)
	if err := f.Activate(kickoff); err != nil {
		t.Fatalf("seedCompleted Activate: %v", err)
	}
	if err := f.Complete(kickoff.Add(100 * time.Minute)); err != nil {
		t.Fatalf("seedCompleted Complete: %v", err)
	}
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("seedCompleted Upsert: %v", err)
	}
}

// TestEventRepo_ListLiveFleetEventIDs — the fleet reaper's KEEP set (audit
// P0-5). The load-bearing case is (b): a late goal whose fixture already
// flipped active→completed while its EventWorkflow is STILL searching — its
// Firefox instance must survive the reaper. A fixture-active-only filter would
// reap it mid-discovery and lose the goal's clips. Also covers the pre-trigger
// active window (live) and the three reapable shapes.
func TestEventRepo_ListLiveFleetEventIDs(t *testing.T) {
	ctx, pool, repo, fRepo := setupEventRepo(t)

	// ── active fixture 8020 ──
	seedFixture(t, ctx, fRepo, 8020)

	// (a) active fixture, no downstream row (pre-trigger debounce window) → live.
	ea := makeGoalEvent(8020, 1)
	if err := repo.Insert(ctx, ea, "c1"); err != nil {
		t.Fatalf("insert a: %v", err)
	}

	// (f) active fixture, event removed (VAR) → NOT live: Step 4.5 releases it,
	//     the reaper is the backstop.
	ef := makeGoalEvent(8020, 2)
	insertAndTrigger(t, ctx, repo, ef)
	for _, wf := range []string{"a1", "a2", "a3"} {
		_, _, _ = repo.RegisterEventAbsence(ctx, ef.ID, wf)
	}

	// ── completed fixture 8021 (match over — late-goal territory) ──
	seedCompletedFixture(t, ctx, fRepo, 8021)

	// (b) completed fixture, downstream still in flight → LIVE. THE fix: the
	//     OR-branch on completed_at IS NULL keeps this instance alive.
	eb := makeGoalEvent(8021, 1)
	insertAndTrigger(t, ctx, repo, eb)
	if err := repo.RegisterDownstreamWorkflow(ctx, eb.ID, "discovery", "d-b"); err != nil {
		t.Fatalf("register b: %v", err)
	}

	// (c) completed fixture, downstream completed → NOT live (reapable).
	ec := makeGoalEvent(8021, 2)
	insertAndTrigger(t, ctx, repo, ec)
	_ = repo.RegisterDownstreamWorkflow(ctx, ec.ID, "discovery", "d-c")
	if _, err := pool.Exec(ctx, `UPDATE event_downstream_workflows
		SET completed_at = NOW(), outcome_class = 'success' WHERE event_id = $1`, ec.ID); err != nil {
		t.Fatalf("complete c: %v", err)
	}

	// (d) completed fixture, no downstream row (crash-orphan: provisioned at
	//     count=1, worker died before spawn) → NOT live (reapable).
	ed := makeGoalEvent(8021, 3)
	if err := repo.Insert(ctx, ed, "c1"); err != nil {
		t.Fatalf("insert d: %v", err)
	}

	got, err := repo.ListLiveFleetEventIDs(ctx)
	if err != nil {
		t.Fatalf("ListLiveFleetEventIDs: %v", err)
	}
	live := map[uuid.UUID]bool{}
	for _, id := range got {
		live[id] = true
	}
	if !live[ea.ID] {
		t.Error("(a) active fixture, pre-trigger → should be live")
	}
	if !live[eb.ID] {
		t.Error("(b) completed fixture, downstream in flight → should be live (late-goal protection)")
	}
	if live[ec.ID] {
		t.Error("(c) completed fixture, downstream done → should NOT be live (reapable)")
	}
	if live[ed.ID] {
		t.Error("(d) completed fixture, no downstream row → should NOT be live (crash-orphan)")
	}
	if live[ef.ID] {
		t.Error("(f) removed event → should NOT be live")
	}
}

// TestEventRepo_DiscoveryComplete — the read-API phase signal: only events
// whose discovery workflow finished (completed_at set) are in the returned
// set; in-flight and no-row events are excluded. Also exercises the ::uuid[]
// array cast against real Postgres.
func TestEventRepo_DiscoveryComplete(t *testing.T) {
	ctx, pool, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 8030)

	// A: triggered, discovery completed → in the set.
	ea := makeGoalEvent(8030, 1)
	insertAndTrigger(t, ctx, repo, ea)
	_ = repo.RegisterDownstreamWorkflow(ctx, ea.ID, "discovery", "d-a")
	if _, err := pool.Exec(ctx, `UPDATE event_downstream_workflows
		SET completed_at = NOW(), outcome_class = 'assets_surfaced' WHERE event_id = $1`, ea.ID); err != nil {
		t.Fatalf("complete a: %v", err)
	}

	// B: triggered, discovery in flight (completed_at NULL) → NOT in the set.
	eb := makeGoalEvent(8030, 2)
	insertAndTrigger(t, ctx, repo, eb)
	_ = repo.RegisterDownstreamWorkflow(ctx, eb.ID, "discovery", "d-b")

	// C: no discovery row at all → NOT in the set.
	ec := makeGoalEvent(8030, 3)
	if err := repo.Insert(ctx, ec, "c1"); err != nil {
		t.Fatalf("insert c: %v", err)
	}

	got, err := repo.DiscoveryComplete(ctx, []uuid.UUID{ea.ID, eb.ID, ec.ID})
	if err != nil {
		t.Fatalf("DiscoveryComplete: %v", err)
	}
	if !got[ea.ID] {
		t.Error("A (completed discovery) should be in the set")
	}
	if got[eb.ID] {
		t.Error("B (in-flight discovery) should NOT be in the set")
	}
	if got[ec.ID] {
		t.Error("C (no discovery row) should NOT be in the set")
	}

	// Empty input → empty set, no error (guards the ANY([]) edge).
	empty, err := repo.DiscoveryComplete(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("DiscoveryComplete(nil) = %v, %v; want empty set, nil", empty, err)
	}
}
