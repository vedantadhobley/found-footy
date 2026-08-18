// EventRepo CRUD and mutable-state integration tests.
package pg_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

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

// UpdateMutableFields must refresh assist/minute/extra/detail onto an existing
// row (late-arriving assist, VAR minute correction, detail reclassification)
// WITHOUT changing identity (id, natural_key, scorer). #199.
func TestEventRepo_UpdateMutableFields(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7301)

	// Insert a goal with no assist, 30', no extra, "Normal Goal".
	e := makeGoalEvent(7301, 1)
	if err := repo.Insert(ctx, e, "wf-mut-1"); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Fresh observation of the SAME event: assist now populated, minute bumped
	// to 45+2 (VAR), detail reclassified to Penalty.
	fresh := makeGoalEvent(7301, 1)
	aid, aname, x := 777, "Late Assister", 2
	fresh.Assist = event.Player{ID: &aid, Name: &aname}
	fresh.Minute = 45
	fresh.Extra = &x
	fresh.Detail = "Penalty"

	if err := repo.UpdateMutableFields(ctx, e.ID, fresh); err != nil {
		t.Fatalf("UpdateMutableFields: %v", err)
	}

	got, err := repo.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Mutable fields refreshed.
	if got.Assist.ID == nil || *got.Assist.ID != 777 || got.Assist.Name == nil || *got.Assist.Name != "Late Assister" {
		t.Errorf("assist = %+v, want {777, Late Assister}", got.Assist)
	}
	if got.Minute != 45 || got.Extra == nil || *got.Extra != 2 {
		t.Errorf("minute/extra = %d/%v, want 45/2", got.Minute, got.Extra)
	}
	if got.Detail != "Penalty" {
		t.Errorf("detail = %q, want Penalty", got.Detail)
	}
	// Identity untouched.
	if got.ID != e.ID || got.NaturalKey != e.NaturalKey {
		t.Errorf("identity changed: %s/%s want %s/%s", got.ID, got.NaturalKey, e.ID, e.NaturalKey)
	}
	if got.Player.ID == nil || *got.Player.ID != 999 {
		t.Errorf("scorer changed: %+v, want player 999", got.Player)
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
