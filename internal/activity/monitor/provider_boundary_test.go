// provider_boundary_test.go characterizes unsafe pre-enforcement ordering. These
// assertions document current gaps and must invert when guarded writes ship.
package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// TestProviderBoundary_CurrentRefreshPrecedesAssessment proves that even an
// event-history read failure leaves a regressed fixture snapshot committed.
func TestProviderBoundary_CurrentRefreshPrecedesAssessment(t *testing.T) {
	kickoff := time.Date(2026, 9, 16, 19, 0, 0, 0, time.UTC)
	f := mkActiveN4Fixture(1570385, kickoff, 65, 4, 2)
	f.APIStatus = fixture.APIStatus{Short: "2h", Long: "Second Half"}
	fixtures := newFakeFixtureRepo()
	require.NoError(t, fixtures.Upsert(context.Background(), f))
	failure := errors.New("event history unavailable")
	events := &unavailableProviderHistory{fakeEventRepo: newFakeEventRepo(), err: failure}
	now := kickoff.Add(2 * time.Hour)
	acts := newActs(&fakeFetcher{}, fixtures, events, now)
	api := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: f.ID, Status: apifootball.APIFixtureStatus{Short: "2h", Elapsed: pi(46)}},
		Goals:   apifootball.APIFixtureGoals{Home: pi(0), Away: pi(0)},
	}
	out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: api, WorkflowID: "boundary", ObservedAt: now})
	require.ErrorIs(t, err, failure)
	require.False(t, out.ProviderIntegrity.Policy.Valid(), "assessment was never reached")
	stored, err := fixtures.Get(context.Background(), f.ID)
	require.NoError(t, err)
	require.Equal(t, 46, *stored.APIElapsed, "current behavior, not the target safety contract")
	require.Equal(t, 0, *stored.HomeScore)
	require.True(t, stored.LastPolledAt.Equal(now))
}

// TestProviderBoundary_CurrentFrozenClockRemainsTrusted reproduces the missing
// freshness detector through real reconciliation over forty scheduled polls.
func TestProviderBoundary_CurrentFrozenClockRemainsTrusted(t *testing.T) {
	kickoff := time.Date(2026, 9, 16, 19, 0, 0, 0, time.UTC)
	start := kickoff.Add(2 * time.Hour)
	f := mkActiveN4Fixture(1570385, kickoff, 65, 4, 2)
	f.APIStatus = fixture.APIStatus{Short: "2h", Long: "Second Half"}
	f.UpdateResult(nil, nil)
	fixtures := newFakeFixtureRepo()
	require.NoError(t, fixtures.Upsert(context.Background(), f))
	api := apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{ID: f.ID, Status: apifootball.APIFixtureStatus{Short: "2h", Long: "Second Half", Elapsed: pi(65)}},
		League:  apifootball.APIFixtureLeague{ID: f.League.ID, Name: f.League.Name, Season: f.League.Season},
		Teams: apifootball.APIFixtureTeams{
			Home: apifootball.APIFixtureTeam{ID: f.Home.ID, Name: f.Home.Name},
			Away: apifootball.APIFixtureTeam{ID: f.Away.ID, Name: f.Away.Name},
		},
		Goals: apifootball.APIFixtureGoals{Home: pi(4), Away: pi(2)},
	}
	for i := 1; i <= 40; i++ {
		at := start.Add(time.Duration(i) * 30 * time.Second)
		acts := newActs(&fakeFetcher{}, fixtures, newFakeEventRepo(), at)
		out, err := acts.ReconcileFixture(context.Background(), ReconcileFixtureInput{APIFixture: api, WorkflowID: at.String(), ObservedAt: at})
		require.NoError(t, err)
		require.Equal(t, providerintegrity.PolicyTrusted, out.ProviderIntegrity.Policy)
		require.Equal(t, FixtureFeedNone, out.FeedAction)
	}
	stored, err := fixtures.Get(context.Background(), f.ID)
	require.NoError(t, err)
	require.Equal(t, 65, *stored.APIElapsed)
	require.True(t, stored.LastPolledAt.Equal(start.Add(20*time.Minute)))
}

// unavailableProviderHistory fails the read required to calculate a verdict.
type unavailableProviderHistory struct {
	*fakeEventRepo
	err error
}

// ListAllByFixture fails after the current implementation's fixture refresh.
func (r *unavailableProviderHistory) ListAllByFixture(context.Context, int64) ([]*event.Event, error) {
	return nil, r.err
}
