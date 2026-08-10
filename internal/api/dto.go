// dto.go — the read API's response shapes (api-contract.md, settled 2026-08-09).
// Hand-shaped JSON from the domain models — NOT the Python Mongo passthrough.
// One type per resource, composed: fixtureDTO ⊃ []eventDTO ⊃ []videoDTO. The
// mappers translate domain → DTO so the handlers stay thin. `eventDTO.FixtureID`
// is the one field that lets an event-scope refetch splice into a cached fixture.
package api

import (
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// sideDTO is one side of a fixture (home/away): identity + result. Score and
// winner are pointers — null until the vendor reports them (pre-kickoff /
// pre-decision), emitted explicitly (no omitempty) so the frontend can tell
// "0-0" from "not started".
type sideDTO struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Score  *int   `json:"score"`
	Winner *bool  `json:"winner"`
}

type leagueDTO struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Season int    `json:"season"`
}

// statusDTO is the API-reported match status. Elapsed/extra are the live clock.
type statusDTO struct {
	Short   string `json:"short"`
	Long    string `json:"long"`
	Elapsed *int   `json:"elapsed"`
	Extra   *int   `json:"extra"`
}

type teamRefDTO struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type playerDTO struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// videoDTO is one live clip. `url` is the share-id redirect endpoint; the
// browser follows its 302 to a presigned Garage URL. rank 1 = primary.
type videoDTO struct {
	ShareID         string `json:"share_id"`
	URL             string `json:"url"`
	Rank            int    `json:"rank"`
	Verified        bool   `json:"verified"`
	ExtractedMinute *int   `json:"extracted_minute"`
	Popularity      int    `json:"popularity"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	DurationMS      int    `json:"duration_ms"`
}

type eventDTO struct {
	ID        string     `json:"id"`
	FixtureID int64      `json:"fixture_id"`
	Type      string     `json:"type"`
	Detail    string     `json:"detail"`
	Minute    int        `json:"minute"`
	Extra     *int       `json:"extra"`
	Team      teamRefDTO `json:"team"`
	Player    *playerDTO `json:"player"` // null for an unknown scorer
	Videos    []videoDTO `json:"videos"`
}

type fixtureDTO struct {
	ID             int64      `json:"id"`
	State          string     `json:"state"`
	Kickoff        time.Time  `json:"kickoff"`
	League         leagueDTO  `json:"league"`
	Home           sideDTO    `json:"home"`
	Away           sideDTO    `json:"away"`
	Status         statusDTO  `json:"status"`
	LastActivityAt *time.Time `json:"last_activity_at"`
	Events         []eventDTO `json:"events"`
}

// GET /fixtures and /fixtures?ids=… both return a flat []fixtureDTO — one shape
// for one / many / all, so "pass a list" works exactly like "pass one". The
// frontend keys by id and buckets by each fixture's `state`. Likewise /events
// and /events?ids=… return []eventDTO. (No three-bucket window object.)

// videoURL builds the public playback URL for a share id (the 302 endpoint).
func videoURL(shareID string) string { return "/api/v1/videos/" + shareID }

// toVideoDTO maps one live clip.
func toVideoDTO(c video.LiveClip) videoDTO {
	return videoDTO{
		ShareID: c.ShareID, URL: videoURL(c.ShareID), Rank: c.Rank,
		Verified: c.Verified, ExtractedMinute: c.ExtractedMinute, Popularity: c.Popularity,
		Width: c.Width, Height: c.Height, DurationMS: c.DurationMS,
	}
}

// toEventDTO maps an event plus its already-loaded live videos. videos may be
// nil (no clips yet) → serialized as an empty array by ensureVideos.
func toEventDTO(e *event.Event, videos []videoDTO) eventDTO {
	d := eventDTO{
		ID: e.ID.String(), FixtureID: e.FixtureID,
		Type: string(e.Type), Detail: string(e.Detail),
		Minute: e.Minute, Extra: e.Extra,
		Team:   teamRefDTO{ID: e.Team.ID, Name: e.Team.Name},
		Videos: ensureVideos(videos),
	}
	if e.Player.Known() {
		d.Player = &playerDTO{ID: *e.Player.ID, Name: *e.Player.Name}
	}
	return d
}

// toFixtureDTO maps a fixture plus its already-loaded events.
func toFixtureDTO(f *fixture.Fixture, events []eventDTO) fixtureDTO {
	return fixtureDTO{
		ID: f.ID, State: string(f.State), Kickoff: f.Kickoff,
		League: leagueDTO{ID: f.League.ID, Name: f.League.Name, Season: f.League.Season},
		Home:   sideDTO{ID: f.Home.ID, Name: f.Home.Name, Score: f.HomeScore, Winner: f.HomeWinner},
		Away:   sideDTO{ID: f.Away.ID, Name: f.Away.Name, Score: f.AwayScore, Winner: f.AwayWinner},
		Status: statusDTO{
			Short: string(f.APIStatus.Short), Long: f.APIStatus.Long,
			Elapsed: f.APIElapsed, Extra: f.APIExtra,
		},
		LastActivityAt: f.LastActivityAt,
		Events:         ensureEvents(events),
	}
}

// ensureVideos / ensureEvents turn a nil slice into an empty one so JSON emits
// `[]` rather than `null` — the frontend always gets an array to iterate.
func ensureVideos(v []videoDTO) []videoDTO {
	if v == nil {
		return []videoDTO{}
	}
	return v
}
func ensureEvents(e []eventDTO) []eventDTO {
	if e == nil {
		return []eventDTO{}
	}
	return e
}
