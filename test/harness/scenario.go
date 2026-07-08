// Package harness — scenario-based end-to-end test harness for the
// found-footy Go rebuild. See docs/rebuild/proposals/test-corpus.md
// for the design.
//
// A scenario is a YAML file at test/scenarios/<suite>/<name>.yaml that
// declares: which workflow to exercise, what api-sports.io returns
// during the run, and what the final pg state must look like. The
// harness loads the YAML, sets up an isolated testcontainer Postgres +
// mock apifootball HTTP server + Temporal testsuite environment,
// executes the workflow against real activity code, then asserts the
// resulting pg state matches the scenario's expected block.
package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Scenario is the top-level YAML shape. Each *.yaml file under
// test/scenarios/ unmarshals into one of these.
type Scenario struct {
	// Name is human-readable; test output uses it. Optional — defaults
	// to the filename stem if empty.
	Name string `yaml:"name"`

	// Description is multi-line prose for humans reading the YAML.
	Description string `yaml:"description"`

	// Workflow selects which workflow the runner exercises. Currently
	// supported: "IngestWorkflow". "MonitorWorkflow" lands when that
	// code exists.
	Workflow string `yaml:"workflow"`

	// Setup pre-seeds pg state before the workflow runs. Optional —
	// scenarios starting from empty pg leave this out.
	Setup Setup `yaml:"setup"`

	// APIResponses configure what the mock apifootball server returns
	// for specific endpoints during the run. Currently a single blob
	// per endpoint since Ingest is one-shot; when Monitor scenarios
	// arrive we'll extend to per-cycle responses.
	APIResponses APIResponses `yaml:"api_responses"`

	// IngestInput populates the IngestWorkflow input when
	// Workflow == "IngestWorkflow". Times parse from YAML as RFC3339.
	IngestInput *IngestInput `yaml:"ingest_input,omitempty"`

	// ExpectedFinalState is what pg must look like after the workflow
	// completes. Tier 1 assertions only for now.
	ExpectedFinalState ExpectedFinalState `yaml:"expected_final_state"`
}

// Setup pre-seeds rows before the workflow runs. Any of these blocks
// may be omitted; empty means "no setup needed."
type Setup struct {
	Fixtures    []SetupFixture    `yaml:"fixtures"`
	TeamAliases []SetupTeamAlias  `yaml:"team_aliases"`
}

// SetupFixture is a fixture row to insert before the workflow runs.
// State determines which fields are required (staging: kickoff only;
// active: kickoff + activated_at; completed: all three).
type SetupFixture struct {
	ID              int64      `yaml:"id"`
	State           string     `yaml:"state"` // staging | active | completed
	Kickoff         time.Time  `yaml:"kickoff"`
	ActivatedAt     *time.Time `yaml:"activated_at,omitempty"`
	CompletedAt     *time.Time `yaml:"completed_at,omitempty"`
	LastPolledAt    *time.Time `yaml:"last_polled_at,omitempty"`
	APIStatusShort  string     `yaml:"api_status_short"`
	APIStatusLong   string     `yaml:"api_status_long"`
	HomeID          int        `yaml:"home_id"`
	HomeName        string     `yaml:"home_name"`
	AwayID          int        `yaml:"away_id"`
	AwayName        string     `yaml:"away_name"`
	LeagueID        int        `yaml:"league_id"`
	LeagueName      string     `yaml:"league_name"`
	LeagueSeason    int        `yaml:"league_season"`
}

// SetupTeamAlias pre-seeds a team_aliases row (rarely needed — Ingest
// creates placeholders itself).
type SetupTeamAlias struct {
	TeamID     int    `yaml:"team_id"`
	TeamName   string `yaml:"team_name"`
	IsNational bool   `yaml:"is_national"`
}

// APIResponses configures the mock apifootball server. The mock
// serves /status unconditionally with a canned "Pro plan, requests
// under limit" body; scenario-specific endpoints (fixtures window,
// fixtures-by-IDs) come from these fields.
type APIResponses struct {
	// FixturesWindow returns the array of APIFixture entries when
	// the workflow queries /fixtures?from=&to=. If nil, the mock
	// returns an empty array.
	FixturesWindow *FixturesResponse `yaml:"fixtures_window,omitempty"`

	// FixturesByIDs returns the array when the workflow queries
	// /fixtures?ids=... — the Monitor + manual-reingest paths.
	FixturesByIDs *FixturesResponse `yaml:"fixtures_by_ids,omitempty"`
}

// FixturesResponse mirrors the api-sports.io envelope + response
// array. Only the fields our production code reads are populated.
type FixturesResponse struct {
	Fixtures []APIFixture `yaml:"fixtures"`
}

// APIFixture is the mock-side shape of what api-sports.io returns.
// Field names lowercase-with-underscores per YAML convention; the
// mock server translates these into the JSON envelope
// production code expects.
type APIFixture struct {
	ID              int64     `yaml:"id"`
	Kickoff         time.Time `yaml:"kickoff"`
	StatusShort     string    `yaml:"status_short"`
	StatusLong      string    `yaml:"status_long"`
	StatusElapsed   *int      `yaml:"status_elapsed,omitempty"`
	StatusExtra     *int      `yaml:"status_extra,omitempty"`
	HomeID          int       `yaml:"home_id"`
	HomeName        string    `yaml:"home_name"`
	AwayID          int       `yaml:"away_id"`
	AwayName        string    `yaml:"away_name"`
	LeagueID        int       `yaml:"league_id"`
	LeagueName      string    `yaml:"league_name"`
	LeagueSeason    int       `yaml:"league_season"`
	GoalsHome       *int      `yaml:"goals_home,omitempty"`
	GoalsAway       *int      `yaml:"goals_away,omitempty"`
}

// IngestInput populates IngestWorkflowInput. All fields optional;
// defaults match plan §5 W1 for scheduled invocation.
type IngestInput struct {
	ManualDate       *time.Time    `yaml:"manual_date,omitempty"`
	ManualFixtureIDs []int64       `yaml:"manual_fixture_ids,omitempty"`
	ActivationWindow time.Duration `yaml:"activation_window,omitempty"`
	RetentionDays    int           `yaml:"retention_days,omitempty"`
}

// ExpectedFinalState is what the harness asserts after the workflow
// completes. Missing blocks aren't checked — only what's declared.
type ExpectedFinalState struct {
	// Fixtures asserts specific rows exist with the given state.
	// Fixture IDs not mentioned are NOT checked. Row counts are
	// checked separately via Counts below.
	Fixtures []ExpectedFixture `yaml:"fixtures,omitempty"`

	// TeamAliases asserts specific alias rows exist. Same
	// only-checked-if-declared semantic.
	TeamAliases []ExpectedTeamAlias `yaml:"team_aliases,omitempty"`

	// Counts asserts overall row counts in pg tables. Missing keys
	// aren't checked; declared keys must match exactly.
	Counts map[string]int `yaml:"counts,omitempty"`
}

// ExpectedFixture is a subset of Fixture fields the assertion
// engine checks. All fields optional; only declared fields are
// verified (unset fields not checked).
type ExpectedFixture struct {
	ID             int64   `yaml:"id"`
	State          string  `yaml:"state,omitempty"`
	APIStatusShort string  `yaml:"api_status_short,omitempty"`
	HasActivatedAt *bool   `yaml:"has_activated_at,omitempty"` // true = must be non-null, false = must be null
	HasCompletedAt *bool   `yaml:"has_completed_at,omitempty"`
	HasLastPolledAt *bool  `yaml:"has_last_polled_at,omitempty"`
}

// ExpectedTeamAlias verifies a team_aliases row.
type ExpectedTeamAlias struct {
	TeamID   int    `yaml:"team_id"`
	TeamName string `yaml:"team_name,omitempty"`
}

// LoadScenario reads and parses a YAML scenario file. Fills in
// defaults where sensible (Name from filename if empty).
func LoadScenario(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("harness.LoadScenario read %s: %w", path, err)
	}
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("harness.LoadScenario unmarshal %s: %w", path, err)
	}
	if s.Name == "" {
		base := filepath.Base(path)
		s.Name = base[:len(base)-len(filepath.Ext(base))]
	}
	if s.Workflow == "" {
		return nil, fmt.Errorf("harness.LoadScenario %s: `workflow` field required", path)
	}
	return &s, nil
}
