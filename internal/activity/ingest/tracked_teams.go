// Tracked-team cache refresh activity and partial-failure preservation.
package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/team"
)

// RefreshTrackedTeamsIfStaleInput carries no fields. The activity reads its
// league IDs and cache age from Activities.
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
