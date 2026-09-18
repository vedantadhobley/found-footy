// dto.go — the read API's response shapes (api-contract.md, settled 2026-08-09).
// Hand-shaped JSON from the domain models — NOT the Python Mongo passthrough.
// One type per resource, composed: fixtureDTO ⊃ []eventDTO ⊃ []videoDTO. The
// mappers translate domain → DTO so the handlers stay thin. `eventDTO.FixtureID`
// is the one field that lets an event-scope refetch splice into a cached fixture.
package api

import (
	"time"

	"github.com/vedantadhobley/found-footy/internal/contract/eventpresentation"
	"github.com/vedantadhobley/found-footy/internal/contract/fixturepresentation"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// sideDTO is one side of a fixture (home/away): identity + result. Score and
// winner are pointers. Winner is derived from the current match score (or a
// terminal shootout), so it is null before scoring data or while tied. Both are
// emitted explicitly so the frontend can tell "0-0" from "not started".
type sideDTO struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Score  *int   `json:"score"`
	Winner *bool  `json:"winner"`
}

type leagueDTO struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Season   int    `json:"season"`
	Country  string `json:"country"`  // "World", "USA", "England" — the competition line
	Priority int    `json:"priority"` // producer-owned competition order; lower renders first
	Round    string `json:"round"`    // raw provider value retained for diagnostics
	// RoundLabel + RoundKind are the normalized public presentation. Consumers
	// do not parse API-Football strings such as "Regular Season - 12".
	RoundLabel string                        `json:"round_label"`
	RoundKind  fixturepresentation.RoundKind `json:"round_kind"`
}

// penaltyDTO is the shootout result (home (4) — (3) away). Present on the
// fixture only when a shootout happened; null otherwise. The rest of the score
// breakdown (halftime/fulltime/extratime) is intentionally not surfaced.
type penaltyDTO struct {
	Home int `json:"home"`
	Away int `json:"away"`
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
	// Kind and Presentation are provider-independent semantic display inputs.
	// Type/detail remain during the additive cutover for older consumers.
	Kind         eventpresentation.Kind         `json:"kind"`
	Presentation eventpresentation.Presentation `json:"presentation"`
	// PresentationState is the complete consumer-facing lifecycle. It splits
	// compatibility phase=detected into unidentified versus confirming.
	PresentationState eventpresentation.State `json:"presentation_state"`
	// Phase is the coarse compatibility lifecycle retained during the additive
	// cutover. New consumers render PresentationState + Videos (orthogonal).
	// No layer-1 fields (edw.completed_at, outcome_class, the legacy
	// monitor/download flags) cross this boundary.
	Phase string `json:"phase"`
	// DebounceCount (0–3) — raw, for "confirming N/3" during presentation_state=confirming.
	DebounceCount int        `json:"debounce_count"`
	Videos        []videoDTO `json:"videos"`
}

type fixtureDTO struct {
	ID      int64     `json:"id"`
	State   string    `json:"state"`
	Kickoff time.Time `json:"kickoff"`
	// Projection is embedded so REST and fixture.status expose the
	// identical presentation_state/clock/status/display field set.
	fixturepresentation.Projection
	League         leagueDTO   `json:"league"`
	Home           sideDTO     `json:"home"`
	Away           sideDTO     `json:"away"`
	Penalty        *penaltyDTO `json:"penalty"` // shootout result; null when no shootout
	LastActivityAt *time.Time  `json:"last_activity_at"`
	Events         []eventDTO  `json:"events"`
}

// searchEventMatchDTO names the exact event fields that matched the query.
// Both flags are retained because a query can match player and assist text on
// the same event.
type searchEventMatchDTO struct {
	EventID string `json:"event_id"`
	Player  bool   `json:"player"`
	Assist  bool   `json:"assist"`
}

// searchMatchDTO is backend-owned match provenance. It describes why the full
// fixture was returned without asking a consumer to repeat normalization or
// substring rules.
type searchMatchDTO struct {
	Competition bool                  `json:"competition"`
	HomeTeam    bool                  `json:"home_team"`
	AwayTeam    bool                  `json:"away_team"`
	Events      []searchEventMatchDTO `json:"events"`
}

// searchFixtureDTO preserves the ordinary flat fixture shape and adds one
// search-only field. Existing consumers can ignore search_match during the
// additive rollout; updated consumers use it as the sole highlight authority.
type searchFixtureDTO struct {
	fixtureDTO
	SearchMatch searchMatchDTO `json:"search_match"`
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
func toEventDTO(
	e *event.Event,
	videos []videoDTO,
	discoveryComplete bool,
	projection eventpresentation.Projection,
) eventDTO {
	d := eventDTO{
		ID: e.ID.String(), FixtureID: e.FixtureID,
		Type: string(e.Type), Detail: string(e.Detail),
		Minute: e.Minute, Extra: e.Extra,
		Team:              teamRefDTO{ID: e.Team.ID, Name: e.Team.Name},
		Kind:              projection.Kind,
		Presentation:      projection.Presentation,
		PresentationState: eventpresentation.DeriveState(e, discoveryComplete),
		Phase:             string(event.DerivePhase(e.Removed, e.DownstreamTriggered, discoveryComplete)),
		DebounceCount:     e.DebounceCount,
		Videos:            ensureVideos(videos),
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
	round := fixturepresentation.PresentRound(f.League.Round)
	return fixtureDTO{
		ID:         f.ID,
		State:      string(f.State),
		Kickoff:    f.Kickoff,
		Projection: fixturepresentation.From(f.APIStatus, f.APIElapsed, f.APIExtra),
		League: leagueDTO{
			ID: f.League.ID, Name: f.League.Name, Season: f.League.Season,
			Country:  f.League.Country,
			Priority: fixturepresentation.CompetitionPriority(f.League.ID),
			Round:    f.League.Round, RoundLabel: round.Label, RoundKind: round.Kind,
		},
		Home:           sideDTO{ID: f.Home.ID, Name: f.Home.Name, Score: f.HomeScore, Winner: f.HomeWinner},
		Away:           sideDTO{ID: f.Away.ID, Name: f.Away.Name, Score: f.AwayScore, Winner: f.AwayWinner},
		Penalty:        toPenaltyDTO(f.HomePenalty, f.AwayPenalty),
		LastActivityAt: lastActivityAt,
		Events:         ensureEvents(events),
	}
}

// toSearchMatchDTO maps typed domain provenance onto the public wire shape.
func toSearchMatchDTO(match fixture.SearchMatch) searchMatchDTO {
	events := make([]searchEventMatchDTO, len(match.Events))
	for i, eventMatch := range match.Events {
		events[i] = searchEventMatchDTO{
			EventID: eventMatch.EventID.String(),
			Player:  eventMatch.Player,
			Assist:  eventMatch.Assist,
		}
	}
	return searchMatchDTO{
		Competition: match.Competition,
		HomeTeam:    match.HomeTeam,
		AwayTeam:    match.AwayTeam,
		Events:      events,
	}
}

// deriveLastActivity computes last_activity_at at read time — the recency sort
// key. It is the wall-clock of the fixture's most recent NOTEWORTHY moment: the
// max of activation, first terminal observation, and the first-seen time of any
// surviving known-scorer goal/card. Legacy/direct-complete rows without a
// terminal observation fall back to completion time. NOT poll time or clocks.
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
	if f.TerminalObservedAt != nil {
		consider(*f.TerminalObservedAt)
	} else if f.State == fixture.StateCompleted && f.CompletedAt != nil {
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
