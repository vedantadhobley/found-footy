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
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Season  int    `json:"season"`
	Country string `json:"country"` // "World", "USA", "England" — the competition line
	Round   string `json:"round"`   // "Regular Season - 12", "Group Stage - 1"
}

// penaltyDTO is the shootout result (home (4) — (3) away). Present on the
// fixture only when a shootout happened; null otherwise. The rest of the score
// breakdown (halftime/fulltime/extratime) is intentionally not surfaced.
type penaltyDTO struct {
	Home int `json:"home"`
	Away int `json:"away"`
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
	Assist    *playerDTO `json:"assist"` // the assisting player; null when none
	// Phase — the derived semantic lifecycle: detected / searching / complete /
	// removed (see internal/domain/event/phase.go). The layer-2 contract: the
	// frontend renders this + Videos (orthogonal) without re-deriving pipeline
	// state. No layer-1 fields (edw.completed_at, outcome_class, the legacy
	// monitor/download flags) cross this boundary.
	Phase string `json:"phase"`
	// DebounceCount (0–3) — raw, for "confirming N/3" during the detected phase.
	DebounceCount int        `json:"debounce_count"`
	Videos        []videoDTO `json:"videos"`
}

type fixtureDTO struct {
	ID             int64       `json:"id"`
	State          string      `json:"state"`
	Kickoff        time.Time   `json:"kickoff"`
	League         leagueDTO   `json:"league"`
	Home           sideDTO     `json:"home"`
	Away           sideDTO     `json:"away"`
	Penalty        *penaltyDTO `json:"penalty"` // shootout result; null when no shootout
	Status         statusDTO   `json:"status"`
	LastActivityAt *time.Time  `json:"last_activity_at"`
	Events         []eventDTO  `json:"events"`
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

// toEventDTO maps an event plus its already-loaded live videos and the
// discovery-complete signal (whether the event's discovery workflow has
// finished) — the third input, alongside Removed + DownstreamTriggered, that
// event.DerivePhase needs. videos may be nil (no clips yet) → serialized as an
// empty array by ensureVideos.
func toEventDTO(e *event.Event, videos []videoDTO, discoveryComplete bool) eventDTO {
	d := eventDTO{
		ID: e.ID.String(), FixtureID: e.FixtureID,
		Type: string(e.Type), Detail: string(e.Detail),
		Minute: e.Minute, Extra: e.Extra,
		Team:          teamRefDTO{ID: e.Team.ID, Name: e.Team.Name},
		Phase:         string(event.DerivePhase(e.Removed, e.DownstreamTriggered, discoveryComplete)),
		DebounceCount: e.DebounceCount,
		Videos:        ensureVideos(videos),
	}
	if e.Player.Known() {
		d.Player = &playerDTO{ID: *e.Player.ID, Name: *e.Player.Name}
	}
	if e.Assist.Known() {
		d.Assist = &playerDTO{ID: *e.Assist.ID, Name: *e.Assist.Name}
	}
	return d
}

// toFixtureDTO maps a fixture plus its already-loaded events. lastActivityAt is
// the derived recency key (see deriveLastActivity) — the caller computes it from
// the domain events, since eventDTO doesn't carry first_seen_at.
func toFixtureDTO(f *fixture.Fixture, events []eventDTO, lastActivityAt *time.Time) fixtureDTO {
	return fixtureDTO{
		ID: f.ID, State: string(f.State), Kickoff: f.Kickoff,
		League: leagueDTO{
			ID: f.League.ID, Name: f.League.Name, Season: f.League.Season,
			Country: f.League.Country, Round: f.League.Round,
		},
		Home:    sideDTO{ID: f.Home.ID, Name: f.Home.Name, Score: f.HomeScore, Winner: f.HomeWinner},
		Away:    sideDTO{ID: f.Away.ID, Name: f.Away.Name, Score: f.AwayScore, Winner: f.AwayWinner},
		Penalty: toPenaltyDTO(f.HomePenalty, f.AwayPenalty),
		Status: statusDTO{
			Short: string(f.APIStatus.Short), Long: f.APIStatus.Long,
			Elapsed: f.APIElapsed, Extra: f.APIExtra,
		},
		LastActivityAt: lastActivityAt,
		Events:         ensureEvents(events),
	}
}

// deriveLastActivity computes last_activity_at at read time — the recency sort
// key. It is the wall-clock of the fixture's most recent NOTEWORTHY moment: the
// max of activation, completion, and the first-seen time of any surviving
// known-scorer goal/card. NOT poll time, clock ticks, or status transitions.
//
// events must be the fixture's non-removed events (ListByFixture already excludes
// removed) — so a VAR overturn reverts the value for free, and unknown-scorer
// placeholders (no player) never count. Nil for a not-yet-activated (staging)
// fixture. See decisions.md 2026-08-14.
func deriveLastActivity(f *fixture.Fixture, events []*event.Event) *time.Time {
	var latest time.Time
	var set bool
	consider := func(t time.Time) {
		if !set || t.After(latest) {
			latest, set = t, true
		}
	}
	if f.ActivatedAt != nil {
		consider(*f.ActivatedAt)
	}
	if f.CompletedAt != nil {
		consider(*f.CompletedAt)
	}
	for _, e := range events {
		if e.Player.Known() {
			consider(e.FirstSeenAt)
		}
	}
	if !set {
		return nil
	}
	return &latest
}

// toPenaltyDTO returns the shootout result only when one occurred — both sides
// have a penalty score. A partial/absent shootout maps to null.
func toPenaltyDTO(home, away *int) *penaltyDTO {
	if home == nil || away == nil {
		return nil
	}
	return &penaltyDTO{Home: *home, Away: *away}
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
