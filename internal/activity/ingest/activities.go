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
	"errors"
	"fmt"
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

	// FetchWindowFutureDays — how many days beyond today
	// FetchFixturesForWindow queries. Sourced from config at worker
	// startup.
	FetchWindowFutureDays int

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
}

// GetIngestConfig — trivial config accessor for the workflow.
func (a *Activities) GetIngestConfig(
	_ context.Context, _ GetIngestConfigInput,
) (GetIngestConfigOutput, error) {
	return GetIngestConfigOutput{MaxLookaheadDays: a.FetchWindowFutureDays}, nil
}

// ── RefreshTrackedTeamsIfStale ─────────────────────────────────

// RefreshTrackedTeamsIfStaleInput carries no fields — the activity
// reads TrackedLeagueIDs + TopFlightCacheHours off the Activities
// struct so the workflow doesn't need to plumb them through.
type RefreshTrackedTeamsIfStaleInput struct{}

// RefreshTrackedTeamsIfStaleOutput surfaces what happened for
// observability: whether a refresh actually fired, how many teams
// landed in the cache, per-league counts for debug.
type RefreshTrackedTeamsIfStaleOutput struct {
	Refreshed       bool
	TotalTeams      int
	PerLeagueCounts map[int]int // league_id → team count
	Errors          []string    // per-league fetch failures — non-fatal
}

// RefreshTrackedTeamsIfStale checks whether tracked_teams_cache is
// older than TopFlightCacheHours and, if so, re-populates it from the
// API. Cache-hit path exits early with Refreshed=false.
//
// Refresh path (per-league, sequential — small N):
//  1. GetCurrentSeason(leagueID) via /leagues?id=X
//  2. ListTeamsForLeague(leagueID, season) via /teams?league=X&season=Y
//  3. Append to accumulator
//
// After looping every league, call TeamRepo.Replace(teams, now) — a
// single transaction that truncates + copies the new set. Concurrent
// Ingest cycles never see a partial cache.
//
// Non-fatal per-league failures are aggregated into Errors so a bad
// league ID (or a single failed API call) doesn't nuke the whole
// refresh — we still populate the cache with the leagues that did
// return.
//
// Mirrors Python's `get_top_flight_team_ids` shape.
func (a *Activities) RefreshTrackedTeamsIfStale(
	ctx context.Context, _ RefreshTrackedTeamsIfStaleInput,
) (RefreshTrackedTeamsIfStaleOutput, error) {
	out := RefreshTrackedTeamsIfStaleOutput{PerLeagueCounts: map[int]int{}}

	// Cache-freshness check.
	oldest, hasCache, err := a.TeamRepo.OldestRefreshedAt(ctx)
	if err != nil {
		return out, fmt.Errorf("ingest.RefreshTrackedTeamsIfStale: check cache: %w", err)
	}
	now := a.now()
	if hasCache {
		age := now.Sub(oldest)
		if age < time.Duration(a.TopFlightCacheHours)*time.Hour {
			// Cache is fresh — skip.
			return out, nil
		}
	}

	// Refresh path — walk every tracked league.
	var accumulated []team.TrackedTeam
	seen := map[int64]struct{}{}
	for _, leagueID := range a.TrackedLeagueIDs {
		season, err := a.APIFootball.GetCurrentSeason(ctx, leagueID)
		if err != nil {
			out.Errors = append(out.Errors,
				fmt.Sprintf("GetCurrentSeason(league=%d): %v", leagueID, err))
			continue
		}
		apiTeams, err := a.APIFootball.ListTeamsForLeague(ctx, leagueID, season)
		if err != nil {
			out.Errors = append(out.Errors,
				fmt.Sprintf("ListTeamsForLeague(league=%d,season=%d): %v", leagueID, season, err))
			continue
		}
		// The vendor's league record contains league name — we don't
		// re-fetch it just for the denormalized display field. Passing
		// an empty leagueName is acceptable; refresh + observability
		// still work. Follow-up if we want richer logs.
		for _, t := range apiTeams {
			if _, dup := seen[t.ID]; dup {
				continue
			}
			seen[t.ID] = struct{}{}
			accumulated = append(accumulated, team.TrackedTeam{
				ID:          t.ID,
				Name:        t.Name,
				LeagueID:    leagueID,
				LeagueName:  "", // not fetched; not load-bearing
				Season:      season,
				RefreshedAt: now,
			})
			out.PerLeagueCounts[leagueID]++
		}
	}

	if len(accumulated) == 0 {
		// Every league failed. Don't nuke the existing cache — return
		// error so the workflow can decide.
		return out, fmt.Errorf(
			"ingest.RefreshTrackedTeamsIfStale: all %d leagues failed to refresh",
			len(a.TrackedLeagueIDs))
	}

	if err := a.TeamRepo.Replace(ctx, accumulated, now); err != nil {
		return out, fmt.Errorf("ingest.RefreshTrackedTeamsIfStale: replace cache: %w", err)
	}
	out.Refreshed = true
	out.TotalTeams = len(accumulated)
	return out, nil
}

// FetchFixturesOutput is a workflow-scoped aggregator type — the
// IngestWorkflow accumulates day-by-day fetch results (and the by-IDs
// path) into this shape before handing off to CategorizeAndUpsertFixtures.
// Not returned by any activity directly.
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
// Empty tracked-teams cache = fail-open (return everything). Matches
// the "if refresh failed, still ingest something" safety net; better
// than silent zero.
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

	// Fail-open: empty cache → no filter. Ingest still works, downstream
	// categorize sees more fixtures than expected but doesn't break.
	if trackedSet.Len() == 0 {
		return FetchFixturesForDayOutput{Fixtures: dayFixtures, Count: len(dayFixtures)}, nil
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
	fixtures, failedIDs, err := a.APIFootball.ListFixturesByIDs(ctx, in.IDs)
	if err != nil {
		return FetchFixturesByIDsOutput{FailedIDs: failedIDs}, fmt.Errorf("ingest.FetchFixturesByIDs: %w", err)
	}
	return FetchFixturesByIDsOutput{Fixtures: fixtures, Count: len(fixtures), FailedIDs: failedIDs}, nil
}

// ── CategorizeAndUpsertFixtures ────────────────────────────────

// CategorizeInput carries the API response array + the activation
// window (from workflow config, default 30 min). The activity
// translates + upserts each and returns counts.
type CategorizeInput struct {
	Fixtures         []apifootball.APIFixture
	ActivationWindow time.Duration
}

// CategorizeOutput counts landed rows by state + surfaces the unique
// team refs the alias step consumes. Errors carries per-fixture
// context strings for anything that failed inside the loop but
// didn't fail the activity — the workflow aggregates these into its
// top-level Errors []string so Temporal UI + Loki surface them
// without having to join per-fixture debug logs.
type CategorizeOutput struct {
	Staging   int
	Active    int
	Completed int
	Errors    []string
	TeamRefs  []TeamRef
}

// TeamRef is the input the alias-placeholder step needs to insert a
// row for a not-yet-cached team. Country/City best-effort; alias
// resolution activity refines later.
type TeamRef struct {
	TeamID     int
	TeamName   string
	IsNational bool
	Country    *string
	City       *string
}

// CategorizeAndUpsertFixtures translates each API fixture to a domain
// fixture, decides initial state, and Upserts. For existing rows,
// preserves domain-managed fields (activated_at, completed_at,
// last_polled_at) and only overwrites API-mutable ones — the daily
// ingest MUST NOT trample state a running fixture accumulated during
// its match day.
//
// Also collects unique team refs across all fixtures for the alias
// step. A team appearing as home in one fixture + away in another
// contributes once.
func (a *Activities) CategorizeAndUpsertFixtures(ctx context.Context, in CategorizeInput) (CategorizeOutput, error) {
	out := CategorizeOutput{}
	teams := make(map[int]TeamRef)
	now := a.now()

	for _, apiFix := range in.Fixtures {
		final, err := a.reconcileFixture(ctx, apiFix, in.ActivationWindow, now)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("reconcile fixture=%d: %v", apiFix.Fixture.ID, err))
			continue
		}
		if err := a.FixtureRepo.Upsert(ctx, final); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("upsert fixture=%d: %v", apiFix.Fixture.ID, err))
			continue
		}
		switch final.State {
		case fixture.StateStaging:
			out.Staging++
		case fixture.StateActive:
			out.Active++
		case fixture.StateCompleted:
			out.Completed++
		}
		// Record teams. IsNational + Country + City not in the fixture
		// response — alias resolution refines them later.
		teams[apiFix.Teams.Home.ID] = TeamRef{TeamID: apiFix.Teams.Home.ID, TeamName: apiFix.Teams.Home.Name}
		teams[apiFix.Teams.Away.ID] = TeamRef{TeamID: apiFix.Teams.Away.ID, TeamName: apiFix.Teams.Away.Name}
	}

	out.TeamRefs = make([]TeamRef, 0, len(teams))
	for _, ref := range teams {
		out.TeamRefs = append(out.TeamRefs, ref)
	}
	return out, nil
}

// reconcileFixture is the load-bearing merge step. Returns the
// Fixture that should be Upserted:
//   existing == nil    → fresh row constructed from API, initial
//                        state applied (staging / active / completed)
//   existing != nil    → API-mutable fields refreshed, domain fields
//                        (state timestamps, created_at) preserved
func (a *Activities) reconcileFixture(
	ctx context.Context,
	apiFix apifootball.APIFixture,
	activationWindow time.Duration,
	now time.Time,
) (*fixture.Fixture, error) {
	existing, err := a.FixtureRepo.Get(ctx, apiFix.Fixture.ID)
	if err != nil && !errors.Is(err, fixture.ErrNotFound) {
		return nil, fmt.Errorf("Get: %w", err)
	}

	if existing != nil {
		// Refresh API fields in place; leave state + timestamps alone.
		// LastPolledAt DOES get updated — ingest is a poll against
		// api-sports.io, and the monitor's future bucket-aware logic
		// benefits from knowing the fixture was seen fresh.
		existing.APIStatus = fixture.APIStatus{
			Short: apiFix.Fixture.Status.Short,
			Long:  apiFix.Fixture.Status.Long,
		}
		existing.APIElapsed = apiFix.Fixture.Status.Elapsed
		existing.APIExtra = apiFix.Fixture.Status.Extra
		existing.Kickoff = apiFix.Fixture.Date.UTC()
		existing.Home = fixture.Team{ID: apiFix.Teams.Home.ID, Name: apiFix.Teams.Home.Name}
		existing.Away = fixture.Team{ID: apiFix.Teams.Away.ID, Name: apiFix.Teams.Away.Name}
		existing.League = fixture.League{ID: apiFix.League.ID, Name: apiFix.League.Name, Season: apiFix.League.Season}
		existing.HomeScore = apiFix.Goals.Home
		existing.AwayScore = apiFix.Goals.Away
		existing.LastPolledAt = &now
		existing.UpdatedAt = now
		return existing, nil
	}

	// Fresh — construct + apply initial state.
	// fixture.New sets CreatedAt/UpdatedAt via its own time.Now() —
	// we don't override here because state transitions (Activate/
	// Complete) rewrite UpdatedAt anyway, and CreatedAt drift of a
	// few ns from the injected `now` is harmless (no test asserts
	// on it; it's a "when was this row born" audit field).
	f := fixture.New(
		apiFix.Fixture.ID,
		fixture.APIStatus{Short: apiFix.Fixture.Status.Short, Long: apiFix.Fixture.Status.Long},
		apiFix.Fixture.Date.UTC(),
		fixture.Team{ID: apiFix.Teams.Home.ID, Name: apiFix.Teams.Home.Name},
		fixture.Team{ID: apiFix.Teams.Away.ID, Name: apiFix.Teams.Away.Name}, fixture.League{ID: apiFix.League.ID, Name: apiFix.League.Name, Season: apiFix.League.Season},
	)
	f.APIElapsed = apiFix.Fixture.Status.Elapsed
	f.APIExtra = apiFix.Fixture.Status.Extra
	f.HomeScore = apiFix.Goals.Home
	f.AwayScore = apiFix.Goals.Away
	// LastPolledAt — ingest just polled api-sports.io; record that.
	// Set on the fresh Fixture BEFORE state transitions (Activate/
	// Complete don't touch LastPolledAt so this survives).
	f.LastPolledAt = &now

	// Apply initial state per the API's status:
	//   Terminal (FT/AET/etc.) → activate at kickoff, then complete now
	//   Live (1H/HT/2H/etc.)   → activate now (emergency: we missed
	//                             the pre-activation window)
	//   Not started            → staging, but ShouldActivateNow moves
	//                             to active if kickoff is imminent
	switch {
	case f.APIStatus.Terminal():
		if err := f.Activate(f.Kickoff); err != nil {
			return nil, fmt.Errorf("initial Activate for terminal: %w", err)
		}
		if err := f.Complete(now); err != nil {
			return nil, fmt.Errorf("initial Complete for terminal: %w", err)
		}
	case f.APIStatus.Live():
		if err := f.Activate(now); err != nil {
			return nil, fmt.Errorf("initial Activate for live: %w", err)
		}
	default:
		if f.ShouldActivateNow(now, activationWindow) {
			if err := f.Activate(now); err != nil {
				return nil, fmt.Errorf("initial Activate for imminent: %w", err)
			}
		}
	}
	return f, nil
}

// ── EnsureAliasPlaceholders ────────────────────────────────────

// EnsureAliasPlaceholdersInput carries the team refs collected during
// categorization.
type EnsureAliasPlaceholdersInput struct {
	Teams []TeamRef
}

// EnsureAliasPlaceholdersOutput counts existing (already-cached) vs
// newly-inserted (placeholder) rows. Errors carries per-team context
// strings for anything that failed inside the loop but didn't fail
// the activity — aggregated into the workflow's top-level Errors.
type EnsureAliasPlaceholdersOutput struct {
	Existing int
	Inserted int
	Errors   []string
}

// EnsureAliasPlaceholders BulkGets existing alias rows for each
// team ID; for teams without a cached row, inserts a placeholder
// (input fields only, resolved fields nil). The separate RAG
// resolution activity/workflow — design deferred — fills in
// Wikidata + Twitter aliases later.
//
// This is the decoupling that keeps IngestWorkflow independent of
// Wikidata + LLM availability. If joi is down or the daily quota
// exhausted, ingest still succeeds; only the resolution job pauses.
func (a *Activities) EnsureAliasPlaceholders(ctx context.Context, in EnsureAliasPlaceholdersInput) (EnsureAliasPlaceholdersOutput, error) {
	out := EnsureAliasPlaceholdersOutput{}
	if len(in.Teams) == 0 {
		return out, nil
	}

	ids := make([]int, 0, len(in.Teams))
	for _, t := range in.Teams {
		ids = append(ids, t.TeamID)
	}
	existing, err := a.AliasRepo.BulkGet(ctx, ids)
	if err != nil {
		return out, fmt.Errorf("ingest.EnsureAliasPlaceholders: BulkGet: %w", err)
	}

	now := a.now()
	for _, t := range in.Teams {
		if _, hasIt := existing[t.TeamID]; hasIt {
			out.Existing++
			continue
		}
		ta := alias.New(t.TeamID, t.TeamName, t.IsNational, t.Country, t.City, now)
		if err := a.AliasRepo.Upsert(ctx, ta); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("alias upsert team=%d: %v", t.TeamID, err))
			continue
		}
		out.Inserted++
	}
	return out, nil
}

// ── PruneOldFixtures ───────────────────────────────────────────

// PruneOldFixturesInput carries the retention cutoff.
type PruneOldFixturesInput struct {
	Threshold time.Time
}

// PruneOldFixturesOutput carries the deleted-row count.
type PruneOldFixturesOutput struct {
	Deleted int
}

// PruneOldFixtures deletes completed fixtures older than Threshold
// that have zero surviving video_shares (URL-stability invariant).
// Delegates entirely to FixtureRepo.PruneCompleted.
func (a *Activities) PruneOldFixtures(ctx context.Context, in PruneOldFixturesInput) (PruneOldFixturesOutput, error) {
	n, err := a.FixtureRepo.PruneCompleted(ctx, in.Threshold)
	if err != nil {
		return PruneOldFixturesOutput{}, fmt.Errorf("ingest.PruneOldFixtures: %w", err)
	}
	return PruneOldFixturesOutput{Deleted: n}, nil
}
