// Read-model regressions keep provisional event scores consistent across all public reads.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/contract/eventpresentation"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

// TestEventScoresDoNotGateFixtureScoreOrDependOnAggregate covers every public assembly path.
func TestEventScoresDoNotGateFixtureScoreOrDependOnAggregate(t *testing.T) {
	for _, tc := range []struct {
		name string
		home *int
	}{
		{"score_first", ip(2)}, {"event_first", ip(0)}, {"unknown_score", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fixtureID, eventID := scaffold()
			h.Fixtures.(*fakeFixtures).byID[fixtureID].HomeScore = tc.home
			var baseline eventpresentation.Presentation
			for _, route := range []string{
				"/api/v1/fixtures", fmt.Sprintf("/api/v1/fixtures?ids=%d", fixtureID), "/api/v1/search?q=Alpha",
			} {
				rec := get(h, route)
				require.Equal(t, http.StatusOK, rec.Code, route)
				var fixtures []fixtureDTO
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fixtures))
				require.Len(t, fixtures, 1)
				require.Equal(t, tc.home, fixtures[0].Home.Score, "fixture scoreboard changed with its event projection")
				require.Len(t, fixtures[0].Events, 1)
				presentation := fixtures[0].Events[0].Presentation
				require.Equal(t, &eventpresentation.Score{Home: 0, Away: 0}, presentation.ScoreBefore)
				require.Equal(t, &eventpresentation.Score{Home: 1, Away: 0}, presentation.ScoreAfter)
				baseline = presentation
			}
			rec := get(h, "/api/v1/events?ids="+eventID.String())
			require.Equal(t, http.StatusOK, rec.Code)
			var events []eventDTO
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &events))
			require.Len(t, events, 1)
			require.Equal(t, baseline, events[0].Presentation)
		})
	}
}

// TestRemovedSameClockTargetDoesNotAlterSurvivingContext pins fixture/event endpoint equivalence.
func TestRemovedSameClockTargetDoesNotAlterSurvivingContext(t *testing.T) {
	h, fixtureID, survivorID := scaffold()
	removedID := uuid.New()
	survivor := h.Events.(*fakeEvents).byID[survivorID]
	h.Events.(*fakeEvents).byID[removedID] = &event.Event{
		ID: removedID, FixtureID: fixtureID, Type: event.TypeGoal,
		Minute: survivor.Minute, Team: survivor.Team, Removed: true,
	}
	for _, requested := range []string{survivorID.String(), survivorID.String() + "," + removedID.String()} {
		rec := get(h, "/api/v1/events?ids="+requested)
		require.Equal(t, http.StatusOK, rec.Code)
		var events []eventDTO
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &events))
		byID := make(map[string]eventDTO, len(events))
		for _, e := range events {
			byID[e.ID] = e
		}
		require.Contains(t, byID, survivorID.String())
		presentation := byID[survivorID.String()].Presentation
		require.Equal(t, &eventpresentation.Score{Home: 0, Away: 0}, presentation.ScoreBefore)
		require.Equal(t, &eventpresentation.Score{Home: 1, Away: 0}, presentation.ScoreAfter)
	}
}
