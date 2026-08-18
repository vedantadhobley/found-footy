// EventRepo presence/absence debounce transaction integration tests.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// TestEventRepo_Presence_ClimbTo3TriggersDownstream requires distinct votes
// to advance 1→2→3 and only the threshold vote to report the trigger.
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

	c1, _, _ := repo.RegisterEventAbsence(ctx, e.ID, "c3")    // count=1
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
