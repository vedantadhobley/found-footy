// teams.go — the /leagues + /teams endpoints, used by IngestWorkflow's
// step-0 tracked-team refresh. Together these two calls answer "who
// plays in league X for the currently-active season," which is what
// the fixture-fetch filter needs.
//
// Mirrors Python's `get_current_season` + `get_teams_for_league` in
// archive/src/api/api_client.py. Response types are DOMAIN-agnostic —
// activity code translates to domain team types.
package apifootball

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// APILeague is one item in the /leagues response. Only the fields the
// season-lookup path needs are named; the endpoint returns much more
// (coverage flags, country info, logos) that we intentionally drop.
type APILeague struct {
	League  APILeagueMeta     `json:"league"`
	Country APILeagueCountry  `json:"country"`
	Seasons []APILeagueSeason `json:"seasons"`
}

type APILeagueMeta struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "League" | "Cup"
	Logo string `json:"logo"`
}

type APILeagueCountry struct {
	Name string  `json:"name"`
	Code *string `json:"code"` // nullable — e.g. World Cup has no country
	Flag *string `json:"flag"`
}

// APILeagueSeason mirrors what the API sends per-season for a given
// league. Current==true marks the season we care about for team-roster
// lookup.
type APILeagueSeason struct {
	Year    int    `json:"year"`
	Start   string `json:"start"` // YYYY-MM-DD
	End     string `json:"end"`   // YYYY-MM-DD
	Current bool   `json:"current"`
}

// APITeamEnvelope is one item in the /teams response — the endpoint
// nests each team under a "team" key alongside "venue" info we don't
// currently consume.
type APITeamEnvelope struct {
	Team  APITeam      `json:"team"`
	Venue APITeamVenue `json:"venue"`
}

// APITeam is the team identity. `National` distinguishes club teams
// from national teams — could inform tournament-vs-league logic later.
type APITeam struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Code     *string `json:"code"`
	Country  string  `json:"country"`
	Founded  *int    `json:"founded"`
	National bool    `json:"national"`
	Logo     string  `json:"logo"`
}

type APITeamVenue struct {
	ID       *int    `json:"id"`
	Name     *string `json:"name"`
	Address  *string `json:"address"`
	City     *string `json:"city"`
	Capacity *int    `json:"capacity"`
	Surface  *string `json:"surface"`
	Image    *string `json:"image"`
}

// GetCurrentSeason returns the currently-active season year for a
// league (e.g. 2026 for the 2026-27 season by API convention). Falls
// back to the most-recent-year season if the API doesn't mark one
// current — matches Python's fallback (`archive/src/api/api_client.py:174`).
//
// Returns (0, error) on transport / decode failures. Returns
// (season, nil) if a season was resolved. Returns (0, nil) with a
// descriptive error only if the league itself doesn't exist in the API.
func (c *Client) GetCurrentSeason(ctx context.Context, leagueID int) (int, error) {
	q := url.Values{"id": {strconv.Itoa(leagueID)}}

	var envelope struct {
		Response []APILeague `json:"response"`
		Errors   any         `json:"errors"`
	}
	if err := c.getJSON(ctx, "leagues", "/leagues", q, &envelope); err != nil {
		return 0, err
	}
	if len(envelope.Response) == 0 {
		return 0, fmt.Errorf("apifootball.GetCurrentSeason: league %d not found", leagueID)
	}
	seasons := envelope.Response[0].Seasons
	if len(seasons) == 0 {
		return 0, fmt.Errorf("apifootball.GetCurrentSeason: league %d has no seasons", leagueID)
	}

	// Prefer the season the API flagged current.
	for _, s := range seasons {
		if s.Current {
			return s.Year, nil
		}
	}
	// Fallback: latest year. Matches Python behavior.
	latest := seasons[0].Year
	for _, s := range seasons[1:] {
		if s.Year > latest {
			latest = s.Year
		}
	}
	return latest, nil
}

// ListTeamsForLeague returns every team registered to a league for a
// specific season. Used by the tracked-teams refresh step to build the
// filter set. Zero-length response is not an error — a league may
// have no teams yet (pre-season, upcoming competition).
func (c *Client) ListTeamsForLeague(ctx context.Context, leagueID, season int) ([]APITeam, error) {
	q := url.Values{
		"league": {strconv.Itoa(leagueID)},
		"season": {strconv.Itoa(season)},
	}

	var envelope struct {
		Response []APITeamEnvelope `json:"response"`
		Errors   any               `json:"errors"`
	}
	if err := c.getJSON(ctx, "teams", "/teams", q, &envelope); err != nil {
		return nil, err
	}
	teams := make([]APITeam, 0, len(envelope.Response))
	for _, e := range envelope.Response {
		if e.Team.ID == 0 {
			// Defensive: skip malformed items. Not observed in prod but
			// cheap to guard against.
			continue
		}
		teams = append(teams, e.Team)
	}
	return teams, nil
}

// GetTeamProfile returns the full team + venue envelope for one team ID. It is
// retained as an adapter capability from the retired alias resolver and has no
// production caller.
//
// Returns (nil, error) on transport/decode failure. Returns (nil, err)
// with a "not found" error if the vendor's response is empty for the
// given ID.
func (c *Client) GetTeamProfile(ctx context.Context, teamID int64) (*APITeamEnvelope, error) {
	q := url.Values{"id": {strconv.FormatInt(teamID, 10)}}

	var envelope struct {
		Response []APITeamEnvelope `json:"response"`
		Errors   any               `json:"errors"`
	}
	if err := c.getJSON(ctx, "teams_profile", "/teams", q, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Response) == 0 {
		return nil, fmt.Errorf("apifootball.GetTeamProfile: team %d not found", teamID)
	}
	return &envelope.Response[0], nil
}
