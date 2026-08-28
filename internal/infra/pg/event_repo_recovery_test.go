// EventRepo discovery-recovery and live-fleet projection integration tests.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// TestEventRepo_EventsAwaitingDiscovery selects triggered, live events without
// a completed discovery row for the recovery pass.
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
	if err := repo.Insert(ctx, f); err != nil {
		t.Fatalf("seedCompleted Insert: %v", err)
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
