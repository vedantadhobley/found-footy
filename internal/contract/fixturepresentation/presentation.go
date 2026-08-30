// Package fixturepresentation defines the one consumer-facing fixture-status
// projection shared by the REST snapshot and the inline NATS feed.
package fixturepresentation

import (
	"strings"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// State is the coarse product grouping for a fixture. It is deliberately
// independent of Found Footy's staging/active/completed processing state.
type State string

const (
	StatePlaying  State = "playing"
	StateFinished State = "finished"
	StateUpcoming State = "upcoming"
	StateDeferred State = "deferred"
)

// Display tells a generic fixture indicator which value to render. The
// consumer formats clock values but never interprets provider status codes.
type Display string

const (
	DisplayClock  Display = "clock"
	DisplayStatus Display = "status"
)

// Clock is the provider's current match clock. Minute remains nullable for
// statuses such as LIVE where the provider explicitly has no elapsed value.
type Clock struct {
	Minute *int `json:"minute"`
	Extra  *int `json:"extra"`
}

// Status preserves the provider's display status. Short is normalized to the
// API-Football presentation convention; Long remains the provider description.
type Status struct {
	Short string `json:"short"`
	Long  string `json:"long"`
}

// Projection is the complete frontend fixture-status contract. REST embeds it
// in every fixture and fixture.presentation embeds the same fields per entry.
type Projection struct {
	PresentationState State   `json:"presentation_state"`
	Clock             Clock   `json:"clock"`
	Status            Status  `json:"status"`
	Display           Display `json:"display"`
}

// From derives the product presentation from canonical provider facts.
func From(status fixture.APIStatus, elapsed, extra *int) Projection {
	projection := Projection{
		PresentationState: presentationState(status.Short),
		Clock:             Clock{Minute: elapsed, Extra: extra},
		Status: Status{
			Short: strings.ToUpper(string(status.Short)),
			Long:  status.Long,
		},
		Display: DisplayStatus,
	}
	if elapsed != nil && statusUsesClock(status.Short) {
		projection.Display = DisplayClock
	}
	return projection
}

// Equal compares semantic values rather than pointer addresses.
func (p Projection) Equal(other Projection) bool {
	return p.PresentationState == other.PresentationState &&
		p.Status == other.Status &&
		p.Display == other.Display &&
		intPtrEqual(p.Clock.Minute, other.Clock.Minute) &&
		intPtrEqual(p.Clock.Extra, other.Clock.Extra)
}

func presentationState(status apifootball.APIStatusCode) State {
	switch status {
	case apifootball.StatusFirstHalf,
		apifootball.StatusHalftime,
		apifootball.StatusSecondHalf,
		apifootball.StatusExtraTime,
		apifootball.StatusBreakTime,
		apifootball.StatusPenaltyPlay,
		apifootball.StatusLive:
		return StatePlaying
	case apifootball.StatusFullTime,
		apifootball.StatusAfterExtra,
		apifootball.StatusPenaltyDone,
		apifootball.StatusTechnicalLoss,
		apifootball.StatusWalkover:
		return StateFinished
	case apifootball.StatusNotStarted, apifootball.StatusTBD:
		return StateUpcoming
	default:
		// Postponed, cancelled, suspended, interrupted, abandoned, and
		// unknown provider codes fail closed into the non-live group.
		return StateDeferred
	}
}

func statusUsesClock(status apifootball.APIStatusCode) bool {
	switch status {
	case apifootball.StatusFirstHalf,
		apifootball.StatusSecondHalf,
		apifootball.StatusExtraTime,
		apifootball.StatusLive:
		return true
	default:
		return false
	}
}

func intPtrEqual(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
