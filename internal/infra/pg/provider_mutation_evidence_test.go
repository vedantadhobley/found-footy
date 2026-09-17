// provider_mutation_evidence_test.go distinguishes restricted plans from actual
// effects, including partial corrections and conditional completion checks.
package pg

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
)

// TestRestrictedActiveOperations pins the distinction between total rejection
// and partial policy; unaffected insert/presence operations are not restricted.
func TestRestrictedActiveOperations(t *testing.T) {
	id := uuid.New()
	plan := providerintegrity.ActiveMutationPlan{
		Fixture:        &fixture.Fixture{APIStatus: fixture.APIStatus{Short: "2h"}},
		Inserts:        []providerintegrity.PlannedEventInsert{{Event: &event.Event{ID: id}}},
		Presences:      []providerintegrity.PlannedEventPresence{{EventID: id}},
		Updates:        []providerintegrity.PlannedEventUpdate{{EventID: id}},
		Absences:       []providerintegrity.PlannedEventAbsence{{EventID: id}},
		UnknownDeletes: []uuid.UUID{id},
	}
	require.Nil(t, restrictedActiveOperations(plan, providerintegrity.PolicyTrusted))
	positive := restrictedActiveOperations(plan, providerintegrity.PolicyPositiveOnly)
	require.True(t, positive.FixtureRefreshLimited)
	require.Empty(t, positive.EventInserts)
	require.Empty(t, positive.PresenceVotes)
	require.Equal(t, []uuid.UUID{id}, positive.EventCorrectionsLimited)
	require.Equal(t, []uuid.UUID{id}, positive.AbsenceVotes)
	require.Equal(t, []uuid.UUID{id}, positive.PlaceholderDeletes)
	require.False(t, positive.CompletionCheck)
	rejected := restrictedActiveOperations(plan, providerintegrity.PolicyRejected)
	require.Equal(t, []uuid.UUID{id}, rejected.EventInserts)
	require.Equal(t, []uuid.UUID{id}, rejected.PresenceVotes)
	now := time.Now()
	terminal := now.Add(-time.Hour)
	plan.Fixture.APIStatus.Short, plan.Fixture.TerminalObservedAt = "ft", &terminal
	plan.Completion = providerintegrity.CompletionPlan{TerminalBefore: terminal, CompletedAt: now, GraceSeconds: 3600}
	require.True(t, restrictedActiveOperations(plan, providerintegrity.PolicyPositiveOnly).CompletionCheck)
	plan.Completion.TerminalBefore = terminal.Add(-time.Second)
	require.False(t, restrictedActiveOperations(plan, providerintegrity.PolicyPositiveOnly).CompletionCheck)
}

// TestRestrictedFixtureOperations keeps historical initial insertion unchanged
// under positive_only while describing rejected inserts and limited refreshes.
func TestRestrictedFixtureOperations(t *testing.T) {
	plan := providerintegrity.FixtureMutationPlan{}
	require.Nil(t, restrictedFixtureOperations(plan, providerintegrity.PolicyTrusted))
	require.Nil(t, restrictedFixtureOperations(plan, providerintegrity.PolicyPositiveOnly))
	require.True(t, restrictedFixtureOperations(plan, providerintegrity.PolicyRejected).FixtureRefreshLimited)
	plan.BaselineExists = true
	require.True(t, restrictedFixtureOperations(plan, providerintegrity.PolicyPositiveOnly).FixtureRefreshLimited)
}
