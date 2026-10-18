// Timeline regressions preserve independent score arrival and isolate real ambiguity.
package eventpresentation

import (
	"testing"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// TestBuildMalagaScorerCorrection pins the October 18 transient double-goal inventory.
func TestBuildMalagaScorerCorrection(t *testing.T) {
	f := &fixture.Fixture{Home: fixture.Team{ID: 10}, Away: fixture.Team{ID: 20}, HomeScore: intPointer(2), AwayScore: intPointer(4)}
	goal := func(seq, team, minute int) *event.Event {
		return makeEvent(seq, event.TypeGoal, apifootball.DetailNormalGoal, team, minute, nil, false)
	}
	kubo, oskarsson := goal(1, 20, 3), goal(2, 20, 26)
	nino, oyarzabal59 := goal(3, 10, 54), goal(4, 20, 59)
	recio, pablo := goal(5, 10, 66), goal(7, 10, 66)
	oyarzabal67, guedes := goal(6, 20, 67), goal(8, 20, 73)
	events := []*event.Event{kubo, oskarsson, nino, oyarzabal59, recio, oyarzabal67}
	got := Build(f, events)
	assertProjection(t, got[oyarzabal67.ID], KindGoal, "Goal", TeamSideAway, &Score{2, 3}, &Score{2, 4})

	// Both identities survive during correction debounce. Only the tied goals
	// lose individual order; the other rows keep provisional score boundaries.
	events = append(events, pablo)
	got = Build(f, events)
	assertProjection(t, got[kubo.ID], KindGoal, "Goal", TeamSideAway, &Score{0, 0}, &Score{0, 1})
	for _, e := range []*event.Event{recio, pablo} {
		if got[e.ID].Presentation.ScoreBefore != nil || got[e.ID].Presentation.ScoreAfter != nil {
			t.Fatal("same-clock replacement identities claimed a real order")
		}
	}
	assertProjection(t, got[oyarzabal67.ID], KindGoal, "Goal", TeamSideAway, &Score{3, 3}, &Score{3, 4})

	// A removed historical target cannot perturb the surviving projection.
	recio.Removed = true
	got = Build(f, events)
	assertProjection(t, got[pablo.ID], KindGoal, "Goal", TeamSideHome, &Score{1, 3}, &Score{2, 3})
	assertProjection(t, got[oyarzabal67.ID], KindGoal, "Goal", TeamSideAway, &Score{2, 3}, &Score{2, 4})

	// Guedes's aggregate score leads his event by one poll. Existing scorelines
	// do not disappear; after arrival they advance without consulting that score.
	f.AwayScore = intPointer(5)
	got = Build(f, events)
	assertProjection(t, got[oyarzabal67.ID], KindGoal, "Goal", TeamSideAway, &Score{2, 3}, &Score{2, 4})
	events = append(events, guedes)
	got = Build(f, events)
	assertProjection(t, got[guedes.ID], KindGoal, "Goal", TeamSideAway, &Score{2, 4}, &Score{2, 5})
}

// TestBuildLateEarlierGoalAndVAR recomputes chronology without cached scorelines or final-score guesses.
func TestBuildLateEarlierGoalAndVAR(t *testing.T) {
	f := &fixture.Fixture{Home: fixture.Team{ID: 10}, Away: fixture.Team{ID: 20}}
	late := makeEvent(2, event.TypeGoal, apifootball.DetailNormalGoal, 10, 10, nil, false)
	later := makeEvent(1, event.TypeGoal, apifootball.DetailNormalGoal, 20, 30, nil, false)
	got := Build(f, []*event.Event{later})
	assertProjection(t, got[later.ID], KindGoal, "Goal", TeamSideAway, &Score{0, 0}, &Score{0, 1})
	got = Build(f, []*event.Event{later, late})
	assertProjection(t, got[later.ID], KindGoal, "Goal", TeamSideAway, &Score{1, 0}, &Score{1, 1})
	late.Removed = true
	got = Build(f, []*event.Event{later, late})
	assertProjection(t, got[later.ID], KindGoal, "Goal", TeamSideAway, &Score{0, 0}, &Score{0, 1})
}

// TestBuildUnknownGoalSideOnlyInvalidatesDependentBoundaries preserves earlier known rows.
func TestBuildUnknownGoalSideOnlyInvalidatesDependentBoundaries(t *testing.T) {
	f := &fixture.Fixture{Home: fixture.Team{ID: 10}, Away: fixture.Team{ID: 20}}
	first := makeEvent(1, event.TypeGoal, apifootball.DetailNormalGoal, 10, 10, nil, false)
	unknown := makeEvent(2, event.TypeGoal, apifootball.DetailNormalGoal, 99, 20, nil, false)
	last := makeEvent(3, event.TypeGoal, apifootball.DetailNormalGoal, 20, 30, nil, false)
	got := Build(f, []*event.Event{last, unknown, first})
	assertProjection(t, got[first.ID], KindGoal, "Goal", TeamSideHome, &Score{0, 0}, &Score{1, 0})
	for _, e := range []*event.Event{unknown, last} {
		if got[e.ID].Presentation.ScoreBefore != nil || got[e.ID].Presentation.ScoreAfter != nil {
			t.Fatal("unassignable goal produced a fabricated downstream score")
		}
	}
}
