// runner.go — orchestrator that ties scenario → real workflow code
// → real pg → assertions. One RunScenario call per YAML file.
package harness

import (
	"context"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/vedantadhobley/found-footy/internal/activity/ingest"
	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	ffwf "github.com/vedantadhobley/found-footy/internal/workflow"
)

// harnessInstrumentBundle wraps the adapter Instruments the harness
// needs at construction time. Each scenario creates a fresh bundle so
// metrics don't accumulate across runs.
type harnessInstrumentBundle struct {
	af *apifootball.Instruments
}

func harnessInstruments() *harnessInstrumentBundle {
	reg := metrics.New()
	log := &logging.TestEmitter{}
	return &harnessInstrumentBundle{
		af: apifootball.RegisterMetrics(reg, log),
	}
}

// RunScenario is the entry point. Handles setup (truncate + seed),
// mock configuration, workflow execution, and final-state
// assertions. Reports failures via testing.T; caller does NOT need
// to wrap with require.Nil / etc.
func RunScenario(ctx context.Context, t *testing.T, pool *pg.Pool, mockAPI *MockAPI, s *Scenario) {
	t.Helper()
	// 1. Wipe pg.
	TruncateAll(ctx, t, pool)

	// 2. Configure mock apifootball responses.
	mockAPI.SetResponses(s.APIResponses)

	// 3. Seed setup rows.
	if err := applySetup(ctx, pool, s.Setup); err != nil {
		t.Fatalf("harness.RunScenario: applySetup: %v", err)
	}

	// 4. Build the real apifootball client pointed at the mock.
	fx := harnessInstruments()
	afClient, err := apifootball.NewClient(ctx, config.APIFootballConfig{
		BaseURL: mockAPI.URL(),
		APIKey:  "harness-key",
		Timeout: 5 * time.Second,
	}, fx.af)
	if err != nil {
		t.Fatalf("harness.RunScenario: NewClient: %v", err)
	}

	// 5. Dispatch to the workflow-specific runner.
	switch s.Workflow {
	case "IngestWorkflow":
		runIngest(ctx, t, pool, afClient, s)
	default:
		t.Fatalf("harness.RunScenario: unknown workflow %q", s.Workflow)
	}

	// 6. Assert final state.
	AssertFinalState(ctx, t, pool, s.ExpectedFinalState)
}

func runIngest(ctx context.Context, t *testing.T, pool *pg.Pool, afClient *apifootball.Client, s *Scenario) {
	t.Helper()
	// Inject the scenario's anchor into the activity clock so that
	// ShouldActivateNow's "is kickoff within window from now?" decision
	// uses scenario-time, not wall-clock time. Without this, a scenario
	// with kickoffs relative to 2026-07-07 would activate any fixture
	// whose kickoff is in the past relative to today's real clock.
	acts := &ingest.Activities{
		APIFootball: afClient,
		FixtureRepo: pg.NewFixtureRepo(pool),
		AliasRepo:   pg.NewAliasRepo(pool),
		Now: func() time.Time {
			if s.IngestInput != nil && s.IngestInput.ManualDate != nil {
				return s.IngestInput.ManualDate.UTC()
			}
			return time.Now().UTC()
		},
	}

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(ffwf.IngestWorkflow)
	env.RegisterActivity(acts)

	// Translate scenario input → workflow input.
	in := ffwf.IngestWorkflowInput{}
	if s.IngestInput != nil {
		if s.IngestInput.ManualDate != nil {
			d := s.IngestInput.ManualDate.UTC()
			in.ManualDate = &d
		}
		in.ManualFixtureIDs = s.IngestInput.ManualFixtureIDs
		in.ActivationWindow = s.IngestInput.ActivationWindow
		in.RetentionDays = s.IngestInput.RetentionDays
	}

	env.ExecuteWorkflow(ffwf.IngestWorkflow, in)
	if !env.IsWorkflowCompleted() {
		t.Fatalf("IngestWorkflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("IngestWorkflow error: %v", err)
	}
}

// applySetup inserts scenario-declared setup rows into pg.
func applySetup(ctx context.Context, pool *pg.Pool, setup Setup) error {
	for _, sf := range setup.Fixtures {
		_, err := pool.Exec(ctx, `
			INSERT INTO fixtures (
				id, state, api_status_short, api_status_long,
				kickoff, home_team_id, home_team_name, away_team_id, away_team_name,
				league_id, league_name, league_season,
				activated_at, completed_at, last_polled_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
				$13, $14, $15
			)
		`, sf.ID, sf.State, sf.APIStatusShort, sf.APIStatusLong,
			sf.Kickoff.UTC(), sf.HomeID, sf.HomeName, sf.AwayID, sf.AwayName,
			sf.LeagueID, sf.LeagueName, sf.LeagueSeason,
			sf.ActivatedAt, sf.CompletedAt, sf.LastPolledAt)
		if err != nil {
			return err
		}
	}
	for _, sa := range setup.TeamAliases {
		_, err := pool.Exec(ctx, `
			INSERT INTO team_aliases (team_id, team_name, is_national, wikidata_aliases, twitter_aliases)
			VALUES ($1, $2, $3, '{}', '{}')
		`, sa.TeamID, sa.TeamName, sa.IsNational)
		if err != nil {
			return err
		}
	}
	return nil
}
