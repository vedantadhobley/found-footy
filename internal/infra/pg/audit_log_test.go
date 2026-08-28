// Integration tests for atomic semantic state and event_log persistence.
package pg_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
)

func TestAuditedFixtureTransitionRollsBackWhenAuditInsertFails(t *testing.T) {
	ctx, pool, repo := setupRepo(t)
	f := makeStaging(7001, time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC))
	if err := repo.Upsert(ctx, f); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		ALTER TABLE event_log ADD CONSTRAINT test_reject_activation
		CHECK (event_type <> 'fixture.activated')
	`); err != nil {
		t.Fatalf("install audit failure constraint: %v", err)
	}
	activatedAt := time.Date(2026, 8, 28, 17, 55, 0, 0, time.UTC)
	if err := f.Activate(activatedAt); err != nil {
		t.Fatalf("activate domain fixture: %v", err)
	}
	record, err := auditlog.New(
		auditlog.KindFixtureActivated,
		uuid.Nil,
		f.ID,
		auditlog.FixtureActivatedPayload{FixtureID: f.ID, ActivatedAt: activatedAt, Reason: "test"},
	)
	if err != nil {
		t.Fatalf("build audit: %v", err)
	}
	if _, err := repo.UpsertWithAudit(ctx, f, record); err == nil {
		t.Fatal("UpsertWithAudit succeeded despite rejected audit insert")
	}

	stored, err := repo.Get(ctx, f.ID)
	if err != nil {
		t.Fatalf("read rolled-back fixture: %v", err)
	}
	if stored.State != fixture.StateStaging || stored.ActivatedAt != nil {
		t.Fatalf("fixture state survived failed audit: state=%s activated_at=%v", stored.State, stored.ActivatedAt)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_log WHERE fixture_id = $1`, f.ID).Scan(&auditCount); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 0 {
		t.Fatalf("audit rows = %d, want 0", auditCount)
	}
}

func TestAuditedEventTransitionsWriteExactlyOnce(t *testing.T) {
	ctx, pool, events, fixtures := setupEventRepo(t)
	seedFixture(t, ctx, fixtures, 7002)
	ev := makeGoalEvent(7002, 1)

	detected, err := auditlog.New(auditlog.KindEventDetected, ev.ID, ev.FixtureID, map[string]any{"phase": "detected"})
	if err != nil {
		t.Fatalf("build detected audit: %v", err)
	}
	if err := events.InsertWithAudit(ctx, ev, "poll-1", detected); err != nil {
		t.Fatalf("InsertWithAudit: %v", err)
	}

	stable, err := auditlog.New(auditlog.KindEventStable, ev.ID, ev.FixtureID, map[string]any{"phase": "stable"})
	if err != nil {
		t.Fatalf("build stable audit: %v", err)
	}
	if count, transitioned, err := events.RegisterEventPresenceWithAudit(ctx, ev.ID, "poll-2", stable); err != nil || count != 2 || transitioned {
		t.Fatalf("presence 2 = count %d transitioned %v err %v", count, transitioned, err)
	}
	if count, transitioned, err := events.RegisterEventPresenceWithAudit(ctx, ev.ID, "poll-3", stable); err != nil || count != 3 || !transitioned {
		t.Fatalf("presence 3 = count %d transitioned %v err %v", count, transitioned, err)
	}
	if _, transitioned, err := events.RegisterEventPresenceWithAudit(ctx, ev.ID, "poll-3", stable); err != nil || transitioned {
		t.Fatalf("presence retry = transitioned %v err %v", transitioned, err)
	}

	removed, err := auditlog.New(auditlog.KindEventRemoved, ev.ID, ev.FixtureID, map[string]any{"phase": "removed"})
	if err != nil {
		t.Fatalf("build removed audit: %v", err)
	}
	for i, workflowID := range []string{"drop-1", "drop-2", "drop-3"} {
		count, transitioned, err := events.RegisterEventAbsenceWithAudit(ctx, ev.ID, workflowID, removed)
		wantTransition := i == 2
		if err != nil || count != 2-i || transitioned != wantTransition {
			t.Fatalf("absence %d = count %d transitioned %v err %v", i+1, count, transitioned, err)
		}
	}
	if _, transitioned, err := events.RegisterEventAbsenceWithAudit(ctx, ev.ID, "drop-3", removed); err != nil || transitioned {
		t.Fatalf("absence retry = transitioned %v err %v", transitioned, err)
	}

	rows, err := pool.Query(ctx, `
		SELECT event_type, count(*)
		FROM event_log
		WHERE event_id = $1
		GROUP BY event_type
	`, ev.ID)
	if err != nil {
		t.Fatalf("list audit rows: %v", err)
	}
	defer rows.Close()
	got := make(map[string]int)
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			t.Fatalf("scan audit count: %v", err)
		}
		got[kind] = count
	}
	for _, kind := range []auditlog.Kind{auditlog.KindEventDetected, auditlog.KindEventStable, auditlog.KindEventRemoved} {
		if got[kind.String()] != 1 {
			t.Errorf("%s rows = %d, want 1", kind, got[kind.String()])
		}
	}
}
