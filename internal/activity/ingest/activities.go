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

	"github.com/google/uuid"

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

	// GetTeamProfile is the per-team enrichment call ResolveAliasesForTeams
	// runs on cache-miss teams. Returns venue.city + authoritative
	// team.country + team.national + team.code — signals fixture data
	// alone doesn't carry. Ports Python's `get_team_info` shape.
	GetTeamProfile(ctx context.Context, teamID int64) (*apifootball.APITeamEnvelope, error)
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
	// startup. See internal/config/workflows.go for the 2× invariant
	// against StagingPollInterval.
	ActivationWindow time.Duration

	// RetentionDays — completed fixtures older than this get pruned
	// by PruneOldFixtures. Sourced from config.Workflows at worker
	// startup. Scheduled invocation uses this; manual triggers can
	// override via input.
	RetentionDays int

	// AliasResolver runs the alias lookup + selection pipeline for
	// ResolveAliasesForTeams. Nil = pipeline skipped (safe soft-fail
	// for worker configs that don't wire Wikidata).
	AliasResolver *alias.Resolver

	// AliasThrottle is the sleep between per-team alias resolutions
	// inside ResolveAliasesForTeams. Zero = no throttle (fine for
	// tests + tiny team lists). Prod defaults to ~500ms — Wikidata
	// rate-limits anonymous wbsearchentities at ~50 req / short window.
	AliasThrottle time.Duration

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
	// MonitorWorkflow uses for its PreActivateUpcoming lookahead.
	ActivationWindow time.Duration

	// RetentionDays — completed fixtures older than this get pruned.
	// Sourced from config.Workflows.RetentionDays. Zero here means the
	// scheduled invocation is expected to explicitly override.
	RetentionDays int
}

// GetIngestConfig — trivial config accessor for the workflow.
func (a *Activities) GetIngestConfig(
	_ context.Context, _ GetIngestConfigInput,
) (GetIngestConfigOutput, error) {
	return GetIngestConfigOutput{
		MaxLookaheadDays: a.FetchWindowFutureDays,
		ActivationWindow: a.ActivationWindow,
		RetentionDays:    a.RetentionDays,
	}, nil
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
	Refreshed        bool
	TotalTeams       int
	PerLeagueCounts  map[int]int // league_id → freshly-fetched team count
	PreservedLeagues map[int]int // league_id → prior rows carried forward (audit P1-1)
	Errors           []string    // per-league fetch failures — non-fatal
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
// Partial-failure safety (audit P1-1): a league that errors OR returns
// an empty roster (season rollover) is NOT dropped. Its prior cached
// rows are carried forward with their ORIGINAL refreshed_at, so the
// cache never loses a league we simply couldn't reach this run, and the
// stale timestamp makes the next run retry it. Only when EVERY league
// fails/empties do we abort without touching the cache. The final set
// (fresh survivors + preserved prior rows for configured leagues) goes
// to TeamRepo.Replace in one transaction, so a mid-refresh crash never
// leaves a partial cache visible.
//
// Mirrors Python's `get_top_flight_team_ids` shape.
func (a *Activities) RefreshTrackedTeamsIfStale(
	ctx context.Context, _ RefreshTrackedTeamsIfStaleInput,
) (RefreshTrackedTeamsIfStaleOutput, error) {
	out := RefreshTrackedTeamsIfStaleOutput{
		PerLeagueCounts:  map[int]int{},
		PreservedLeagues: map[int]int{},
	}

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

	// A league counts as "refreshed" only if it returned ≥1 team. A league
	// that errored OR came back empty (season rollover, before new-season
	// rosters are entered) contributes nothing and must NOT be allowed to
	// wipe its prior rows. audit P1-1.
	refreshed := make(map[int]bool, len(out.PerLeagueCounts))
	for lg, n := range out.PerLeagueCounts {
		if n > 0 {
			refreshed[lg] = true
		}
	}

	if len(refreshed) == 0 {
		// Nothing fresh at all — every league failed or was empty. Leave the
		// existing cache untouched (return error → the workflow keeps the
		// prior cache and the next run retries). The safe total-failure case.
		return out, fmt.Errorf(
			"ingest.RefreshTrackedTeamsIfStale: no league returned teams (%d configured)",
			len(a.TrackedLeagueIDs))
	}

	// Partial refresh: carry forward the prior rows of any CONFIGURED league
	// that did NOT refresh this run, with their ORIGINAL RefreshedAt. This is
	// the fix for the wipe: Replace rebuilds the whole cache, so anything not
	// in `accumulated` would be lost. Preserving keeps the league tracked and
	// its stale timestamp makes the next cycle retry it. Leagues no longer in
	// TrackedLeagueIDs are intentionally NOT preserved (they drop out).
	if len(refreshed) < len(a.TrackedLeagueIDs) {
		configured := make(map[int]bool, len(a.TrackedLeagueIDs))
		for _, lg := range a.TrackedLeagueIDs {
			configured[lg] = true
		}
		existing, err := a.TeamRepo.List(ctx)
		if err != nil {
			return out, fmt.Errorf("ingest.RefreshTrackedTeamsIfStale: load prior cache for preserve: %w", err)
		}
		for _, t := range existing {
			if configured[t.LeagueID] && !refreshed[t.LeagueID] {
				accumulated = append(accumulated, t) // keeps t.RefreshedAt
				out.PreservedLeagues[t.LeagueID]++
			}
		}
	}

	if err := a.TeamRepo.Replace(ctx, accumulated); err != nil {
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
	// fixture the vendor has would flood Postgres with the whole world
	// and hammer Wikidata for hundreds of unknown teams. Ingest nothing
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
		// Record teams. Country from league (proxy per Python's
		// `country = league.get("country")` in rag pre-cache) — for
		// league fixtures both teams are the league's country; for
		// international/cup fixtures the league country is a mixed
		// signal but still better than nil. Only overwrite if we
		// don't already have a non-empty value from a prior fixture
		// (a team seen in a domestic league keeps that country over
		// an international-cup one).
		countryPtr := (*string)(nil)
		if apiFix.League.Country != "" && apiFix.League.Country != "World" {
			c := apiFix.League.Country
			countryPtr = &c
		}
		// IsNational + City still not in the fixture response —
		// alias resolution can't refine those from here. IsNational
		// requires a separate /teams?id=X lookup; City requires
		// venue.city from the same endpoint. Both deferred until we
		// need them (rare enough that per-team API-Football calls
		// might be justified — but not right now).
		for _, tm := range []struct {
			id   int
			name string
		}{
			{apiFix.Teams.Home.ID, apiFix.Teams.Home.Name},
			{apiFix.Teams.Away.ID, apiFix.Teams.Away.Name},
		} {
			existing, seen := teams[tm.id]
			if !seen {
				teams[tm.id] = TeamRef{TeamID: tm.id, TeamName: tm.name, Country: countryPtr}
				continue
			}
			// Already seen — enrich country if we didn't have one
			// but do now.
			if existing.Country == nil && countryPtr != nil {
				existing.Country = countryPtr
				teams[tm.id] = existing
			}
		}
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
// (phase-1 vendor fields only, phase-2 resolution fields nil). The
// deterministic Wikidata resolution activity (task #134) fills in
// wikidata_qid + aliases + resolved_at asynchronously.
//
// This decoupling keeps IngestWorkflow independent of Wikidata
// availability. If Wikidata is down or rate-limiting, ingest still
// succeeds; only the resolution job pauses.
//
// TeamCode isn't in TeamRef yet — passed as nil for the placeholder
// (the resolution activity can fetch it fresh if needed). Add it to
// TeamRef when a future consumer needs it at Ingest time.
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
		ta := alias.New(t.TeamID, t.TeamName, t.IsNational, nil, t.Country, t.City, now)
		if err := a.AliasRepo.UpsertVendorFields(ctx, ta); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("alias upsert team=%d: %v", t.TeamID, err))
			continue
		}
		out.Inserted++
	}
	return out, nil
}

// ── ResolveAliasesForTeams ─────────────────────────────────────

// ResolveAliasesForTeamsInput carries the team refs to resolve. Same
// dedup discipline as EnsureAliasPlaceholders — one entry per team_id.
type ResolveAliasesForTeamsInput struct {
	Teams []TeamRef
}

// ResolveAliasesForTeamsOutput counts per-team outcomes for
// observability. Errors carries per-team failure strings (not fatal
// to the activity — soft-fail matches Python's rag_success /
// rag_failed pattern).
type ResolveAliasesForTeamsOutput struct {
	CacheHits int // team already had wikidata_qid — skipped
	Resolved  int // new resolution completed + persisted
	NoMatch   int // lookup ran but Wikidata returned no candidate
	Failed    int // transport / decode / other errors
	Errors    []string
}

// ResolveAliasesForTeams runs the full alias pipeline for each team
// that lacks a resolved wikidata_qid. Cache-hit teams (row exists AND
// wikidata_qid is set) are skipped — QIDs never expire, so once we've
// resolved a team we don't redo the expensive fuzzy lookup.
//
// For each cache-miss team we make two vendor calls in order:
//
//  1. api-football GET /teams?id=X (via GetTeamProfile) — returns
//     venue.city + authoritative team.country + team.national +
//     team.code. Ports Python's per-team `get_team_info` pattern.
//     Result is upserted back into team_aliases immediately so vendor
//     fields are captured even if Wikidata resolution fails.
//  2. Wikidata resolve + select — the deterministic alias pipeline.
//
// Sequential loop with a short delay between teams to keep Wikidata
// happy — matches Python's per-team workflow.execute_activity pattern
// (which was naturally sequential because Temporal activities in a
// workflow loop don't parallelize).
//
// Soft-fail per team: transport errors don't stop the activity; the
// bad team just doesn't get its aliases populated this cycle. It
// remains a placeholder row and will be retried on the next Ingest.
func (a *Activities) ResolveAliasesForTeams(ctx context.Context, in ResolveAliasesForTeamsInput) (ResolveAliasesForTeamsOutput, error) {
	out := ResolveAliasesForTeamsOutput{}
	if len(in.Teams) == 0 {
		return out, nil
	}
	if a.AliasResolver == nil {
		// If no resolver is wired (e.g., worker started without
		// Wikidata config), skip cleanly rather than fail the workflow.
		return out, nil
	}

	// Bulk-load existing rows so cache-hit teams don't roundtrip to pg.
	ids := make([]int, 0, len(in.Teams))
	for _, t := range in.Teams {
		ids = append(ids, t.TeamID)
	}
	existing, err := a.AliasRepo.BulkGet(ctx, ids)
	if err != nil {
		return out, fmt.Errorf("ingest.ResolveAliasesForTeams: BulkGet: %w", err)
	}

	now := a.now()
	for i, t := range in.Teams {
		// Cache hit: row exists AND wikidata_qid is set → skip fuzzy
		// lookup entirely. QID is permanent so we never redo this work
		// unless a future refresh cadence explicitly invalidates it.
		if row, ok := existing[t.TeamID]; ok && row != nil && row.WikidataQID != nil && *row.WikidataQID != "" {
			out.CacheHits++
			continue
		}

		// Enrich vendor fields via api-football /teams?id=X. This is
		// the source of venue.city + authoritative country/national/code
		// that fixture data alone can't give us. Ports Python's
		// `get_team_info` call inside rag pipeline.
		city := t.City
		country := t.Country
		isNational := t.IsNational
		var teamCode *string
		if profile, perr := a.APIFootball.GetTeamProfile(ctx, int64(t.TeamID)); perr == nil && profile != nil {
			if profile.Venue.City != nil && *profile.Venue.City != "" {
				city = profile.Venue.City
			}
			if profile.Team.Country != "" {
				c := profile.Team.Country
				country = &c
			}
			isNational = profile.Team.National
			if profile.Team.Code != nil && *profile.Team.Code != "" {
				teamCode = profile.Team.Code
			}
		} else if perr != nil {
			// Soft-fail: keep the TeamRef values and continue. Wikidata
			// resolution still gets a shot; scoring is just less strong
			// without city.
			out.Errors = append(out.Errors,
				fmt.Sprintf("GetTeamProfile team=%d: %v", t.TeamID, perr))
		}

		// Persist enriched vendor fields immediately (idempotent). If
		// Wikidata resolution fails below, the row still carries the
		// api-football enrichment for the next cycle's retry.
		enriched := alias.New(t.TeamID, t.TeamName, isNational, teamCode, country, city, now)
		if err := a.AliasRepo.UpsertVendorFields(ctx, enriched); err != nil {
			out.Failed++
			out.Errors = append(out.Errors, fmt.Sprintf("upsert vendor team=%d: %v", t.TeamID, err))
			continue
		}

		// Throttle between teams. Wikidata's wbsearchentities rate-
		// limits anonymous clients at roughly 50 requests / short
		// window. Each team = up to ~10 HTTP calls; 500ms between
		// teams keeps us well under the burst threshold even for
		// cold-start scenarios with many new teams.
		if i > 0 && a.AliasThrottle > 0 {
			time.Sleep(a.AliasThrottle)
		}

		lookupIn := alias.LookupInput{
			CanonicalName: t.TeamName,
			Country:       country,
			City:          city,
			IsNational:    isNational,
		}
		lookupOut, err := a.AliasResolver.Resolve(ctx, lookupIn)
		if err != nil {
			if errors.Is(err, alias.ErrNoMatch) {
				out.NoMatch++
			} else {
				out.Failed++
				out.Errors = append(out.Errors, fmt.Sprintf("resolve team=%d: %v", t.TeamID, err))
			}
			continue
		}

		aliases, err := a.AliasResolver.Select(ctx, alias.SelectInput{
			QID:           lookupOut.QID,
			IsNational:    isNational,
			CanonicalName: t.TeamName,
			VenueCity:     city,
		})
		if err != nil {
			out.Failed++
			out.Errors = append(out.Errors, fmt.Sprintf("select team=%d qid=%s: %v", t.TeamID, lookupOut.QID, err))
			continue
		}

		// Build the TeamAlias row: phase-1 vendor fields + phase-2
		// resolution. Reuse the enriched values from the profile fetch
		// above so this row is fully populated in one write.
		ta := alias.New(t.TeamID, t.TeamName, isNational, teamCode, country, city, now)
		ta.SetResolution(lookupOut.QID, aliases, now)
		if err := a.AliasRepo.UpsertResolution(ctx, ta); err != nil {
			out.Failed++
			out.Errors = append(out.Errors, fmt.Sprintf("upsert team=%d: %v", t.TeamID, err))
			continue
		}
		out.Resolved++
	}
	return out, nil
}

// ── PruneOldFixtures ───────────────────────────────────────────

// PruneOldFixturesInput carries the retention cutoff.
type PruneOldFixturesInput struct {
	Threshold time.Time
}

// PruneOldFixturesOutput carries both halves of the retention rule.
// Deleted is the number of clip-LESS completed fixtures hard-deleted
// (rows gone — safe, they never had a URL). ReclaimEventIDs are the
// events of clip-BEARING aged fixtures whose Garage bytes the workflow
// must reclaim via DestroyEvent (#176 option B: bytes reclaimed, rows
// kept as 410 tombstones for URL-stability).
type PruneOldFixturesOutput struct {
	Deleted         int
	ReclaimEventIDs []uuid.UUID
}

// PruneOldFixtures runs the SQL side of the two-part retention rule for
// completed fixtures older than in.Threshold:
//
//  1. Clip-BEARING fixtures — collect their events that still have live
//     shares (ListReclaimableEventIDs) so the workflow can DestroyEvent
//     each (revoke shares → 410, delete Garage bytes). Rows stay: the KB
//     tombstones preserve URL-stability (rebuild-plan §3 as revised
//     2026-08-11); the GB video bytes are the actual reclaimable cost.
//  2. Clip-LESS fixtures — hard-delete outright (PruneCompleted). They
//     never minted a share, so removing their rows 404s nothing.
//
// The two sets are disjoint (a completed fixture either has surviving
// shares or it doesn't), so order is immaterial. This activity does only
// the SQL; the byte reclaim is the workflow's DestroyEvent loop — that
// activity owns the S3 client, this one doesn't need it.
func (a *Activities) PruneOldFixtures(ctx context.Context, in PruneOldFixturesInput) (PruneOldFixturesOutput, error) {
	reclaim, err := a.FixtureRepo.ListReclaimableEventIDs(ctx, in.Threshold)
	if err != nil {
		return PruneOldFixturesOutput{}, fmt.Errorf("ingest.PruneOldFixtures: list reclaimable: %w", err)
	}
	n, err := a.FixtureRepo.PruneCompleted(ctx, in.Threshold)
	if err != nil {
		return PruneOldFixturesOutput{}, fmt.Errorf("ingest.PruneOldFixtures: prune clipless: %w", err)
	}
	return PruneOldFixturesOutput{Deleted: n, ReclaimEventIDs: reclaim}, nil
}
