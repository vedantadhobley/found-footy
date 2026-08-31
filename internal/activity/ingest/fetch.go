// Fixture fetch activities for date windows and explicit ID batches.
package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/team"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// FetchFixturesOutput is the workflow-scoped aggregate passed from fetches to
// CategorizeAndUpsertFixtures. No activity returns it directly.
type FetchFixturesOutput struct {
	Fixtures []apifootball.APIFixture
	Count    int
}

// ── FetchFixturesForDay ────────────────────────────────────────

// FetchFixturesForDayInput carries the UTC date to query.
type FetchFixturesForDayInput struct {
	Date time.Time // must be a UTC calendar date; time component ignored
}

// FetchFixturesForDayOutput carries filtered results for the day.
type FetchFixturesForDayOutput struct {
	Fixtures    []apifootball.APIFixture
	Count       int
	FilteredOut int // dropped because neither team is tracked
	// TrackedCacheEmpty flags the fail-CLOSED path (#174): the
	// tracked-teams cache was empty, so this day fetched nothing rather
	// than flooding the world. The workflow logs an ERROR when set.
	TrackedCacheEmpty bool
}

// FetchFixturesForDay queries /fixtures?date=X for a single UTC day
// and filters results to fixtures where at least one team is in the
// tracked-teams cache. IngestWorkflow orchestrates the day-by-day
// scan (including smart lookahead) by calling this activity multiple
// times.
//
// Design — per-day activity granularity mirrors Python's
// `fetch_todays_fixtures(target_date_str)` shape. Each call is one
// HTTP request, one Temporal Activity, one retry unit. Workflow-side
// orchestration means retry policies + timeouts scope to the day,
// not to a whole window.
//
// Empty tracked-teams cache = fail CLOSED (#174): fetch nothing rather
// than return the whole world. See the guarded branch below.
func (a *Activities) FetchFixturesForDay(ctx context.Context, in FetchFixturesForDayInput) (FetchFixturesForDayOutput, error) {
	// Build tracked-teams filter set on each call. Small overhead
	// (~250 rows, PK-indexed SELECT) preferable to threading the set
	// through the workflow, which would balloon Temporal history.
	tracked, err := a.TeamRepo.List(ctx)
	if err != nil {
		return FetchFixturesForDayOutput{}, fmt.Errorf("ingest.FetchFixturesForDay: load tracked: %w", err)
	}
	trackedSet := team.NewSetFromTeams(tracked)

	// Normalize to midnight UTC so the API's date semantics are stable
	// regardless of how the workflow constructed the input.
	day := time.Date(in.Date.Year(), in.Date.Month(), in.Date.Day(), 0, 0, 0, 0, time.UTC)

	dayFixtures, err := a.APIFootball.ListFixtures(ctx, apifootball.FixtureListParams{Date: day})
	if err != nil {
		return FetchFixturesForDayOutput{}, fmt.Errorf(
			"ingest.FetchFixturesForDay: date=%s: %w", day.Format("2006-01-02"), err)
	}

	// Fail CLOSED (#174, audit Tier-2 #6): an empty cache means the
	// Step-0 refresh failed AND the cache was cold. Returning every
	// fixture the vendor has would flood Postgres and the canonical-team
	// cache with the whole world. Ingest nothing
	// this cycle and flag it (TrackedCacheEmpty → the workflow logs an
	// ERROR); the next refresh repopulates. A bounded static-team seed
	// like Python's 15-team list is the follow-up (#175).
	if trackedSet.Len() == 0 {
		return FetchFixturesForDayOutput{Count: 0, FilteredOut: len(dayFixtures), TrackedCacheEmpty: true}, nil
	}

	kept := make([]apifootball.APIFixture, 0, len(dayFixtures))
	filteredOut := 0
	for _, f := range dayFixtures {
		if trackedSet.Has(int64(f.Teams.Home.ID)) || trackedSet.Has(int64(f.Teams.Away.ID)) {
			kept = append(kept, f)
			continue
		}
		filteredOut++
	}
	return FetchFixturesForDayOutput{Fixtures: kept, Count: len(kept), FilteredOut: filteredOut}, nil
}

// FetchFixturesByIDsInput carries the ManualFixtureIDs list from the
// workflow's ad-hoc-reingest path. Any-size slice — the adapter
// chunks internally at apifootball.IDsBatchLimit and parallelizes.
type FetchFixturesByIDsInput struct {
	IDs []int64
}

// FetchFixturesByIDsOutput is FetchFixturesOutput plus a FailedIDs
// list so the workflow can loop with backoff on just the IDs that
// didn't come back — see IngestWorkflow's retry loop.
type FetchFixturesByIDsOutput struct {
	Fixtures  []apifootball.APIFixture
	Count     int
	FailedIDs []int64
}

// FetchFixturesByIDs calls apifootball with the given IDs. Zero-length
// input short-circuits. The adapter chunks + parallelizes internally;
// partial failures surface as non-empty FailedIDs (err still nil).
// The workflow decides whether to retry FailedIDs.
func (a *Activities) FetchFixturesByIDs(ctx context.Context, in FetchFixturesByIDsInput) (FetchFixturesByIDsOutput, error) {
	if len(in.IDs) == 0 {
		return FetchFixturesByIDsOutput{}, nil
	}
	result, err := a.APIFootball.ListFixturesByIDs(ctx, in.IDs)
	failedIDs := result.FailedIDs()
	if err != nil {
		return FetchFixturesByIDsOutput{FailedIDs: failedIDs}, fmt.Errorf("ingest.FetchFixturesByIDs: %w", err)
	}
	return FetchFixturesByIDsOutput{
		Fixtures: result.Fixtures, Count: len(result.Fixtures), FailedIDs: failedIDs,
	}, nil
}
