// Tests for provider-independent event presentation and guarded score context.
package eventpresentation

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

func TestBuildDerivesKindsLabelsSidesAndScores(t *testing.T) {
	homeScore, awayScore := 2, 1
	f := &fixture.Fixture{
		Home: fixture.Team{ID: 10, Name: "Home"}, Away: fixture.Team{ID: 20, Name: "Away"},
		HomeScore: &homeScore, AwayScore: &awayScore,
	}
	events := []*event.Event{
		makeEvent(3, event.TypeGoal, apifootball.DetailOwnGoal, 10, 45, intPointer(2), false),
		makeEvent(1, event.TypeGoal, apifootball.DetailNormalGoal, 10, 12, nil, false),
		makeEvent(4, event.TypeMissedPenalty, apifootball.DetailMissedPenalty, 20, 60, nil, false),
		makeEvent(2, event.TypeGoal, apifootball.DetailPenalty, 20, 30, nil, false),
		makeEvent(5, event.TypeCard, apifootball.DetailRedCard, 10, 70, nil, false),
	}

	got := Build(f, events)
	assertProjection(t, got[events[1].ID], KindGoal, "Goal", TeamSideHome, &Score{0, 0}, &Score{1, 0})
	assertProjection(t, got[events[3].ID], KindGoal, "Penalty Goal", TeamSideAway, &Score{1, 0}, &Score{1, 1})
	assertProjection(t, got[events[0].ID], KindGoal, "Own Goal", TeamSideHome, &Score{1, 1}, &Score{2, 1})
	assertProjection(t, got[events[2].ID], KindMissedPenalty, "Penalty Miss", TeamSideAway, nil, nil)
	assertProjection(t, got[events[4].ID], KindRedCard, "Red Card", TeamSideHome, nil, nil)
}

func TestBuildRemovedGoalIsTentativeAndDoesNotAdvanceLaterScore(t *testing.T) {
	homeScore, awayScore := 1, 1
	f := &fixture.Fixture{Home: fixture.Team{ID: 10}, Away: fixture.Team{ID: 20}, HomeScore: &homeScore, AwayScore: &awayScore}
	first := makeEvent(1, event.TypeGoal, apifootball.DetailNormalGoal, 10, 10, nil, false)
	removed := makeEvent(2, event.TypeGoal, apifootball.DetailNormalGoal, 10, 20, nil, true)
	last := makeEvent(3, event.TypeGoal, apifootball.DetailNormalGoal, 20, 30, nil, false)

	got := Build(f, []*event.Event{last, removed, first})
	assertProjection(t, got[removed.ID], KindGoal, "Goal", TeamSideHome, &Score{1, 0}, &Score{2, 0})
	assertProjection(t, got[last.ID], KindGoal, "Goal", TeamSideAway, &Score{1, 0}, &Score{1, 1})
}

func TestBuildSuppressesScoresWhenInventoryDoesNotMatchProvider(t *testing.T) {
	homeScore, awayScore := 2, 0
	f := &fixture.Fixture{Home: fixture.Team{ID: 10}, Away: fixture.Team{ID: 20}, HomeScore: &homeScore, AwayScore: &awayScore}
	goal := makeEvent(1, event.TypeGoal, apifootball.DetailNormalGoal, 10, 10, nil, false)

	projection := Build(f, []*event.Event{goal})[goal.ID]
	if projection.Presentation.ScoreBefore != nil || projection.Presentation.ScoreAfter != nil {
		t.Fatalf("mismatched inventory exposed score context: %+v", projection.Presentation)
	}
}

func TestBuildSuppressesUnknownTeamSide(t *testing.T) {
	homeScore, awayScore := 1, 0
	f := &fixture.Fixture{Home: fixture.Team{ID: 10}, Away: fixture.Team{ID: 20}, HomeScore: &homeScore, AwayScore: &awayScore}
	goal := makeEvent(1, event.TypeGoal, apifootball.DetailNormalGoal, 99, 10, nil, false)

	projection := Build(f, []*event.Event{goal})[goal.ID]
	if projection.Presentation.TeamSide != nil || projection.Presentation.ScoreAfter != nil {
		t.Fatalf("unknown event team exposed side/score: %+v", projection.Presentation)
	}
}

func makeEvent(sequence int, eventType event.Type, detail apifootball.APIEventDetail, teamID, minute int, extra *int, removed bool) *event.Event {
	return &event.Event{
		ID:   uuid.New(),
		Type: eventType, Detail: detail, Team: event.Team{ID: teamID}, Minute: minute, Extra: extra,
		FirstSeenAt: time.Date(2026, 9, 18, 12, sequence, 0, 0, time.UTC), Removed: removed,
	}
}

func intPointer(value int) *int { return &value }

func assertProjection(t *testing.T, got Projection, kind Kind, label string, side TeamSide, before, after *Score) {
	t.Helper()
	if got.Kind != kind || got.Presentation.Label != label || got.Presentation.TeamSide == nil || *got.Presentation.TeamSide != side {
		t.Fatalf("projection = %+v, want kind=%s label=%s side=%s", got, kind, label, side)
	}
	if !scoresEqual(got.Presentation.ScoreBefore, before) || !scoresEqual(got.Presentation.ScoreAfter, after) {
		t.Fatalf("scores = before %+v after %+v, want before %+v after %+v", got.Presentation.ScoreBefore, got.Presentation.ScoreAfter, before, after)
	}
}

func scoresEqual(left, right *Score) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
