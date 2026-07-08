// assertions.go — Tier 1 assertion engine: fixture rows, alias rows,
// row counts. Tier 2/3 (workflow spawns, video shares, log lines,
// metric counters, timing bounds) not implemented until scenarios
// need them.
package harness

import (
	"context"
	"fmt"
	"testing"

	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// AssertFinalState runs every assertion declared in the scenario's
// ExpectedFinalState against real pg state. Reports every mismatch
// via t.Errorf (not Fatal) so one scenario's failures show up
// together rather than short-circuiting.
func AssertFinalState(ctx context.Context, t *testing.T, pool *pg.Pool, expected ExpectedFinalState) {
	t.Helper()
	assertFixtures(ctx, t, pool, expected.Fixtures)
	assertTeamAliases(ctx, t, pool, expected.TeamAliases)
	assertEvents(ctx, t, pool, expected.Events)
	assertCounts(ctx, t, pool, expected.Counts)
}

func assertFixtures(ctx context.Context, t *testing.T, pool *pg.Pool, expected []ExpectedFixture) {
	t.Helper()
	for _, ef := range expected {
		var (
			gotState          string
			gotAPIStatusShort string
			hasActivated      bool
			hasCompleted      bool
			hasLastPolled     bool
		)
		err := pool.QueryRow(ctx, `
			SELECT state, api_status_short,
			       activated_at IS NOT NULL,
			       completed_at IS NOT NULL,
			       last_polled_at IS NOT NULL
			FROM fixtures WHERE id = $1
		`, ef.ID).Scan(&gotState, &gotAPIStatusShort, &hasActivated, &hasCompleted, &hasLastPolled)
		if err != nil {
			t.Errorf("fixture id=%d not found or unreadable: %v", ef.ID, err)
			continue
		}
		if ef.State != "" && ef.State != gotState {
			t.Errorf("fixture id=%d state = %q, want %q", ef.ID, gotState, ef.State)
		}
		if ef.APIStatusShort != "" && ef.APIStatusShort != gotAPIStatusShort {
			t.Errorf("fixture id=%d api_status_short = %q, want %q", ef.ID, gotAPIStatusShort, ef.APIStatusShort)
		}
		if ef.HasActivatedAt != nil && *ef.HasActivatedAt != hasActivated {
			t.Errorf("fixture id=%d activated_at populated = %v, want %v", ef.ID, hasActivated, *ef.HasActivatedAt)
		}
		if ef.HasCompletedAt != nil && *ef.HasCompletedAt != hasCompleted {
			t.Errorf("fixture id=%d completed_at populated = %v, want %v", ef.ID, hasCompleted, *ef.HasCompletedAt)
		}
		if ef.HasLastPolledAt != nil && *ef.HasLastPolledAt != hasLastPolled {
			t.Errorf("fixture id=%d last_polled_at populated = %v, want %v", ef.ID, hasLastPolled, *ef.HasLastPolledAt)
		}
	}
}

func assertTeamAliases(ctx context.Context, t *testing.T, pool *pg.Pool, expected []ExpectedTeamAlias) {
	t.Helper()
	for _, ea := range expected {
		var gotName string
		err := pool.QueryRow(ctx,
			"SELECT team_name FROM team_aliases WHERE team_id = $1", ea.TeamID).Scan(&gotName)
		if err != nil {
			t.Errorf("team_alias team_id=%d not found: %v", ea.TeamID, err)
			continue
		}
		if ea.TeamName != "" && ea.TeamName != gotName {
			t.Errorf("team_alias team_id=%d team_name = %q, want %q", ea.TeamID, gotName, ea.TeamName)
		}
	}
}

func assertEvents(ctx context.Context, t *testing.T, pool *pg.Pool, expected []ExpectedEvent) {
	t.Helper()
	for _, ee := range expected {
		var (
			gotDebounce           int
			gotDownstreamTriggered bool
			gotRemoved            bool
			gotRemovedReason      *string
			gotMonitorComplete    bool
		)
		err := pool.QueryRow(ctx, `
			SELECT debounce_count, downstream_triggered, removed,
			       removed_reason, monitor_complete
			FROM events
			WHERE fixture_id = $1 AND natural_key = $2
		`, ee.FixtureID, ee.NaturalKey).Scan(
			&gotDebounce, &gotDownstreamTriggered, &gotRemoved,
			&gotRemovedReason, &gotMonitorComplete,
		)
		if err != nil {
			t.Errorf("event fixture=%d natural_key=%q not found or unreadable: %v",
				ee.FixtureID, ee.NaturalKey, err)
			continue
		}
		if ee.DebounceCount != nil && *ee.DebounceCount != gotDebounce {
			t.Errorf("event fixture=%d nk=%q debounce_count = %d, want %d",
				ee.FixtureID, ee.NaturalKey, gotDebounce, *ee.DebounceCount)
		}
		if ee.DownstreamTriggered != nil && *ee.DownstreamTriggered != gotDownstreamTriggered {
			t.Errorf("event fixture=%d nk=%q downstream_triggered = %v, want %v",
				ee.FixtureID, ee.NaturalKey, gotDownstreamTriggered, *ee.DownstreamTriggered)
		}
		if ee.Removed != nil && *ee.Removed != gotRemoved {
			t.Errorf("event fixture=%d nk=%q removed = %v, want %v",
				ee.FixtureID, ee.NaturalKey, gotRemoved, *ee.Removed)
		}
		if ee.RemovedReason != "" {
			if gotRemovedReason == nil {
				t.Errorf("event fixture=%d nk=%q removed_reason = nil, want %q",
					ee.FixtureID, ee.NaturalKey, ee.RemovedReason)
			} else if *gotRemovedReason != ee.RemovedReason {
				t.Errorf("event fixture=%d nk=%q removed_reason = %q, want %q",
					ee.FixtureID, ee.NaturalKey, *gotRemovedReason, ee.RemovedReason)
			}
		}
		if ee.MonitorComplete != nil && *ee.MonitorComplete != gotMonitorComplete {
			t.Errorf("event fixture=%d nk=%q monitor_complete = %v, want %v",
				ee.FixtureID, ee.NaturalKey, gotMonitorComplete, *ee.MonitorComplete)
		}
	}
}

func assertCounts(ctx context.Context, t *testing.T, pool *pg.Pool, expected map[string]int) {
	t.Helper()
	for tbl, want := range expected {
		var got int
		if err := pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", tbl)).Scan(&got); err != nil {
			t.Errorf("count %s failed: %v", tbl, err)
			continue
		}
		if got != want {
			t.Errorf("count %s = %d, want %d", tbl, got, want)
		}
	}
}
