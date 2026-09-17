// reconcile_guarded.go plans active provider mutations without writing, then
// delegates the whole fixture/event commit to the atomic policy boundary.
package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/contract/fixturepresentation"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
)

// reconcileGuarded performs every read needed to build an immutable plan. The
// repository rejects it if the health generation or fixture baseline changes
// before the transaction acquires its locks.
func (a *Activities) reconcileGuarded(ctx context.Context, in ReconcileFixtureInput) (ReconcileFixtureOutput, error) {
	out := ReconcileFixtureOutput{FixtureID: in.APIFixture.Fixture.ID}
	if in.PollID == "" || in.ObservedAt.IsZero() {
		return out, fmt.Errorf("monitor.ReconcileFixture: guarded observation token is required")
	}
	now := a.now()
	observedAt := in.ObservedAt.UTC()
	stored, err := a.FixtureRepo.Get(ctx, in.APIFixture.Fixture.ID)
	if err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: get fixture: %w", err)
	}
	allEvents, err := a.EventRepo.ListAllByFixture(ctx, stored.ID)
	if err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: list fixture event history: %w", err)
	}

	candidate := *stored
	scoreInventory := newScoreEventInventory(in.APIFixture)
	providerParity := scoreInventory.terminalProviderScoreEventParity(
		in.APIFixture.Fixture.Status.Short,
		in.APIFixture.Teams.Home.ID,
		in.APIFixture.Teams.Away.ID,
	)
	candidate.UpdateFromPoll(
		fixture.APIStatus{Short: in.APIFixture.Fixture.Status.Short, Long: in.APIFixture.Fixture.Status.Long},
		in.APIFixture.Fixture.Status.Elapsed,
		in.APIFixture.Fixture.Status.Extra,
		in.APIFixture.Goals.Home,
		in.APIFixture.Goals.Away,
		observedAt,
	)
	updateFixtureMetadataFromAPI(&candidate, in.APIFixture)
	candidate.UpdatePenalty(in.APIFixture.Score.Penalty.Home, in.APIFixture.Score.Penalty.Away)
	candidate.UpdateResult(in.APIFixture.Teams.Home.Winner, in.APIFixture.Teams.Away.Winner)

	pgByKey := make(map[string]*event.Event, len(allEvents))
	byID := make(map[uuid.UUID]*event.Event, len(allEvents))
	for _, storedEvent := range allEvents {
		byID[storedEvent.ID] = storedEvent
		if !storedEvent.Removed {
			pgByKey[storedEvent.NaturalKey] = storedEvent
		}
	}
	incompleteGoalTeams := map[int]bool{
		in.APIFixture.Teams.Home.ID: scoreInventory.scoreRequiresMissingGoal(in.APIFixture.Teams.Home.ID),
		in.APIFixture.Teams.Away.ID: scoreInventory.scoreRequiresMissingGoal(in.APIFixture.Teams.Away.ID),
	}
	sequences, err := assignEventSequences(in.APIFixture.Events, allEvents, incompleteGoalTeams)
	if err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: assign event identities: %w", err)
	}
	out.ProviderIntegrity = providerintegrity.AssessFixture(
		providerFixtureComparison(stored, allEvents, in.APIFixture, sequences),
	)
	plan := providerintegrity.ActiveMutationPlan{
		Guard: providerintegrity.ObservationGuard{
			ObservationID: in.PollID, ObservedAt: observedAt,
			ExpectedGeneration: in.HealthGeneration, RequireHealthReceipt: true,
		},
		Fixture: &candidate, BaselineExists: true,
		BaselineLastPolled: cloneOptionalTime(stored.LastPolledAt),
		LocalPolicy:        out.ProviderIntegrity.Policy, WorkflowID: in.WorkflowID,
		Completion: providerintegrity.CompletionPlan{
			TerminalBefore: now.Add(-a.TerminalGracePeriod), CompletedAt: now,
			GraceSeconds:             int64(a.TerminalGracePeriod / time.Second),
			ProviderScoreEventParity: providerParity,
		},
	}

	apiKeys := make(map[string]struct{}, len(in.APIFixture.Events))
	for apiIndex, apiEvent := range in.APIFixture.Events {
		domainType := trackableType(apiEvent)
		if domainType == "" {
			continue
		}
		sequence, ok := sequences[apiIndex]
		if !ok {
			out.Errors = append(out.Errors, fmt.Sprintf("event identity missing api_index=%d", apiIndex))
			continue
		}
		fresh, key, buildErr := a.buildDomainEvent(&candidate, apiEvent, domainType, sequence, now)
		if buildErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("build event: %v", buildErr))
			continue
		}
		apiKeys[key] = struct{}{}
		if existing, exists := pgByKey[key]; exists {
			if !fresh.Player.Known() {
				continue
			}
			audit, auditErr := eventStableAudit(existing.ID, candidate.ID, fresh)
			if auditErr != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("build stable audit event=%s: %v", key, auditErr))
				continue
			}
			plan.Presences = append(plan.Presences, providerintegrity.PlannedEventPresence{EventID: existing.ID, Audit: audit})
			if existing.MutableFieldsChanged(fresh) {
				plan.Updates = append(plan.Updates, providerintegrity.PlannedEventUpdate{EventID: existing.ID, Fresh: fresh})
			}
			continue
		}
		insert := providerintegrity.PlannedEventInsert{Event: fresh}
		if fresh.Player.Known() {
			audit, auditErr := eventDetectedAudit(fresh.ID, candidate.ID, fresh)
			if auditErr != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("build detected audit event=%s: %v", key, auditErr))
				continue
			}
			insert.Audit = &audit
		}
		plan.Inserts = append(plan.Inserts, insert)
		byID[fresh.ID] = fresh
	}
	for key, storedEvent := range pgByKey {
		if _, present := apiKeys[key]; present {
			continue
		}
		if !storedEvent.Player.Known() {
			plan.UnknownDeletes = append(plan.UnknownDeletes, storedEvent.ID)
			continue
		}
		if storedEvent.Type == event.TypeGoal && scoreInventory.scoreRequiresMissingGoal(storedEvent.Team.ID) {
			out.GoalAbsencesHeld = append(out.GoalAbsencesHeld, key)
			continue
		}
		audit, auditErr := eventRemovedAudit(storedEvent.ID, candidate.ID, now)
		if auditErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("build removal audit event=%s: %v", key, auditErr))
			continue
		}
		plan.Absences = append(plan.Absences, providerintegrity.PlannedEventAbsence{EventID: storedEvent.ID, Audit: audit})
	}

	result, err := a.ProviderMutations.ApplyActive(ctx, plan)
	if err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: guarded commit: %w", err)
	}
	out.MutationPolicy = result.Policy
	out.MutationDecision = result.Decision
	out.MutationApplied = result.Applied
	out.MutationStale = result.Stale
	if result.Stale {
		return out, nil
	}
	out.NewEventsDetected = len(result.InsertedEventIDs)
	out.NewNamedEventIDs = result.NewNamedEventIDs
	out.EventsRemovedIDs = result.RemovedEventIDs
	out.UnknownDropped = result.UnknownDropped
	out.Completed = result.Completed
	for _, id := range result.StableEventIDs {
		ev := byID[id]
		if ev == nil {
			ev, err = a.EventRepo.Get(ctx, id)
		}
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("load stable event=%s: %v", id, err))
			continue
		}
		out.EventsBecameStable = append(out.EventsBecameStable, ev.NaturalKey)
		if spawnErr := a.registerAndSpawnEvent(ctx, ev, ev, candidate.ID); spawnErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("spawn event=%s: %v", ev.NaturalKey, spawnErr))
		}
	}
	for _, id := range result.RemovedEventIDs {
		if ev := byID[id]; ev != nil {
			out.EventsRemoved = append(out.EventsRemoved, ev.NaturalKey)
		}
	}

	// Recovery is not provider-derived mutation. It remains available while an
	// observation is rejected and is safe after the guarded transaction closes.
	if awaiting, recoveryErr := a.EventRepo.EventsAwaitingDiscovery(ctx, candidate.ID); recoveryErr != nil {
		out.Errors = append(out.Errors, fmt.Sprintf("awaiting-discovery: %v", recoveryErr))
	} else {
		for _, ev := range awaiting {
			if spawnErr := a.registerAndSpawnEvent(ctx, ev, ev, candidate.ID); spawnErr != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("recover event=%s: %v", ev.ID, spawnErr))
			}
		}
	}
	classifyGuardedFeed(&out, result)
	return out, nil
}

// classifyGuardedFeed uses committed effects so activity retries retain the
// original notification even if their planning reads see newer fixture data.
func classifyGuardedFeed(out *ReconcileFixtureOutput, result providerintegrity.MutationResult) {
	if result.Fixture == nil {
		return
	}
	out.Presentation = fixturepresentation.From(result.Fixture.APIStatus, result.Fixture.APIElapsed, result.Fixture.APIExtra)
	if result.EventChanged || result.Completed || result.FixtureStructuralChanged {
		out.markUpdate()
		return
	}
	if result.FixturePresentationChanged {
		out.markStatus()
	}
}

func cloneOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
