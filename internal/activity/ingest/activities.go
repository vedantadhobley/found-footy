// Package ingest holds the Temporal activities the IngestWorkflow
// orchestrates. Each activity is a method on the Activities struct so
// dependencies (adapter clients, domain repos) inject cleanly and
// tests substitute fakes.
//
// Activities are the crossing between orchestration and infra: they
// depend on adapter concrete types (or narrow interfaces) + domain
// repo interfaces, and translate API responses into domain values
// before persisting. Workflows depend only on activity input/output
// types — not on adapters or domain internals.
package ingest

import (
	"context"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/team"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// fixtureFetcher is the narrow interface the fetch activities need.
// Defined in the consumer package (per Go idiom); prod passes a
// *apifootball.Client, tests pass an in-memory fake. Isolates the
// activity from the full apifootball surface.
//
// ListFixturesByIDs returns (fixtures, failedIDs, err): fixtures are
// the IDs that came back, failedIDs are the IDs that didn't. err is
// only set on catastrophic failure — partial failures surface as
// non-empty failedIDs with err=nil. See apifootball.ListFixturesByIDs.
type fixtureFetcher interface {
	ListFixtures(ctx context.Context, params apifootball.FixtureListParams) ([]apifootball.APIFixture, error)
	ListFixturesByIDs(ctx context.Context, ids []int64) (
		fixtures []apifootball.APIFixture, failedIDs []int64, err error,
	)

	// GetCurrentSeason + ListTeamsForLeague back the tracked-team
	// refresh step. Kept on the same interface as the fixture calls
	// because the same *apifootball.Client satisfies both — no reason
	// to split.
	GetCurrentSeason(ctx context.Context, leagueID int) (int, error)
	ListTeamsForLeague(ctx context.Context, leagueID, season int) ([]apifootball.APITeam, error)
}

// Activities bundles the dependencies each ingest activity method
// needs. Constructed once at worker startup and registered whole via
// worker.RegisterActivity.
type Activities struct {
	APIFootball fixtureFetcher
	FixtureRepo fixture.Repo
	AliasRepo   alias.Repo
	TeamRepo    team.Repo

	// TrackedLeagueIDs — which league IDs Refresh iterates over
	// (typically the top-5 European club leagues + current major
	// tournament). Sourced from config at worker startup.
	TrackedLeagueIDs []int

	// TopFlightCacheHours — how stale tracked_teams_cache can get
	// before RefreshTrackedTeamsIfStale re-fetches. Sourced from
	// config at worker startup.
	TopFlightCacheHours int

	// FetchWindowFutureDays — how many days beyond today the by-date
	// FetchFixturesForDay lookahead scans. Sourced from config at worker
	// startup.
	FetchWindowFutureDays int

	// ActivationWindow — kickoff-lookahead for pre-activation at
	// Ingest categorize time. Sourced from config.Workflows at worker
	// startup. See internal/config/workflows.go.
	ActivationWindow time.Duration

	// CompletedFixtureDates is the public-history window shared with the API.
	// The retention planner reclaims only media older than this window.
	CompletedFixtureDates int

	// Now is injectable so tests can drive time deterministically.
	// Defaults to time.Now if unset.
	Now func() time.Time
}

func (a *Activities) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// ── GetIngestConfig ────────────────────────────────────────────

// GetIngestConfigInput has no fields.
type GetIngestConfigInput struct{}

// GetIngestConfigOutput exposes env-driven config to the workflow.
// Workflows can't touch env / files directly (Temporal determinism),
// so a trivial activity is the standard idiom for "workflow needs to
// know a config value."
type GetIngestConfigOutput struct {
	// MaxLookaheadDays bounds the smart-lookahead scan when tomorrow
	// is empty. Sourced from FetchWindowFutureDays config. Matches
	// Python's MAX_LOOKAHEAD_DAYS behavior.
	MaxLookaheadDays int

	// ActivationWindow — the kickoff-lookahead used at Ingest categorize
	// time to promote imminent fixtures straight to `active`. Sourced
	// from config.Workflows.ActivationWindow; MUST match what
	// ActivePollWorkflow uses for its ActivateUpcoming lookahead.
	ActivationWindow time.Duration

	// CompletedFixtureDates is the minimum number of completed UTC kickoff
	// dates kept public and in object storage.
	CompletedFixtureDates int
}

// GetIngestConfig — trivial config accessor for the workflow.
func (a *Activities) GetIngestConfig(
	_ context.Context, _ GetIngestConfigInput,
) (GetIngestConfigOutput, error) {
	return GetIngestConfigOutput{
		MaxLookaheadDays:      a.FetchWindowFutureDays,
		ActivationWindow:      a.ActivationWindow,
		CompletedFixtureDates: a.CompletedFixtureDates,
	}, nil
}
