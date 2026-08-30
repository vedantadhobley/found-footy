// Tests for the shared fixture-presentation projection.
package fixturepresentation

import (
	"testing"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

func TestFromClassifiesEveryProviderStatus(t *testing.T) {
	cases := map[State][]apifootball.APIStatusCode{
		StatePlaying: {
			apifootball.StatusFirstHalf, apifootball.StatusHalftime,
			apifootball.StatusSecondHalf, apifootball.StatusExtraTime,
			apifootball.StatusBreakTime, apifootball.StatusPenaltyPlay,
			apifootball.StatusLive,
		},
		StateFinished: {
			apifootball.StatusFullTime, apifootball.StatusAfterExtra,
			apifootball.StatusPenaltyDone, apifootball.StatusTechnicalLoss,
			apifootball.StatusWalkover,
		},
		StateUpcoming: {apifootball.StatusNotStarted, apifootball.StatusTBD},
		StateDeferred: {
			apifootball.StatusPostponed, apifootball.StatusCancelled,
			apifootball.StatusSuspended, apifootball.StatusInterrupted,
			apifootball.StatusAbandoned,
		},
	}
	for want, statuses := range cases {
		for _, status := range statuses {
			got := From(fixture.APIStatus{Short: status}, nil, nil)
			if got.PresentationState != want {
				t.Errorf("status %q state = %q, want %q", status, got.PresentationState, want)
			}
		}
	}
	if got := From(fixture.APIStatus{Short: "future"}, nil, nil); got.PresentationState != StateDeferred {
		t.Errorf("unknown status state = %q, want deferred", got.PresentationState)
	}
}

func TestFromSelectsClockOnlyDuringTimedPlay(t *testing.T) {
	minute, extra := 45, 2
	for _, status := range []apifootball.APIStatusCode{
		apifootball.StatusFirstHalf,
		apifootball.StatusSecondHalf,
		apifootball.StatusExtraTime,
		apifootball.StatusLive,
	} {
		got := From(fixture.APIStatus{Short: status}, &minute, &extra)
		if got.Display != DisplayClock {
			t.Errorf("status %q display = %q, want clock", status, got.Display)
		}
	}
	for _, status := range []apifootball.APIStatusCode{
		apifootball.StatusHalftime,
		apifootball.StatusBreakTime,
		apifootball.StatusPenaltyPlay,
		apifootball.StatusPenaltyDone,
		apifootball.StatusSuspended,
	} {
		got := From(fixture.APIStatus{Short: status}, &minute, &extra)
		if got.Display != DisplayStatus {
			t.Errorf("status %q display = %q, want status", status, got.Display)
		}
	}
	if got := From(fixture.APIStatus{Short: apifootball.StatusLive}, nil, nil); got.Display != DisplayStatus {
		t.Errorf("LIVE without elapsed display = %q, want status", got.Display)
	}
}

func TestFromNormalizesStatusAndEqualUsesPointerValues(t *testing.T) {
	leftMinute, rightMinute := 62, 62
	left := From(
		fixture.APIStatus{Short: apifootball.StatusSecondHalf, Long: "Second Half"},
		&leftMinute,
		nil,
	)
	right := From(
		fixture.APIStatus{Short: apifootball.StatusSecondHalf, Long: "Second Half"},
		&rightMinute,
		nil,
	)
	if left.Status.Short != "2H" {
		t.Errorf("short = %q, want 2H", left.Status.Short)
	}
	if !left.Equal(right) {
		t.Fatal("equal semantic projections with distinct pointers compared unequal")
	}
	rightMinute++
	if left.Equal(right) {
		t.Fatal("different clock values compared equal")
	}
}
