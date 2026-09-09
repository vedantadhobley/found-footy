// event_repo_fleet_lease_test.go covers browser ownership across the debounce/checklist handoff.
package pg_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

// TestEventRepo_FleetLeaseBoundaries excludes finished discovery without losing warmup or late work.
func TestEventRepo_FleetLeaseBoundaries(t *testing.T) {
	ctx, pool, repo, fixtures := setupEventRepo(t)
	seedFixture(t, ctx, fixtures, 8070)
	seedCompletedFixture(t, ctx, fixtures, 8071)
	want := map[uuid.UUID]bool{}
	names := map[uuid.UUID]string{}
	for i, tc := range []struct {
		name          string
		completed     bool
		triggered     bool
		unknown       bool
		discoveryDone bool
		pending       bool
		otherDone     bool
		keep          bool
	}{
		{name: "warmup", keep: true},
		{name: "triggered before checklist registration", triggered: true, keep: true},
		{name: "active discovery", triggered: true, pending: true, keep: true},
		{name: "finished discovery on active fixture", triggered: true, discoveryDone: true},
		{name: "late discovery", completed: true, triggered: true, pending: true, keep: true},
		{name: "finished late discovery", completed: true, triggered: true, discoveryDone: true},
		{name: "completed fixture without work", completed: true},
		{name: "anonymous placeholder", unknown: true},
		{name: "replay alongside old completion", triggered: true, discoveryDone: true, pending: true, keep: true},
		{name: "other completed work does not close warmup", otherDone: true, keep: true},
	} {
		fixtureID := int64(8070)
		if tc.completed {
			fixtureID = 8071
		}
		ev := makeGoalEvent(fixtureID, i+1)
		if tc.unknown {
			ev.Player = event.Player{}
		}
		if tc.triggered {
			insertAndTrigger(t, ctx, repo, ev)
		} else if err := repo.Insert(ctx, ev, "detect"); err != nil {
			t.Fatal(err)
		}
		if tc.discoveryDone || tc.otherDone {
			kind := "discovery"
			if tc.otherDone {
				kind = "other"
			}
			if err := repo.RegisterDownstreamWorkflow(ctx, ev.ID, kind, fmt.Sprintf("closed-%d", i)); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE event_downstream_workflows SET completed_at=NOW(), outcome_class='success' WHERE event_id=$1`, ev.ID); err != nil {
				t.Fatal(err)
			}
		}
		if tc.pending {
			if err := repo.RegisterDownstreamWorkflow(ctx, ev.ID, "discovery", fmt.Sprintf("pending-%d", i)); err != nil {
				t.Fatal(err)
			}
		}
		want[ev.ID], names[ev.ID] = tc.keep, tc.name
	}
	ids, err := repo.ListLiveFleetEventIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uuid.UUID]bool{}
	for _, id := range ids {
		if _, ok := want[id]; !ok {
			t.Fatalf("unexpected owner %s", id)
		}
		got[id] = true
	}
	for id, keep := range want {
		if got[id] != keep {
			t.Errorf("%s: keep=%v, want %v", names[id], got[id], keep)
		}
	}
}
