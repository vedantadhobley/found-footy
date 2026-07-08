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
