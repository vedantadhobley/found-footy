// Downstream tracking tests verify exact checklist completion and idempotent
// retry classification against real Postgres.
package pg_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

func TestEventRepo_CompleteDownstreamWorkflowClassifiesIdentity(t *testing.T) {
	ctx, _, events, fixtures := setupEventRepo(t)
	const fixtureID int64 = 910001
	seedFixture(t, ctx, fixtures, fixtureID)
	e := makeGoalEvent(fixtureID, 1)
	if err := events.Insert(ctx, e, "monitor-1"); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	workflowID := "event-" + e.ID.String()
	if err := events.RegisterDownstreamWorkflow(ctx, e.ID, "discovery", workflowID); err != nil {
		t.Fatalf("register downstream: %v", err)
	}

	first, err := events.CompleteDownstreamWorkflow(
		ctx, e.ID, "discovery", workflowID, "assets_surfaced",
	)
	if err != nil {
		t.Fatalf("complete downstream: %v", err)
	}
	if first.State != event.DownstreamCompletedNow || first.OutcomeClass != "assets_surfaced" || first.CompletedAt.IsZero() {
		t.Fatalf("first completion = %+v", first)
	}

	retry, err := events.CompleteDownstreamWorkflow(
		ctx, e.ID, "discovery", workflowID, "no_candidates",
	)
	if err != nil {
		t.Fatalf("retry completion: %v", err)
	}
	if retry.State != event.DownstreamAlreadyCompleted ||
		retry.OutcomeClass != first.OutcomeClass || !retry.CompletedAt.Equal(first.CompletedAt) {
		t.Fatalf("retry completion = %+v, want stored result %+v", retry, first)
	}

	_, err = events.CompleteDownstreamWorkflow(
		ctx, e.ID, "discovery", "event-"+uuid.NewString(), "no_candidates",
	)
	if !errors.Is(err, event.ErrDownstreamWorkflowNotFound) {
		t.Fatalf("missing identity error = %v, want ErrDownstreamWorkflowNotFound", err)
	}
}
