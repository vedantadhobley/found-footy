// FixtureRepo CRUD and state-list integration tests.
package pg_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
)

func TestFixtureRepo_Get_NotFound(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	_, err := repo.Get(ctx, 999_999)
	if !errors.Is(err, fixture.ErrNotFound) {
		t.Errorf("Get non-existent returned %v, want fixture.ErrNotFound", err)
	}
}

func TestFixtureRepo_UpsertThenGet(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	original := makeStaging(1001, time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC))
	// Vendor display + shootout fields (round-2 DTO gaps): must roundtrip.
	original.League.Country = "World"
	original.League.Round = "Group Stage"
	ph, pa, wh := 5, 6, false
	original.HomePenalty, original.AwayPenalty = &ph, &pa
	original.HomeWinner = &wh

	if err := repo.Upsert(ctx, original); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := repo.Get(ctx, 1001)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != 1001 || got.State != fixture.StateStaging {
		t.Errorf("Get returned wrong fixture: %+v", got)
	}
	if got.APIStatus.Short != "NS" || got.APIStatus.Long != "Not Started" {
		t.Errorf("APIStatus roundtrip = %+v", got.APIStatus)
	}
	if got.Home.ID != 40 || got.Home.Name != "Liverpool" {
		t.Errorf("Home roundtrip = %+v", got.Home)
	}
	if got.League.Season != 2026 {
		t.Errorf("League.Season = %d, want 2026", got.League.Season)
	}
	if got.League.Country != "World" || got.League.Round != "Group Stage" {
		t.Errorf("League country/round roundtrip = %q/%q", got.League.Country, got.League.Round)
	}
	if got.HomePenalty == nil || *got.HomePenalty != 5 || got.AwayPenalty == nil || *got.AwayPenalty != 6 {
		t.Errorf("penalty roundtrip = %v/%v, want 5/6", got.HomePenalty, got.AwayPenalty)
	}
	if got.HomeWinner == nil || *got.HomeWinner != false {
		t.Errorf("HomeWinner roundtrip = %v, want false", got.HomeWinner)
	}
	if !got.Kickoff.Equal(original.Kickoff) {
		t.Errorf("Kickoff = %v, want %v", got.Kickoff, original.Kickoff)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not populated: CreatedAt=%v UpdatedAt=%v", got.CreatedAt, got.UpdatedAt)
	}
}

// Upsert on existing row updates fields but preserves created_at (via
// the ON CONFLICT DO UPDATE not listing created_at + updated_at,
// which the trg_fixtures_updated_at trigger maintains automatically).
func TestFixtureRepo_Upsert_UpdatesExisting_PreservesCreatedAt(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	f := makeStaging(2001, kickoff)
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	first, err := repo.Get(ctx, 2001)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	origCreated := first.CreatedAt

	// Transition to active, upsert again.
	if err := first.Activate(kickoff.Add(-15 * time.Minute)); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := repo.Upsert(ctx, first); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	after, err := repo.Get(ctx, 2001)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if after.State != fixture.StateActive {
		t.Errorf("State = %q, want active", after.State)
	}
	if after.ActivatedAt == nil {
		t.Fatal("ActivatedAt not persisted")
	}
	if !after.CreatedAt.Equal(origCreated) {
		t.Errorf("CreatedAt changed on Upsert-update: was %v, now %v", origCreated, after.CreatedAt)
	}
	if !after.UpdatedAt.After(origCreated) {
		t.Errorf("UpdatedAt should have advanced past CreatedAt: got %v vs %v", after.UpdatedAt, origCreated)
	}
}

// The domain's ValidateInvariants runs at Upsert time; a state ↔
// timestamp mismatch should be caught before the SQL executes.
func TestFixtureRepo_Upsert_RejectsInvariantViolation(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	f := makeStaging(3001, time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC))
	// Bypass domain methods — set State=active but leave ActivatedAt nil.
	f.State = fixture.StateActive
	err := repo.Upsert(ctx, f)
	if err == nil {
		t.Fatal("expected invariant-mismatch error, got nil")
	}
	if !errors.Is(err, fixture.ErrStateTimestampMismatch) {
		t.Errorf("Upsert returned %v, want ErrStateTimestampMismatch chain", err)
	}
}

// ListByState -----------------------------------------------------

func TestFixtureRepo_ListByState(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)

	staging := makeStaging(4001, kickoff)
	if err := repo.Upsert(ctx, staging); err != nil {
		t.Fatalf("staging Upsert: %v", err)
	}
	// Second fixture in active state.
	active := makeStaging(4002, kickoff)
	if err := active.Activate(kickoff.Add(-15 * time.Minute)); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := repo.Upsert(ctx, active); err != nil {
		t.Fatalf("active Upsert: %v", err)
	}

	stagingList, err := repo.ListByState(ctx, fixture.StateStaging)
	if err != nil {
		t.Fatalf("ListByState staging: %v", err)
	}
	if len(stagingList) != 1 || stagingList[0].ID != 4001 {
		t.Errorf("staging list = %+v, want single fixture 4001", stagingList)
	}
	activeList, err := repo.ListByState(ctx, fixture.StateActive)
	if err != nil {
		t.Fatalf("ListByState active: %v", err)
	}
	if len(activeList) != 1 || activeList[0].ID != 4002 {
		t.Errorf("active list = %+v, want single fixture 4002", activeList)
	}
	completedList, err := repo.ListByState(ctx, fixture.StateCompleted)
	if err != nil {
		t.Fatalf("ListByState completed: %v", err)
	}
	if len(completedList) != 0 {
		t.Errorf("completed list should be empty, got %+v", completedList)
	}
}

// ListActiveIDs --------------------------------------------------

func TestFixtureRepo_ListActiveIDs(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	kickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)

	// Seed a mix: staging, active x2, completed. Only the actives
	// should come back.
	staging := makeStaging(6001, kickoff)
	if err := repo.Upsert(ctx, staging); err != nil {
		t.Fatalf("staging Upsert: %v", err)
	}
	activeA := makeStaging(6002, kickoff)
	if err := activeA.Activate(kickoff.Add(-15 * time.Minute)); err != nil {
		t.Fatalf("Activate A: %v", err)
	}
	if err := repo.Upsert(ctx, activeA); err != nil {
		t.Fatalf("active A Upsert: %v", err)
	}
	activeB := makeStaging(6003, kickoff)
	if err := activeB.Activate(kickoff.Add(-15 * time.Minute)); err != nil {
		t.Fatalf("Activate B: %v", err)
	}
	if err := repo.Upsert(ctx, activeB); err != nil {
		t.Fatalf("active B Upsert: %v", err)
	}
	completed := makeStaging(6004, kickoff)
	if err := completed.Activate(kickoff); err != nil {
		t.Fatalf("Activate for completion: %v", err)
	}
	if err := completed.Complete(kickoff.Add(100 * time.Minute)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := repo.Upsert(ctx, completed); err != nil {
		t.Fatalf("completed Upsert: %v", err)
	}

	ids, err := repo.ListActiveIDs(ctx)
	if err != nil {
		t.Fatalf("ListActiveIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d IDs, want 2; ids=%v", len(ids), ids)
	}
	// Ordering: ORDER BY id, so [6002, 6003] deterministically.
	if ids[0] != 6002 || ids[1] != 6003 {
		t.Errorf("ids = %v, want [6002, 6003]", ids)
	}
}

func TestFixtureRepo_ListActiveIDs_Empty(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	ids, err := repo.ListActiveIDs(ctx)
	if err != nil {
		t.Fatalf("ListActiveIDs on empty: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("empty pg returned %v", ids)
	}
}

// ListStagingBeforeKickoff ---------------------------------------

func TestFixtureRepo_ListStagingBeforeKickoff(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)

	// Three staging fixtures at different kickoff times.
	near := makeStaging(5001, base.Add(10*time.Minute))   // 10 min from base
	within := makeStaging(5002, base.Add(25*time.Minute)) // 25 min from base
	far := makeStaging(5003, base.Add(2*time.Hour))       // 2 hours from base
	for _, f := range []*fixture.Fixture{near, within, far} {
		if err := repo.Upsert(ctx, f); err != nil {
			t.Fatalf("Upsert %d: %v", f.ID, err)
		}
	}

	threshold := base.Add(30 * time.Minute)
	got, err := repo.ListStagingBeforeKickoff(ctx, threshold)
	if err != nil {
		t.Fatalf("ListStagingBeforeKickoff: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("returned %d fixtures, want 2", len(got))
	}
	// Ordered by kickoff ascending.
	if got[0].ID != 5001 || got[1].ID != 5002 {
		t.Errorf("ordering wrong: got IDs %d, %d; want 5001, 5002", got[0].ID, got[1].ID)
	}
}
