// Fixture categorization, state reconciliation, and team-reference collection.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// CategorizeInput carries the API response and activation window. The activity
// translates and upserts each fixture.
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
	// ChangedIDs — fixtures newly inserted or with a frontend-meaningful field
	// change (status/kickoff/score/penalty/winner) this ingest. The workflow
	// emits one fixture.update for them (N6).
	ChangedIDs []int64
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
		final, changed, err := a.reconcileFixture(ctx, apiFix, in.ActivationWindow, now)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("reconcile fixture=%d: %v", apiFix.Fixture.ID, err))
			continue
		}
		if err := a.FixtureRepo.Upsert(ctx, final); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("upsert fixture=%d: %v", apiFix.Fixture.ID, err))
			continue
		}
		if changed {
			out.ChangedIDs = append(out.ChangedIDs, final.ID)
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
//
//	existing == nil    → fresh row constructed from API, initial
//	                     state applied (staging / active / completed)
//	existing != nil    → API-mutable fields refreshed, domain fields
//	                     (state timestamps, created_at) preserved
func (a *Activities) reconcileFixture(
	ctx context.Context,
	apiFix apifootball.APIFixture,
	activationWindow time.Duration,
	now time.Time,
) (*fixture.Fixture, bool, error) {
	existing, err := a.FixtureRepo.Get(ctx, apiFix.Fixture.ID)
	if err != nil && !errors.Is(err, fixture.ErrNotFound) {
		return nil, false, fmt.Errorf("Get: %w", err)
	}

	if existing != nil {
		// N6: snapshot the frontend-meaningful fields before overwriting so we
		// can tell whether this refresh actually changed anything worth pushing
		// as fixture.update (a bare LastPolledAt/UpdatedAt bump is not).
		prevStatus := existing.APIStatus.Short
		prevKickoff := existing.Kickoff
		prevHS, prevAS := existing.HomeScore, existing.AwayScore
		prevHP, prevAP := existing.HomePenalty, existing.AwayPenalty
		prevHW, prevAW := existing.HomeWinner, existing.AwayWinner

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
		existing.League = fixture.League{
			ID: apiFix.League.ID, Name: apiFix.League.Name, Season: apiFix.League.Season,
			Country: apiFix.League.Country, Round: apiFix.League.Round,
		}
		existing.HomeScore = apiFix.Goals.Home
		existing.AwayScore = apiFix.Goals.Away
		existing.UpdatePenalty(apiFix.Score.Penalty.Home, apiFix.Score.Penalty.Away)
		existing.UpdateResult(apiFix.Teams.Home.Winner, apiFix.Teams.Away.Winner)
		existing.LastPolledAt = &now
		existing.UpdatedAt = now

		changed := string(prevStatus) != string(existing.APIStatus.Short) ||
			!prevKickoff.Equal(existing.Kickoff) ||
			!reflect.DeepEqual(prevHS, existing.HomeScore) ||
			!reflect.DeepEqual(prevAS, existing.AwayScore) ||
			!reflect.DeepEqual(prevHP, existing.HomePenalty) ||
			!reflect.DeepEqual(prevAP, existing.AwayPenalty) ||
			!reflect.DeepEqual(prevHW, existing.HomeWinner) ||
			!reflect.DeepEqual(prevAW, existing.AwayWinner)
		return existing, changed, nil
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
		fixture.Team{ID: apiFix.Teams.Away.ID, Name: apiFix.Teams.Away.Name},
		fixture.League{
			ID: apiFix.League.ID, Name: apiFix.League.Name, Season: apiFix.League.Season,
			Country: apiFix.League.Country, Round: apiFix.League.Round,
		},
	)
	f.APIElapsed = apiFix.Fixture.Status.Elapsed
	f.APIExtra = apiFix.Fixture.Status.Extra
	f.HomeScore = apiFix.Goals.Home
	f.AwayScore = apiFix.Goals.Away
	f.UpdatePenalty(apiFix.Score.Penalty.Home, apiFix.Score.Penalty.Away)
	f.UpdateResult(apiFix.Teams.Home.Winner, apiFix.Teams.Away.Winner)
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
			return nil, false, fmt.Errorf("initial Activate for terminal: %w", err)
		}
		if err := f.Complete(now); err != nil {
			return nil, false, fmt.Errorf("initial Complete for terminal: %w", err)
		}
	case f.APIStatus.Live():
		if err := f.Activate(now); err != nil {
			return nil, false, fmt.Errorf("initial Activate for live: %w", err)
		}
	default:
		if f.ShouldActivateNow(now, activationWindow) {
			if err := f.Activate(now); err != nil {
				return nil, false, fmt.Errorf("initial Activate for imminent: %w", err)
			}
		}
	}
	// A fresh fixture is a change by definition → fixture.update.
	return f, true, nil
}
