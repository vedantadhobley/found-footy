// provider_mutation_shadow_test.go verifies advisory policy without weakening
// atomic writes, original retry effects, or stale-observation protection.
package pg_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// TestProviderMutationShadowActive applies even regressive plans while saving
// the exact operations enforcement would restrict. Cross-mode retries must not
// convert a committed advisory result into an enforced result or vice versa.
func TestProviderMutationShadowActive(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := time.Date(2026, 9, 17, 5, 0, 0, 0, time.UTC)
	index := 0
	for _, mode := range []providerintegrity.EnforcementMode{providerintegrity.EnforcementShadow, providerintegrity.EnforcementEnforce} {
		for _, recommendation := range []providerintegrity.MutationPolicy{providerintegrity.PolicyPositiveOnly, providerintegrity.PolicyRejected} {
			t.Run(string(mode)+"/"+string(recommendation), func(t *testing.T) {
				index++
				at := base.Add(time.Duration(index) * time.Hour)
				f := guardedActiveFixture(t, 9900+int64(index), at, 65, 2, 0)
				require.NoError(t, fixtures.Insert(ctx, f))
				events := pg.NewEventRepo(pool)
				old := guardedEvent(f.ID, 40, "Old Goal", at)
				require.NoError(t, events.Insert(ctx, old, "seed"))
				observedAt := at.Add(30 * time.Second)
				candidate := *f
				zero := 0
				candidate.HomeScore, candidate.LastPolledAt = &zero, &observedAt
				poll := providerintegrity.HealthPoll{ID: fmt.Sprintf("shadow-%d", index), At: observedAt,
					Observations: []providerintegrity.HealthObservation{{FixtureID: f.ID,
						Availability: providerintegrity.AvailabilityUsable, BaselineLastPolled: copyTestTime(f.LastPolledAt),
						Comparison: providerintegrity.FixtureComparison{Stored: guardedFacts(f), Observed: guardedFacts(&candidate)},
					}},
				}
				health, err := pg.NewProviderHealthRepo(pool).Observe(ctx, poll)
				require.NoError(t, err)
				incoming := guardedEvent(f.ID, 42, "New Goal", observedAt)
				detected := guardedDetectedAudit(t, incoming)
				plan := providerintegrity.ActiveMutationPlan{
					Guard: providerintegrity.ObservationGuard{ObservationID: poll.ID, ObservedAt: poll.At,
						ExpectedGeneration: health.Generation, RequireHealthReceipt: true},
					Fixture: &candidate, BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt),
					LocalPolicy: recommendation, WorkflowID: poll.ID,
					Inserts:  []providerintegrity.PlannedEventInsert{{Event: incoming, Audit: &detected}},
					Absences: []providerintegrity.PlannedEventAbsence{{EventID: old.ID, Audit: guardedRemovedAudit(t, old, observedAt)}},
				}
				repo := pg.NewProviderMutationRepo(pool, mode)
				first, err := repo.ApplyActive(ctx, plan)
				require.NoError(t, err)
				require.Equal(t, recommendation, first.Decision.RecommendedPolicy)
				require.Equal(t, mode, first.Decision.Mode)
				require.Equal(t, []uuid.UUID{old.ID}, first.Decision.Restricted.AbsenceVotes)
				require.Equal(t, recommendation == providerintegrity.PolicyRejected, len(first.Decision.Restricted.EventInserts) == 1)
				stored, err := fixtures.Get(ctx, f.ID)
				require.NoError(t, err)
				oldAfter, err := events.Get(ctx, old.ID)
				require.NoError(t, err)
				if mode == providerintegrity.EnforcementShadow {
					require.Equal(t, providerintegrity.PolicyTrusted, first.Policy)
					require.True(t, first.Applied)
					require.Equal(t, 0, *stored.HomeScore)
					require.True(t, oldAfter.Removed)
					require.Equal(t, []uuid.UUID{incoming.ID}, first.InsertedEventIDs)
					require.Equal(t, []uuid.UUID{old.ID}, first.RemovedEventIDs)
				} else {
					require.Equal(t, recommendation, first.Policy)
					require.Equal(t, 2, *stored.HomeScore)
					require.False(t, oldAfter.Removed)
					require.Equal(t, recommendation != providerintegrity.PolicyRejected, first.Applied)
				}
				otherMode := providerintegrity.EnforcementShadow
				if mode == otherMode {
					otherMode = providerintegrity.EnforcementEnforce
				}
				retry, err := pg.NewProviderMutationRepo(pool, otherMode).ApplyActive(ctx, plan)
				require.NoError(t, err)
				requireMutationReceiptEqual(t, first, retry)
				// A new logical observation cannot reuse this old baseline.
				if first.Applied {
					plan.Guard.ObservationID += "-stale"
					stale, err := repo.ApplyActive(ctx, plan)
					require.NoError(t, err)
					require.True(t, stale.Stale)
					require.Nil(t, stale.Decision)
				}
			})
		}
	}
}

// TestProviderMutationShadowFixture covers both passive writers under a durable
// provider restriction and a local rejection; neither policy silently enforces.
func TestProviderMutationShadowFixture(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	_, err := pool.Exec(ctx, `UPDATE provider_health SET policy='positive_only'`)
	require.NoError(t, err)
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	for i, writer := range []providerintegrity.MutationMode{providerintegrity.MutationModeIngest, providerintegrity.MutationModeStaging} {
		f := makeStaging(9910+int64(i), base.Add(time.Hour))
		f.APIStatus.Short = "ns"
		require.NoError(t, fixtures.Insert(ctx, f))
		candidate := *f
		candidate.Home.Name = "Corrected name"
		candidate.LastPolledAt = &base
		plan := providerintegrity.FixtureMutationPlan{Guard: providerintegrity.ObservationGuard{ObservationID: string(writer), ObservedAt: base},
			Mode: writer, Fixture: &candidate, BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt), LocalPolicy: providerintegrity.PolicyRejected}
		result, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementShadow).ApplyFixture(ctx, plan)
		require.NoError(t, err)
		require.True(t, result.Applied)
		require.Equal(t, "Corrected name", result.Fixture.Home.Name)
		require.Equal(t, providerintegrity.PolicyTrusted, result.Policy)
		require.Equal(t, providerintegrity.PolicyRejected, result.Decision.RecommendedPolicy)
		require.Equal(t, providerintegrity.PolicyPositiveOnly, result.Decision.ProviderPolicy)
		retry, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementEnforce).ApplyFixture(ctx, plan)
		require.NoError(t, err)
		requireMutationReceiptEqual(t, result, retry)
	}
}

// TestProviderMutationShadowCompletion proves that an advisory restriction does
// not extend terminal polling, while enforce still holds the completion gate.
func TestProviderMutationShadowCompletion(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for i, mode := range []providerintegrity.EnforcementMode{providerintegrity.EnforcementShadow, providerintegrity.EnforcementEnforce} {
		at := base.Add(time.Duration(i) * time.Hour)
		f := guardedActiveFixture(t, 9920+int64(i), at, 90, 0, 0)
		f.APIStatus = fixture.APIStatus{Short: "ft", Long: "Match Finished"}
		terminal := at.Add(-2 * time.Hour)
		f.TerminalObservedAt = &terminal
		require.NoError(t, fixtures.Insert(ctx, f))
		observedAt := at.Add(30 * time.Second)
		candidate := *f
		candidate.LastPolledAt = &observedAt
		poll := providerintegrity.HealthPoll{ID: "complete-" + string(mode), At: observedAt, Observations: []providerintegrity.HealthObservation{{
			FixtureID: f.ID, Availability: providerintegrity.AvailabilityUsable, BaselineLastPolled: copyTestTime(f.LastPolledAt),
			Comparison: providerintegrity.FixtureComparison{Stored: guardedFacts(f), Observed: guardedFacts(&candidate)},
		}}}
		health, err := pg.NewProviderHealthRepo(pool).Observe(ctx, poll)
		require.NoError(t, err)
		result, err := pg.NewProviderMutationRepo(pool, mode).ApplyActive(ctx, providerintegrity.ActiveMutationPlan{
			Guard:   providerintegrity.ObservationGuard{ObservationID: poll.ID, ObservedAt: observedAt, ExpectedGeneration: health.Generation, RequireHealthReceipt: true},
			Fixture: &candidate, BaselineExists: true, BaselineLastPolled: copyTestTime(f.LastPolledAt),
			LocalPolicy: providerintegrity.PolicyPositiveOnly, WorkflowID: poll.ID,
			Completion: providerintegrity.CompletionPlan{TerminalBefore: at.Add(-time.Hour), CompletedAt: observedAt, GraceSeconds: 3600},
		})
		require.NoError(t, err)
		require.True(t, result.Decision.Restricted.CompletionCheck)
		require.Equal(t, mode == providerintegrity.EnforcementShadow, result.Completed)
	}
}

// TestProviderMutationInvalidModeFailsBeforeIO protects non-config callers too.
func TestProviderMutationInvalidModeFailsBeforeIO(t *testing.T) {
	repo := pg.NewProviderMutationRepo(nil, "typo")
	_, err := repo.ApplyActive(context.Background(), providerintegrity.ActiveMutationPlan{})
	require.ErrorContains(t, err, "invalid provider enforcement mode")
	_, err = repo.ApplyFixture(context.Background(), providerintegrity.FixtureMutationPlan{})
	require.ErrorContains(t, err, "invalid provider enforcement mode")
}

// TestProviderShadowRecoveryKeepsOriginalBaseline proves shadow canonical writes
// cannot make a repeated regression appear recovered on the next observation.
func TestProviderShadowRecoveryKeepsOriginalBaseline(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := time.Date(2026, 9, 17, 15, 0, 0, 0, time.UTC)
	f := guardedActiveFixture(t, 9930, base, 65, 2, 0)
	require.NoError(t, fixtures.Insert(ctx, f))
	for i, score := range []int{0, 0, 2} {
		stored, err := fixtures.Get(ctx, f.ID)
		require.NoError(t, err)
		at := base.Add(time.Duration(i+1) * 30 * time.Second)
		candidate := *stored
		candidate.HomeScore, candidate.LastPolledAt = &score, &at
		comparison := providerintegrity.FixtureComparison{Stored: guardedFacts(stored), Observed: guardedFacts(&candidate)}
		poll := providerintegrity.HealthPoll{ID: fmt.Sprintf("shadow-recovery-%d", i), At: at,
			Observations: []providerintegrity.HealthObservation{{FixtureID: f.ID, Availability: providerintegrity.AvailabilityUsable,
				BaselineLastPolled: copyTestTime(stored.LastPolledAt), Comparison: comparison}},
		}
		health, err := pg.NewProviderHealthRepo(pool).Observe(ctx, poll)
		require.NoError(t, err)
		result, err := pg.NewProviderMutationRepo(pool, providerintegrity.EnforcementShadow).ApplyActive(ctx, providerintegrity.ActiveMutationPlan{
			Guard:   providerintegrity.ObservationGuard{ObservationID: poll.ID, ObservedAt: at, ExpectedGeneration: health.Generation, RequireHealthReceipt: true},
			Fixture: &candidate, BaselineExists: true, BaselineLastPolled: copyTestTime(stored.LastPolledAt),
			LocalPolicy: providerintegrity.AssessFixture(comparison).Policy, WorkflowID: poll.ID,
		})
		require.NoError(t, err)
		require.Equal(t, providerintegrity.PolicyTrusted, result.Policy)
		require.Equal(t, score, *result.Fixture.HomeScore)
		if i < 2 {
			require.Equal(t, providerintegrity.PolicyPositiveOnly, result.Decision.RecommendedPolicy)
		} else {
			require.Equal(t, providerintegrity.PolicyTrusted, result.Decision.RecommendedPolicy)
			require.Nil(t, result.Decision.Restricted)
			require.Len(t, health.Transitions, 1)
			require.True(t, health.Transitions[0].Resolved)
			require.Equal(t, providerintegrity.HealthCause(providerintegrity.ReasonScoreDecreased), health.Transitions[0].Cause)
		}
	}
}

// requireMutationReceiptEqual compares the durable wire contract, not pgx's
// Local location pointer versus JSON's equivalent UTC time representation.
func requireMutationReceiptEqual(t *testing.T, first, retry providerintegrity.MutationResult) {
	t.Helper()
	before, err := json.Marshal(first)
	require.NoError(t, err)
	after, err := json.Marshal(retry)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}
