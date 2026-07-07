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
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// fixtureFetcher is the narrow interface the fetch activities need.
// Defined in the consumer package (per Go idiom); prod passes a
// *apifootball.Client, tests pass an in-memory fake. Isolates the
// activity from the full apifootball surface.
type fixtureFetcher interface {
	ListFixtures(ctx context.Context, params apifootball.FixtureListParams) ([]apifootball.APIFixture, error)
	ListFixturesByIDs(ctx context.Context, ids []int64) ([]apifootball.APIFixture, error)
}

// Activities bundles the dependencies each ingest activity method
// needs. Constructed once at worker startup and registered whole via
// worker.RegisterActivity.
type Activities struct {
	APIFootball fixtureFetcher
	FixtureRepo fixture.Repo
	AliasRepo   alias.Repo

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

// ── FetchFixturesForWindow ─────────────────────────────────────

// FetchFixturesInput narrows what the workflow passes.
type FetchFixturesInput struct {
	From time.Time
	To   time.Time
}

// FetchFixturesOutput is the raw API response array. The next
// activity translates each to domain.
type FetchFixturesOutput struct {
	Fixtures []apifootball.APIFixture
	Count    int
}

// FetchFixturesForWindow calls api-sports.io /fixtures with the
// [From, To] window. Returns the raw response array; translation to
// domain types happens in CategorizeAndUpsertFixtures so that the
// fetch step stays a pure I/O boundary.
func (a *Activities) FetchFixturesForWindow(ctx context.Context, in FetchFixturesInput) (FetchFixturesOutput, error) {
	fixtures, err := a.APIFootball.ListFixtures(ctx, apifootball.FixtureListParams{
		From: in.From,
		To:   in.To,
	})
	if err != nil {
		return FetchFixturesOutput{}, fmt.Errorf("ingest.FetchFixturesForWindow: %w", err)
	}
	return FetchFixturesOutput{Fixtures: fixtures, Count: len(fixtures)}, nil
}

// FetchFixturesByIDsInput carries the ManualFixtureIDs list from the
// workflow's ad-hoc-reingest path.
type FetchFixturesByIDsInput struct {
	IDs []int64
}

// FetchFixturesByIDs calls api-sports.io /fixtures with an ids= query.
// api-sports.io accepts up to 20 IDs per call; the adapter enforces
// that cap client-side (returns an error for >20). Reuses the same
// FetchFixturesOutput shape so downstream activities don't care which
// fetch path fed them.
func (a *Activities) FetchFixturesByIDs(ctx context.Context, in FetchFixturesByIDsInput) (FetchFixturesOutput, error) {
	fixtures, err := a.APIFootball.ListFixturesByIDs(ctx, in.IDs)
	if err != nil {
		return FetchFixturesOutput{}, fmt.Errorf("ingest.FetchFixturesByIDs: %w", err)
	}
	return FetchFixturesOutput{Fixtures: fixtures, Count: len(fixtures)}, nil
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
