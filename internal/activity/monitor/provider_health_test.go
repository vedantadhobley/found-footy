// provider_health_test.go checks exact fetch coverage and pre-write collection
// without allowing the health path to change canonical fixture or event data.
package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// captureHealthStore exposes the activity's boundary, not persistence behavior.
type captureHealthStore struct {
	poll     providerintegrity.HealthPoll
	failures []providerintegrity.FetchFailureEvidence
}

// Observe captures a typed logical poll for coverage assertions.
func (s *captureHealthStore) Observe(_ context.Context, poll providerintegrity.HealthPoll) (providerintegrity.HealthResult, error) {
	s.poll = poll
	return providerintegrity.HealthResult{}, nil
}

// RecordFetchFailure captures failed attempts before Temporal retries occur.
func (s *captureHealthStore) RecordFetchFailure(_ context.Context, evidence providerintegrity.FetchFailureEvidence) error {
	s.failures = append(s.failures, evidence)
	return nil
}

// TestCollectProviderHealthIncludesFailureCoverage checks one usable response,
// one rejected chunk, and a missing local result without changing stored fields.
func TestCollectProviderHealthIncludesFailureCoverage(t *testing.T) {
	ctx := context.Background()
	stored, observed := postponedFixturePair(t)
	fixtures := newFakeFixtureRepo()
	require.NoError(t, fixtures.Upsert(ctx, stored))
	store := &captureHealthStore{}
	acts := newActs(&fakeFetcher{}, fixtures, newFakeEventRepo(), time.Now())
	acts.ProviderHealth = store
	_, err := acts.CollectProviderHealth(ctx, CollectProviderHealthInput{
		PollID: "poll", ObservedAt: time.Now(), RequestedIDs: []int64{stored.ID, 2, 3},
		Fetch: FetchLiveFixturesOutput{Fixtures: []apifootball.APIFixture{observed}, FailedIDs: []int64{2},
			Failures: []ProviderFetchFailure{{IDs: []int64{2}, Kind: ProviderFetchFailureContract, ContractReason: "events_missing"}}},
	})
	require.NoError(t, err)
	require.Len(t, store.poll.Observations, 3)
	require.Equal(t, providerintegrity.AvailabilityUsable, store.poll.Observations[0].Availability)
	require.Equal(t, providerintegrity.AvailabilityContract, store.poll.Observations[1].Availability)
	require.Equal(t, providerintegrity.AvailabilityUnobserved, store.poll.Observations[2].Availability)
	actual, err := fixtures.Get(ctx, stored.ID)
	require.NoError(t, err)
	require.Equal(t, stored, actual)
	require.Empty(t, fixtures.audits)
	acts.EventRepo = &unavailableProviderHistory{fakeEventRepo: newFakeEventRepo(), err: errors.New("history unavailable")}
	_, err = acts.CollectProviderHealth(ctx, CollectProviderHealthInput{PollID: "second", ObservedAt: time.Now(), RequestedIDs: []int64{stored.ID}, Fetch: FetchLiveFixturesOutput{Fixtures: []apifootball.APIFixture{observed}}})
	require.NoError(t, err)
	require.Equal(t, providerintegrity.AvailabilityUnobserved, store.poll.Observations[0].Availability)
	require.Equal(t, "event_history_read_failed", store.poll.Observations[0].FailureReason)
}

// TestFetchLiveFixturesRetainsFailedAttempt verifies failure evidence is written
// before the retryable Temporal error boundary, not only after exhaustion.
func TestFetchLiveFixturesRetainsFailedAttempt(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	store := &captureHealthStore{}
	acts := &Activities{APIFootball: &fakeFetcher{err: errors.New("upstream unavailable")}, ProviderHealth: store}
	env.RegisterActivity(acts)
	_, err := env.ExecuteActivity(acts.FetchLiveFixtures, FetchLiveFixturesInput{IDs: []int64{1, 2}, PollID: "poll", ObservedAt: time.Now()})
	require.Error(t, err)
	require.Len(t, store.failures, 1)
	require.Equal(t, 1, store.failures[0].Attempt)
	require.Len(t, store.failures[0].Failures, 2)
	require.Equal(t, providerintegrity.AvailabilityUnobserved, store.failures[0].Failures[0].Availability)
}
