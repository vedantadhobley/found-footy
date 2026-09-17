// provider_mutation_repo_test.go proves health policy and canonical writes share
// one retry-safe transaction against disposable PostgreSQL.
package pg_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// TestProviderMutationRetryPreservesNotificationEffects models a response lost
// after commit. Replanning against the updated fixture must still return the
// original clock/status or structural notification from the database receipt.
func TestProviderMutationRetryPreservesNotificationEffects(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := time.Date(2026, 9, 17, 3, 0, 0, 0, time.UTC)
	for i, tc := range []struct {
		name                     string
		mutate                   func(*fixture.Fixture)
		structural, presentation bool
	}{
		{"unchanged", func(*fixture.Fixture) {}, false, false},
		{"clock", func(f *fixture.Fixture) { v := 45; f.APIElapsed = &v }, false, true},
		{"halftime", func(f *fixture.Fixture) {
			f.APIStatus = fixture.APIStatus{Short: apifootball.StatusHalftime, Long: "Half Time"}
		}, false, true},
		{"score", func(f *fixture.Fixture) { v := 2; f.HomeScore = &v }, true, false},
		{"finished", func(f *fixture.Fixture) {
			f.APIStatus = fixture.APIStatus{Short: apifootball.StatusFullTime, Long: "Match Finished"}
		}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := base.Add(time.Duration(i) * time.Minute)
			f := guardedActiveFixture(t, 9820+int64(i), at, 44, 1, 0)
			f.APIStatus = fixture.APIStatus{Short: apifootball.StatusFirstHalf, Long: "First Half"}
			require.NoError(t, fixtures.Insert(ctx, f))
			candidate := *f
			tc.mutate(&candidate)
			observedAt := at.Add(30 * time.Second)
			candidate.LastPolledAt = &observedAt
			poll := providerintegrity.HealthPoll{ID: fmt.Sprintf("notification-%d", i), At: observedAt,
				Observations: []providerintegrity.HealthObservation{{
					FixtureID: f.ID, Availability: providerintegrity.AvailabilityUsable, BaselineLastPolled: copyTestTime(f.LastPolledAt),
					Comparison: providerintegrity.FixtureComparison{Stored: guardedFacts(f), Observed: guardedFacts(&candidate)},
				}},
			}
			health, err := pg.NewProviderHealthRepo(pool).Observe(ctx, poll)
			require.NoError(t, err)
			plan := providerintegrity.ActiveMutationPlan{
				Guard:   providerintegrity.ObservationGuard{ObservationID: poll.ID, ObservedAt: poll.At, ExpectedGeneration: health.Generation, RequireHealthReceipt: true},
				Fixture: &candidate, BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt),
				LocalPolicy: providerintegrity.PolicyTrusted, WorkflowID: poll.ID,
			}
			first, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyActive(ctx, plan)
			require.NoError(t, err)
			require.True(t, first.Applied)
			require.Equal(t, tc.structural, first.FixtureStructuralChanged)
			require.Equal(t, tc.presentation, first.FixturePresentationChanged)
			landed, err := fixtures.Get(ctx, f.ID)
			require.NoError(t, err)
			plan.Fixture, plan.BaselineLastPolled = landed, copyTestTime(landed.LastPolledAt)
			retry, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyActive(ctx, plan)
			require.NoError(t, err)
			require.Equal(t, first.FixtureChanged, retry.FixtureChanged)
			require.Equal(t, first.FixtureStructuralChanged, retry.FixtureStructuralChanged)
			require.Equal(t, first.FixturePresentationChanged, retry.FixturePresentationChanged)
		})
	}
}

// TestProviderMutationPositiveOnlyBlocksAugustRegression reproduces the first
// bad batch: score/event regressions are retained while positive additions land.
func TestProviderMutationPositiveOnlyBlocksAugustRegression(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := time.Date(2026, 9, 16, 21, 0, 0, 0, time.UTC)
	f := guardedActiveFixture(t, 9801, base, 65, 4, 2)
	require.NoError(t, fixtures.Insert(ctx, f))
	events := pg.NewEventRepo(pool)
	old := guardedEvent(9801, 40, "Old Goal", base)
	require.NoError(t, events.Insert(ctx, old, "seed-1"))
	_, _, err := events.RegisterEventPresence(ctx, old.ID, "seed-2")
	require.NoError(t, err)
	_, stable, err := events.RegisterEventPresence(ctx, old.ID, "seed-3")
	require.NoError(t, err)
	require.True(t, stable)

	badAt := base.Add(30 * time.Second)
	storedFacts := guardedFacts(f)
	observedFacts := storedFacts
	zero, minute := 0, 46
	observedFacts.HomeScore, observedFacts.AwayScore = &zero, &zero
	observedFacts.Status, observedFacts.Elapsed = "ns", &minute
	healthPoll := providerintegrity.HealthPoll{ID: "august-bad-1", At: badAt, Observations: []providerintegrity.HealthObservation{{
		FixtureID: f.ID, Availability: providerintegrity.AvailabilityUsable, BaselineLastPolled: copyTestTime(f.LastPolledAt),
		Comparison: providerintegrity.FixtureComparison{
			Stored: storedFacts, Observed: observedFacts,
			ConfirmedEvents: []providerintegrity.EventFact{{Key: old.NaturalKey, TeamID: old.Team.ID, PlayerID: old.Player.ID, Type: string(old.Type), Detail: string(old.Detail), Minute: old.Minute, DebounceCount: 3}},
		},
	}}}
	health, err := pg.NewProviderHealthRepo(pool).Observe(ctx, healthPoll)
	require.NoError(t, err)
	require.Equal(t, []int64{f.ID}, health.RestrictedIDs)

	candidate := *f
	candidate.APIStatus = fixture.APIStatus{Short: apifootball.StatusNotStarted, Long: "Not Started"}
	candidate.APIElapsed, candidate.HomeScore, candidate.AwayScore = &minute, &zero, &zero
	candidate.LastPolledAt = &badAt
	newEvent := guardedEvent(f.ID, 42, "New Goal", badAt)
	detected := guardedDetectedAudit(t, newEvent)
	removed := guardedRemovedAudit(t, old, badAt)
	result, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyActive(ctx, providerintegrity.ActiveMutationPlan{
		Guard:   providerintegrity.ObservationGuard{ObservationID: healthPoll.ID, ObservedAt: badAt, ExpectedGeneration: health.Generation, RequireHealthReceipt: true},
		Fixture: &candidate, BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt),
		LocalPolicy: providerintegrity.PolicyPositiveOnly, WorkflowID: healthPoll.ID,
		Inserts:  []providerintegrity.PlannedEventInsert{{Event: newEvent, Audit: &detected}},
		Absences: []providerintegrity.PlannedEventAbsence{{EventID: old.ID, Audit: removed}},
	})
	require.NoError(t, err)
	require.Equal(t, providerintegrity.PolicyPositiveOnly, result.Policy)
	require.Equal(t, []uuid.UUID{newEvent.ID}, result.InsertedEventIDs)
	require.Empty(t, result.RemovedEventIDs)
	stored, err := fixtures.Get(ctx, f.ID)
	require.NoError(t, err)
	require.Equal(t, 4, *stored.HomeScore)
	require.Equal(t, 2, *stored.AwayScore)
	require.Equal(t, apifootball.StatusSecondHalf, stored.APIStatus.Short)
	retained, err := events.Get(ctx, old.ID)
	require.NoError(t, err)
	require.False(t, retained.Removed)
	require.Equal(t, 3, retained.DebounceCount)
	_, err = events.Get(ctx, newEvent.ID)
	require.NoError(t, err)

	// Lost activity response: retry returns the exact effects without a second
	// vote or duplicate insert, even though the canonical baseline has changed.
	retry, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyActive(ctx, providerintegrity.ActiveMutationPlan{
		Guard:   providerintegrity.ObservationGuard{ObservationID: healthPoll.ID, ObservedAt: badAt, ExpectedGeneration: health.Generation, RequireHealthReceipt: true},
		Fixture: &candidate, BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt),
		LocalPolicy: providerintegrity.PolicyPositiveOnly, WorkflowID: healthPoll.ID,
	})
	require.NoError(t, err)
	require.Equal(t, result.Applied, retry.Applied)
	require.Equal(t, result.Policy, retry.Policy)
	require.Equal(t, result.InsertedEventIDs, retry.InsertedEventIDs)
	require.Equal(t, result.NewNamedEventIDs, retry.NewNamedEventIDs)
	require.Equal(t, result.EventChanged, retry.EventChanged)
	require.Equal(t, result.Fixture.ID, retry.Fixture.ID)
	require.True(t, result.Fixture.LastPolledAt.Equal(*retry.Fixture.LastPolledAt))
}

// TestProviderMutationRollsBackWholePlan proves a late event-plan failure does
// not leave the earlier fixture snapshot committed.
func TestProviderMutationRollsBackWholePlan(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := time.Date(2026, 9, 16, 22, 0, 0, 0, time.UTC)
	f := guardedActiveFixture(t, 9802, base, 60, 1, 0)
	require.NoError(t, fixtures.Insert(ctx, f))
	observation := healthObservation(f.ID, 60)
	observation.BaselineLastPolled = copyTestTime(f.LastPolledAt)
	poll := healthPoll(20, observation)
	poll.ID, poll.At = "rollback-poll", base.Add(30*time.Second)
	health, err := pg.NewProviderHealthRepo(pool).Observe(ctx, poll)
	require.NoError(t, err)
	candidate := *f
	two := 2
	candidate.HomeScore, candidate.LastPolledAt = &two, &poll.At
	newEvent := guardedEvent(f.ID, 40, "Rollback Goal", poll.At)
	invalid := auditlog.Record{}
	_, err = pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyActive(ctx, providerintegrity.ActiveMutationPlan{
		Guard:   providerintegrity.ObservationGuard{ObservationID: poll.ID, ObservedAt: poll.At, ExpectedGeneration: health.Generation, RequireHealthReceipt: true},
		Fixture: &candidate, BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt),
		LocalPolicy: providerintegrity.PolicyTrusted, WorkflowID: poll.ID,
		Inserts: []providerintegrity.PlannedEventInsert{{Event: newEvent, Audit: &invalid}},
	})
	require.Error(t, err)
	stored, err := fixtures.Get(ctx, f.ID)
	require.NoError(t, err)
	require.Equal(t, 1, *stored.HomeScore)
	require.True(t, stored.LastPolledAt.Equal(*f.LastPolledAt))
	_, err = pg.NewEventRepo(pool).Get(ctx, newEvent.ID)
	require.ErrorIs(t, err, event.ErrNotFound)
	var receipts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM provider_mutation_receipts WHERE observation_id=$1`, poll.ID).Scan(&receipts))
	require.Zero(t, receipts)
}

// TestProviderMutationIngestRespectsFixtureRestriction proves the slower daily
// ingest path cannot undo live canonical facts during a fixture incident.
func TestProviderMutationIngestRespectsFixtureRestriction(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := time.Date(2026, 9, 16, 23, 0, 0, 0, time.UTC)
	f := guardedActiveFixture(t, 9803, base, 70, 3, 1)
	require.NoError(t, fixtures.Insert(ctx, f))

	badAt := base.Add(30 * time.Second)
	storedFacts := guardedFacts(f)
	observedFacts := storedFacts
	zero, minute := 0, 20
	observedFacts.HomeScore, observedFacts.AwayScore = &zero, &zero
	observedFacts.Status, observedFacts.Elapsed = "ns", &minute
	observation := providerintegrity.HealthObservation{
		FixtureID: f.ID, Availability: providerintegrity.AvailabilityUsable,
		BaselineLastPolled: copyTestTime(f.LastPolledAt),
		Comparison:         providerintegrity.FixtureComparison{Stored: storedFacts, Observed: observedFacts},
	}
	_, err := pg.NewProviderHealthRepo(pool).Observe(ctx, providerintegrity.HealthPoll{
		ID: "ingest-regression-health", At: badAt, Observations: []providerintegrity.HealthObservation{observation},
	})
	require.NoError(t, err)

	candidate := *f
	candidate.APIStatus = fixture.APIStatus{Short: apifootball.StatusNotStarted, Long: "Not Started"}
	candidate.APIElapsed, candidate.HomeScore, candidate.AwayScore = &minute, &zero, &zero
	candidate.LastPolledAt = &badAt
	result, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyFixture(ctx, providerintegrity.FixtureMutationPlan{
		Guard: providerintegrity.ObservationGuard{ObservationID: "daily-ingest-1", ObservedAt: badAt},
		Mode:  providerintegrity.MutationModeIngest, Fixture: &candidate,
		BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt),
		LocalPolicy: providerintegrity.PolicyPositiveOnly,
	})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, providerintegrity.PolicyPositiveOnly, result.Policy)
	stored, err := fixtures.Get(ctx, f.ID)
	require.NoError(t, err)
	require.Equal(t, fixture.StateActive, stored.State)
	require.Equal(t, apifootball.StatusSecondHalf, stored.APIStatus.Short)
	require.Equal(t, 3, *stored.HomeScore)
	require.Equal(t, 1, *stored.AwayScore)
	require.True(t, stored.LastPolledAt.Equal(badAt))
}

// TestProviderMutationStagingActivationIsAtomicAndRetrySafe proves the passive
// poll cannot separate an activation from its durable semantic audit.
func TestProviderMutationStagingActivationIsAtomicAndRetrySafe(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	f := makeStaging(9804, base.Add(20*time.Minute))
	require.NoError(t, fixtures.Insert(ctx, f))
	candidate := *f
	minute := 1
	candidate.RecordStagingPoll(fixture.APIStatus{Short: apifootball.StatusFirstHalf, Long: "First Half"}, candidate.Kickoff, base)
	candidate.APIElapsed = &minute
	require.NoError(t, candidate.Activate(base))
	audit := guardedActivatedAudit(t, candidate.ID, base)
	plan := providerintegrity.FixtureMutationPlan{
		Guard: providerintegrity.ObservationGuard{ObservationID: "staging-activation-1", ObservedAt: base},
		Mode:  providerintegrity.MutationModeStaging, Fixture: &candidate,
		BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt),
		LocalPolicy: providerintegrity.PolicyTrusted, Activate: true, ActivationAudit: &audit,
	}
	result, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyFixture(ctx, plan)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, fixture.StateActive, result.Fixture.State)
	retry, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyFixture(ctx, plan)
	require.NoError(t, err)
	require.Equal(t, result.Applied, retry.Applied)
	require.Equal(t, result.Fixture.State, retry.Fixture.State)
	var audits int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM event_log WHERE fixture_id=$1 AND event_type=$2`, f.ID, auditlog.KindFixtureActivated).Scan(&audits))
	require.Equal(t, 1, audits)
}

// TestProviderMutationRejectsOlderFixtureObservation preserves the original
// writer-ordering contract inside the new semantic guard.
func TestProviderMutationRejectsOlderFixtureObservation(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := time.Date(2026, 9, 17, 1, 0, 0, 0, time.UTC)
	f := guardedActiveFixture(t, 9805, base, 60, 1, 0)
	require.NoError(t, fixtures.Insert(ctx, f))
	older := base.Add(-30 * time.Second)
	candidate := *f
	two := 2
	candidate.HomeScore, candidate.LastPolledAt = &two, &older
	result, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyFixture(ctx, providerintegrity.FixtureMutationPlan{
		Guard: providerintegrity.ObservationGuard{ObservationID: "older-ingest-1", ObservedAt: older},
		Mode:  providerintegrity.MutationModeIngest, Fixture: &candidate,
		BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt),
		LocalPolicy: providerintegrity.PolicyTrusted,
	})
	require.NoError(t, err)
	require.True(t, result.Stale)
	require.False(t, result.Applied)
	stored, err := fixtures.Get(ctx, f.ID)
	require.NoError(t, err)
	require.Equal(t, 1, *stored.HomeScore)
	require.True(t, stored.LastPolledAt.Equal(base))
}

// TestProviderMutationScopesMutableEventUpdateByNaturalIdentity proves a fresh
// observation may carry a new in-memory UUID while updating only the locked
// canonical event with the same fixture identity.
func TestProviderMutationScopesMutableEventUpdateByNaturalIdentity(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := time.Date(2026, 9, 17, 2, 0, 0, 0, time.UTC)
	f := guardedActiveFixture(t, 9806, base, 60, 1, 0)
	require.NoError(t, fixtures.Insert(ctx, f))
	events := pg.NewEventRepo(pool)
	storedEvent := guardedEvent(f.ID, 40, "Mutable Goal", base)
	require.NoError(t, events.Insert(ctx, storedEvent, "mutable-seed"))

	at := base.Add(30 * time.Second)
	observation := healthObservation(f.ID, 60)
	observation.BaselineLastPolled = copyTestTime(f.LastPolledAt)
	poll := healthPoll(30, observation)
	poll.ID, poll.At = "mutable-update-health", at
	health, err := pg.NewProviderHealthRepo(pool).Observe(ctx, poll)
	require.NoError(t, err)
	candidate := *f
	candidate.LastPolledAt = &at
	fresh := guardedEvent(f.ID, 40, "Mutable Goal", at)
	fresh.Detail = apifootball.DetailOwnGoal
	result, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyActive(ctx, providerintegrity.ActiveMutationPlan{
		Guard: providerintegrity.ObservationGuard{
			ObservationID: poll.ID, ObservedAt: at, ExpectedGeneration: health.Generation, RequireHealthReceipt: true,
		},
		Fixture: &candidate, BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt),
		LocalPolicy: providerintegrity.PolicyTrusted, WorkflowID: poll.ID,
		Updates: []providerintegrity.PlannedEventUpdate{{EventID: storedEvent.ID, Fresh: fresh}},
	})
	require.NoError(t, err)
	require.True(t, result.EventChanged)
	updated, err := events.Get(ctx, storedEvent.ID)
	require.NoError(t, err)
	require.Equal(t, apifootball.DetailOwnGoal, updated.Detail)
}

func guardedActiveFixture(t *testing.T, id int64, at time.Time, minute, home, away int) *fixture.Fixture {
	t.Helper()
	f := makeStaging(id, at.Add(-2*time.Hour))
	require.NoError(t, f.Activate(at.Add(-2*time.Hour)))
	f.UpdateFromPoll(fixture.APIStatus{Short: apifootball.StatusSecondHalf, Long: "Second Half"}, &minute, nil, &home, &away, at)
	return f
}

func guardedEvent(fixtureID int64, teamID int, name string, at time.Time) *event.Event {
	playerID := teamID * 100
	return event.New(fixtureID, event.Team{ID: teamID, Name: "Team"}, event.Player{ID: &playerID, Name: &name},
		event.TypeGoal, apifootball.DetailNormalGoal, 60, nil, 1, at)
}

func guardedFacts(f *fixture.Fixture) providerintegrity.FixtureFacts {
	return providerintegrity.FixtureFacts{
		FixtureID: f.ID, HomeID: f.Home.ID, AwayID: f.Away.ID, LeagueID: f.League.ID,
		HomeName: f.Home.Name, AwayName: f.Away.Name, LeagueName: f.League.Name,
		Status: string(f.APIStatus.Short), Terminal: f.APIStatus.Terminal(), Elapsed: f.APIElapsed,
		Extra: f.APIExtra, HomeScore: f.HomeScore, AwayScore: f.AwayScore, HasEvents: true,
	}
}

func guardedDetectedAudit(t *testing.T, e *event.Event) auditlog.Record {
	t.Helper()
	record, err := auditlog.New(auditlog.KindEventDetected, e.ID, e.FixtureID, auditlog.EventDetectedPayload{
		EventID: e.ID, FixtureID: e.FixtureID, EventType: string(e.Type), Detail: string(e.Detail),
		PlayerName: *e.Player.Name, Minute: e.Minute, TeamID: int64(e.Team.ID), TeamName: e.Team.Name, Counter: 1,
	})
	require.NoError(t, err)
	return record
}

func guardedRemovedAudit(t *testing.T, e *event.Event, at time.Time) auditlog.Record {
	t.Helper()
	record, err := auditlog.New(auditlog.KindEventRemoved, e.ID, e.FixtureID, auditlog.EventRemovedPayload{
		EventID: e.ID, FixtureID: e.FixtureID, RemovedAt: at, Reason: "debounce_zero",
	})
	require.NoError(t, err)
	return record
}

func guardedActivatedAudit(t *testing.T, fixtureID int64, at time.Time) auditlog.Record {
	t.Helper()
	record, err := auditlog.New(auditlog.KindFixtureActivated, uuid.Nil, fixtureID, auditlog.FixtureActivatedPayload{
		FixtureID: fixtureID, ActivatedAt: at, Reason: "already_started",
	})
	require.NoError(t, err)
	return record
}

func copyTestTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
