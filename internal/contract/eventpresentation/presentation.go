// Package eventpresentation derives provider-independent event semantics for public consumers.
package eventpresentation

import (
	"sort"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// Kind is the provider-independent public classification of a tracked event.
type Kind string

const (
	KindGoal          Kind = "goal"
	KindRedCard       Kind = "red_card"
	KindMissedPenalty Kind = "missed_penalty"
	KindOther         Kind = "other"
)

// TeamSide identifies the event team without asking consumers to compare IDs.
type TeamSide string

const (
	TeamSideHome TeamSide = "home"
	TeamSideAway TeamSide = "away"
)

// Score is the regulation/extra-time score at one event boundary.
type Score struct {
	Home int `json:"home"`
	Away int `json:"away"`
}

// Presentation contains semantic display inputs without frontend markup.
// Score fields are nil when the retained event inventory does not reconcile
// with the provider score, so consumers never display invented history.
type Presentation struct {
	Label       string    `json:"label"`
	TeamSide    *TeamSide `json:"team_side"`
	ScoreBefore *Score    `json:"score_before"`
	ScoreAfter  *Score    `json:"score_after"`
}

// Projection is the complete additive public presentation for one event.
type Projection struct {
	Kind         Kind
	Presentation Presentation
}

// Basic derives the fields that do not require fixture or sibling-event context.
func Basic(e *event.Event) Projection {
	return Projection{
		Kind: kind(e.Type),
		Presentation: Presentation{
			Label: label(e.Type, e.Detail),
		},
	}
}

// Build derives event team sides and trustworthy score boundaries for one
// fixture. Input order is irrelevant. Removed goals receive their tentative
// score boundary but never advance the score seen by later surviving events.
func Build(f *fixture.Fixture, events []*event.Event) map[uuid.UUID]Projection {
	ordered := append([]*event.Event(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if left.Minute != right.Minute {
			return left.Minute < right.Minute
		}
		leftExtra, rightExtra := intValue(left.Extra), intValue(right.Extra)
		if leftExtra != rightExtra {
			return leftExtra < rightExtra
		}
		if !left.FirstSeenAt.Equal(right.FirstSeenAt) {
			return left.FirstSeenAt.Before(right.FirstSeenAt)
		}
		return left.ID.String() < right.ID.String()
	})

	projections := make(map[uuid.UUID]Projection, len(ordered))
	for _, e := range ordered {
		projection := Basic(e)
		projection.Presentation.TeamSide = teamSide(f, e.Team.ID)
		projections[e.ID] = projection
	}

	reliable := scoreInventoryReconciles(f, ordered)
	home, away := 0, 0
	for _, e := range ordered {
		if e.Type != event.TypeGoal {
			continue
		}
		projection := projections[e.ID]
		if reliable && projection.Presentation.TeamSide != nil {
			before := Score{Home: home, Away: away}
			after := before
			if *projection.Presentation.TeamSide == TeamSideHome {
				after.Home++
			} else {
				after.Away++
			}
			projection.Presentation.ScoreBefore = &before
			projection.Presentation.ScoreAfter = &after
			if !e.Removed {
				home, away = after.Home, after.Away
			}
			projections[e.ID] = projection
		}
	}
	return projections
}

func kind(t event.Type) Kind {
	switch t {
	case event.TypeGoal:
		return KindGoal
	case event.TypeCard:
		return KindRedCard
	case event.TypeMissedPenalty:
		return KindMissedPenalty
	default:
		return KindOther
	}
}

func label(t event.Type, detail apifootball.APIEventDetail) string {
	switch t {
	case event.TypeCard:
		return "Red Card"
	case event.TypeMissedPenalty:
		return "Penalty Miss"
	case event.TypeGoal:
		switch detail {
		case apifootball.DetailPenalty:
			return "Penalty Goal"
		case apifootball.DetailOwnGoal:
			return "Own Goal"
		default:
			return "Goal"
		}
	default:
		return "Event"
	}
}

func teamSide(f *fixture.Fixture, teamID int) *TeamSide {
	var side TeamSide
	switch teamID {
	case f.Home.ID:
		side = TeamSideHome
	case f.Away.ID:
		side = TeamSideAway
	default:
		return nil
	}
	return &side
}

func scoreInventoryReconciles(f *fixture.Fixture, events []*event.Event) bool {
	if f.HomeScore == nil || f.AwayScore == nil {
		return false
	}
	home, away := 0, 0
	for _, e := range events {
		if e.Removed || e.Type != event.TypeGoal {
			continue
		}
		side := teamSide(f, e.Team.ID)
		if side == nil {
			return false
		}
		if *side == TeamSideHome {
			home++
		} else {
			away++
		}
	}
	return home == *f.HomeScore && away == *f.AwayScore
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
