// provider_health.go collects provider health before reconciliation fan-out.
// Fetch retries, failed chunks, and local assessment failures remain distinct.
package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// CollectProviderHealthInput is one finalized logical poll, including failures.
type CollectProviderHealthInput struct {
	PollID       string
	ObservedAt   time.Time
	RequestedIDs []int64
	Fetch        FetchLiveFixturesOutput
	FetchFailed  bool
}

// CollectProviderHealth reads pre-reconciliation facts and commits a bounded
// assessment. Canonical fixture/events remain untouched by this activity.
func (a *Activities) CollectProviderHealth(ctx context.Context, in CollectProviderHealthInput) (providerintegrity.HealthResult, error) {
	if a.ProviderHealth == nil {
		return providerintegrity.HealthResult{}, fmt.Errorf("provider health store is required")
	}
	byID := make(map[int64]apifootball.APIFixture, len(in.Fetch.Fixtures))
	for _, f := range in.Fetch.Fixtures {
		if _, exists := byID[f.Fixture.ID]; exists {
			return providerintegrity.HealthResult{}, fmt.Errorf("duplicate fixture in health input")
		}
		byID[f.Fixture.ID] = f
	}
	failures := make(map[int64]providerintegrity.HealthObservation)
	for _, failure := range healthFetchFailures(in.RequestedIDs, in.Fetch, in.FetchFailed) {
		failures[failure.FixtureID] = failure
	}
	poll := providerintegrity.HealthPoll{ID: in.PollID, At: in.ObservedAt}
	for _, id := range in.RequestedIDs {
		observation, failed := failures[id]
		if !failed {
			if f, found := byID[id]; found {
				observation = a.healthComparison(ctx, f, in.ObservedAt)
			} else {
				observation = providerintegrity.HealthObservation{FixtureID: id, Availability: providerintegrity.AvailabilityUnobserved, FailureReason: "missing_fetch_result"}
			}
		}
		poll.Observations = append(poll.Observations, observation)
	}
	return a.ProviderHealth.Observe(ctx, poll)
}

// healthComparison reuses the reconciliation matcher; health does not invent
// an alternative event identity rule. Local read failures are not vendor faults.
func (a *Activities) healthComparison(ctx context.Context, f apifootball.APIFixture, observedAt time.Time) providerintegrity.HealthObservation {
	out := providerintegrity.HealthObservation{FixtureID: f.Fixture.ID, Availability: providerintegrity.AvailabilityUnobserved}
	stored, err := a.FixtureRepo.Get(ctx, f.Fixture.ID)
	if err != nil {
		out.FailureReason = "fixture_read_failed"
		return out
	}
	if stored.LastPolledAt != nil && stored.LastPolledAt.After(observedAt) {
		out.FailureReason = "newer_canonical_snapshot"
		return out
	}
	out.BaselineLastPolled = cloneOptionalTime(stored.LastPolledAt)
	events, err := a.EventRepo.ListAllByFixture(ctx, f.Fixture.ID)
	if err != nil {
		out.FailureReason = "event_history_read_failed"
		return out
	}
	inventory := newScoreEventInventory(f)
	sequences, err := assignEventSequences(f.Events, events, map[int]bool{
		f.Teams.Home.ID: inventory.scoreRequiresMissingGoal(f.Teams.Home.ID),
		f.Teams.Away.ID: inventory.scoreRequiresMissingGoal(f.Teams.Away.ID),
	})
	if err != nil {
		out.FailureReason = "event_matching_failed"
		return out
	}
	out.Availability = providerintegrity.AvailabilityUsable
	out.Comparison = providerFixtureComparison(stored, events, f, sequences)
	return out
}

// healthFetchFailures expands exact chunk coverage without storing raw errors.
// An unclassified catastrophic fetch affects every requested ID not returned.
func healthFetchFailures(ids []int64, out FetchLiveFixturesOutput, catastrophic bool) []providerintegrity.HealthObservation {
	failed := make(map[int64]providerintegrity.HealthObservation)
	for _, chunk := range out.Failures {
		availability := providerintegrity.AvailabilityTransport
		if chunk.Kind == ProviderFetchFailureContract {
			availability = providerintegrity.AvailabilityContract
		}
		for _, id := range chunk.IDs {
			failed[id] = providerintegrity.HealthObservation{FixtureID: id, Availability: availability, FailureReason: chunk.ContractReason}
		}
	}
	for _, id := range out.FailedIDs {
		if _, exists := failed[id]; !exists {
			failed[id] = providerintegrity.HealthObservation{FixtureID: id, Availability: providerintegrity.AvailabilityTransport}
		}
	}
	if catastrophic {
		returned := make(map[int64]bool)
		for _, f := range out.Fixtures {
			returned[f.Fixture.ID] = true
		}
		for _, id := range ids {
			if _, exists := failed[id]; !exists && !returned[id] {
				failed[id] = providerintegrity.HealthObservation{FixtureID: id, Availability: providerintegrity.AvailabilityUnobserved, FailureReason: "fetch_failed_unclassified"}
			}
		}
	}
	result := make([]providerintegrity.HealthObservation, 0, len(failed))
	for _, id := range ids {
		if failure, ok := failed[id]; ok {
			result = append(result, failure)
		}
	}
	return result
}
