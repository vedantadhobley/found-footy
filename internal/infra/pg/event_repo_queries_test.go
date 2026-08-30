// EventRepo fixture-scoped list integration tests.
package pg_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

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

func TestEventRepo_BatchReadsPreserveRequestedGroupingAndHideRemoved(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7010)
	seedFixture(t, ctx, fRepo, 7011)

	first := makeGoalEvent(7010, 1)
	second := makeGoalEvent(7011, 1)
	removed := makeGoalEvent(7010, 2)
	for i, e := range []*event.Event{first, second, removed} {
		if err := repo.Insert(ctx, e, "batch-cycle-"+uuid.NewString()); err != nil {
			t.Fatalf("Insert event %d: %v", i, err)
		}
	}
	if _, hitZero, err := repo.RegisterEventAbsence(ctx, removed.ID, "batch-remove"); err != nil || !hitZero {
		t.Fatalf("soft-remove event: hitZero=%v err=%v", hitZero, err)
	}

	byFixture, err := repo.ListByFixtures(ctx, []int64{7011, 7010})
	if err != nil {
		t.Fatalf("ListByFixtures: %v", err)
	}
	if len(byFixture) != 2 || byFixture[0].ID != second.ID || byFixture[1].ID != first.ID {
		t.Fatalf("batch fixture events = %+v, want requested fixture order without removed row", byFixture)
	}

	byID, err := repo.GetByIDs(ctx, []uuid.UUID{second.ID, removed.ID, uuid.New(), first.ID})
	if err != nil {
		t.Fatalf("GetByIDs: %v", err)
	}
	if len(byID) != 3 || byID[0].ID != second.ID || byID[1].ID != removed.ID || byID[2].ID != first.ID {
		t.Fatalf("batch events by ID = %+v, want known rows in caller order", byID)
	}
}

func TestEventRepo_ListAllByFixture_IncludesRemovedIdentityHistory(t *testing.T) {
	ctx, _, repo, fRepo := setupEventRepo(t)
	seedFixture(t, ctx, fRepo, 7009)

	removed := makeGoalEvent(7009, 1)
	active := makeGoalEvent(7009, 2)
	if err := repo.Insert(ctx, removed, "cycle-1-removed"); err != nil {
		t.Fatalf("Insert removed candidate: %v", err)
	}
	if err := repo.Insert(ctx, active, "cycle-1-active"); err != nil {
		t.Fatalf("Insert active: %v", err)
	}
	if _, hitZero, err := repo.RegisterEventAbsence(ctx, removed.ID, "cycle-2"); err != nil || !hitZero {
		t.Fatalf("soft-remove first event: hitZero=%v err=%v", hitZero, err)
	}

	visible, err := repo.ListByFixture(ctx, 7009)
	if err != nil {
		t.Fatalf("ListByFixture: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != active.ID {
		t.Fatalf("visible events = %+v, want only active row", visible)
	}
	history, err := repo.ListAllByFixture(ctx, 7009)
	if err != nil {
		t.Fatalf("ListAllByFixture: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("identity history length = %d, want 2", len(history))
	}
	byID := map[uuid.UUID]*event.Event{history[0].ID: history[0], history[1].ID: history[1]}
	if !byID[removed.ID].Removed || byID[active.ID].Removed {
		t.Fatalf("identity history removal flags = removed:%v active:%v",
			byID[removed.ID].Removed, byID[active.ID].Removed)
	}
}
