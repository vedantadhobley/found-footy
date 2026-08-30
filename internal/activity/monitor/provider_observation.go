// provider_observation.go translates stored domain state and one validated
// API-Football payload into provider-independent integrity facts.
package monitor

import (
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

func providerFixtureComparison(
	stored *fixture.Fixture,
	storedEvents []*event.Event,
	observed apifootball.APIFixture,
	eventSequences map[int]int,
) providerintegrity.FixtureComparison {
	comparison := providerintegrity.FixtureComparison{
		Stored: providerFixtureFacts(stored),
		Observed: providerintegrity.FixtureFacts{
			FixtureID:  observed.Fixture.ID,
			HomeID:     observed.Teams.Home.ID,
			AwayID:     observed.Teams.Away.ID,
			LeagueID:   observed.League.ID,
			HomeName:   observed.Teams.Home.Name,
			AwayName:   observed.Teams.Away.Name,
			LeagueName: observed.League.Name,
			Status:     string(observed.Fixture.Status.Short),
			Terminal: fixture.APIStatus{
				Short: observed.Fixture.Status.Short,
				Long:  observed.Fixture.Status.Long,
			}.Terminal(),
			Elapsed:   observed.Fixture.Status.Elapsed,
			Extra:     observed.Fixture.Status.Extra,
			HomeScore: observed.Goals.Home,
			AwayScore: observed.Goals.Away,
		},
	}

	for _, storedEvent := range storedEvents {
		if storedEvent.Removed || !storedEvent.DownstreamTriggered {
			continue
		}
		comparison.ConfirmedEvents = append(comparison.ConfirmedEvents,
			providerEventFact(
				storedEvent.NaturalKey,
				storedEvent.Team.ID,
				storedEvent.Type,
				storedEvent.Minute,
				storedEvent.Extra,
			))
	}
	for index, observedEvent := range observed.Events {
		domainType := trackableType(observedEvent)
		if domainType == "" {
			continue
		}
		sequence, exists := eventSequences[index]
		if !exists {
			continue
		}
		comparison.ObservedEvents = append(comparison.ObservedEvents,
			providerEventFact(
				event.ComposeNaturalKey(observedEvent.Team.ID, observedEvent.Player.ID, domainType, sequence),
				observedEvent.Team.ID,
				domainType,
				observedEvent.Time.Elapsed,
				observedEvent.Time.Extra,
			))
	}
	return comparison
}

func providerFixtureFacts(stored *fixture.Fixture) providerintegrity.FixtureFacts {
	return providerintegrity.FixtureFacts{
		FixtureID:  stored.ID,
		HomeID:     stored.Home.ID,
		AwayID:     stored.Away.ID,
		LeagueID:   stored.League.ID,
		HomeName:   stored.Home.Name,
		AwayName:   stored.Away.Name,
		LeagueName: stored.League.Name,
		Status:     string(stored.APIStatus.Short),
		Terminal:   stored.APIStatus.Terminal(),
		Elapsed:    stored.APIElapsed,
		Extra:      stored.APIExtra,
		HomeScore:  stored.HomeScore,
		AwayScore:  stored.AwayScore,
	}
}

func providerEventFact(
	key string,
	teamID int,
	eventType event.Type,
	minute int,
	extra *int,
) providerintegrity.EventFact {
	return providerintegrity.EventFact{
		Key:    key,
		TeamID: teamID,
		Type:   string(eventType),
		Minute: minute,
		Extra:  extra,
	}
}
