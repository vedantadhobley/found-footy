// provider_observation_test.go verifies deferred-score classification through
// Monitor's canonical facts, including evidence outside confirmed event keys.
package monitor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// TestReconcileFixture_PostponedNullScoresRemainQuiet exercises repeated
// refreshes against retained zeros without changing polling or completion.
func TestReconcileFixture_PostponedNullScoresRemainQuiet(t *testing.T) {
	stored, observed := postponedFixturePair(t)
	fRepo := newFakeFixtureRepo()
	if err := fRepo.Upsert(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	eRepo := newFakeEventRepo()
	now := time.Date(2026, 9, 8, 13, 47, 0, 0, time.UTC)
	acts := newActs(&fakeFetcher{}, fRepo, eRepo, now)
	for poll := range 3 {
		out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{
			APIFixture: observed, WorkflowID: fmt.Sprintf("postponed-%d", poll),
			ObservedAt: now.Add(time.Duration(poll) * 30 * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.ProviderIntegrity.Policy != providerintegrity.PolicyTrusted || out.ProviderIntegrity.Anomalous() {
			t.Fatalf("poll %d ProviderIntegrity = %+v, want trusted", poll, out.ProviderIntegrity)
		}
		if out.Completed || out.FeedAction != "" || len(eRepo.events) != 0 {
			t.Fatalf("poll %d output = %+v, want no lifecycle or public mutation", poll, out)
		}
	}
	refreshed, err := fRepo.Get(context.Background(), stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.HomeScore == nil || *refreshed.HomeScore != 0 ||
		refreshed.AwayScore == nil || *refreshed.AwayScore != 0 ||
		refreshed.State != fixture.StateActive || refreshed.TerminalObservedAt != nil {
		t.Fatalf("refreshed fixture = %+v, want retained 0–0 and unchanged active lifecycle", refreshed)
	}
}

// TestProviderFixtureComparison_AnyStoredEventBlocksEmptyScoreboard proves
// that pending, anonymous, and removed events cannot disappear from the
// deferred exception's evidence merely because they are not confirmed keys.
func TestProviderFixtureComparison_AnyStoredEventBlocksEmptyScoreboard(t *testing.T) {
	for _, state := range []string{"pending", "anonymous", "removed", "confirmed"} {
		t.Run(state, func(t *testing.T) {
			stored, observed := postponedFixturePair(t)
			goal := event.New(stored.ID,
				event.Team{ID: stored.Home.ID, Name: stored.Home.Name},
				event.Player{ID: pi(111), Name: stringp("Scorer")},
				event.TypeGoal, apifootball.DetailNormalGoal, 10, nil, 1, stored.Kickoff,
			)
			switch state {
			case "anonymous":
				goal.Player.ID, goal.Player.Name = nil, nil
			case "removed":
				goal.Removed, goal.DownstreamTriggered = true, true
			case "confirmed":
				goal.DebounceCount, goal.DownstreamTriggered = 3, true
			}
			comparison := providerFixtureComparison(stored, []*event.Event{goal}, observed, nil)
			if !comparison.Stored.HasEvents || comparison.Observed.HasEvents ||
				(state != "confirmed" && len(comparison.ConfirmedEvents) != 0) {
				t.Fatalf("comparison = %+v, want history outside confirmed keys retained as evidence", comparison)
			}
			if verdict := providerintegrity.AssessFixture(comparison); !verdict.Anomalous() {
				t.Fatalf("verdict = %+v, want event history to preserve the score-erasure warning", verdict)
			}
		})
	}
}

// TestProviderFixtureComparison_UntrackedObservationBlocksEmptyScoreboard
// keeps a provider yellow card as evidence without changing discovery scope.
func TestProviderFixtureComparison_UntrackedObservationBlocksEmptyScoreboard(t *testing.T) {
	stored, observed := postponedFixturePair(t)
	card := mkAPIGoal(stored.Home.ID, 111, 10)
	card.Type, card.Detail = apifootball.EventTypeCard, apifootball.DetailYellowCard
	observed.Events = []apifootball.APIFixtureEvent{card}
	comparison := providerFixtureComparison(stored, nil, observed, nil)
	if !comparison.Observed.HasEvents || comparison.Stored.HasEvents || len(comparison.ObservedEvents) != 0 {
		t.Fatalf("comparison = %+v, want raw event evidence without a trackable event", comparison)
	}
	if verdict := providerintegrity.AssessFixture(comparison); !verdict.Anomalous() {
		t.Fatalf("verdict = %+v, want raw event evidence to preserve the score-erasure warning", verdict)
	}
}

// postponedFixturePair builds the retained/fresh Cincinnati facts from the
// September 8 audit; no live provider request or production write is needed.
func postponedFixturePair(t *testing.T) (*fixture.Fixture, apifootball.APIFixture) {
	t.Helper()
	kickoff := time.Date(2026, 9, 5, 23, 30, 0, 0, time.UTC)
	stored := fixture.New(1490439,
		fixture.APIStatus{Short: apifootball.StatusPostponed, Long: "Match Postponed"},
		kickoff, fixture.Team{ID: 2242, Name: "FC Cincinnati"}, fixture.Team{ID: 1615, Name: "DC United"},
		fixture.League{ID: 253, Name: "MLS", Season: 2026},
	)
	if err := stored.Activate(kickoff.Add(-5 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored.HomeScore, stored.AwayScore = pi(0), pi(0)
	observed := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID: stored.ID, Date: kickoff,
			Status: apifootball.APIFixtureStatus{Short: apifootball.StatusPostponed, Long: "Match Postponed"},
		},
		League: apifootball.APIFixtureLeague{ID: 253, Name: "MLS", Season: 2026},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: 2242, Name: "FC Cincinnati"},
			Away: apifootball.APIFixtureTeam{ID: 1615, Name: "DC United"},
		},
		Events: []apifootball.APIFixtureEvent{},
	}
	return stored, observed
}
