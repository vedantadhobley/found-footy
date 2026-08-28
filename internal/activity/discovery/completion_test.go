// Checklist completion activity tests cover typed new, retry, and missing-row
// results without coupling activity behavior to SQL.
package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

type fakeDownstreamCompletionStore struct {
	result event.DownstreamCompletion
	err    error
}

func (f fakeDownstreamCompletionStore) CompleteDownstreamWorkflow(
	context.Context, uuid.UUID, string, string, string,
) (event.DownstreamCompletion, error) {
	return f.result, f.err
}

func TestMarkDownstreamCompleteReturnsStoredClassification(t *testing.T) {
	completedAt := time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC)
	a := &Activities{Downstream: fakeDownstreamCompletionStore{result: event.DownstreamCompletion{
		State: event.DownstreamAlreadyCompleted, OutcomeClass: string(event.OutcomeEventRemoved),
		CompletedAt: completedAt,
	}}}
	out, err := a.MarkDownstreamComplete(context.Background(), MarkDownstreamCompleteInput{
		EventID: uuid.New(), WorkflowType: "discovery", WorkflowID: "event-test",
		OutcomeClass: "assets_surfaced",
	})
	if err != nil {
		t.Fatalf("MarkDownstreamComplete: %v", err)
	}
	if out.RowsUpdated != 0 || out.State != event.DownstreamAlreadyCompleted ||
		out.StoredOutcomeClass != string(event.OutcomeEventRemoved) || !out.CompletedAt.Equal(completedAt) {
		t.Fatalf("completion output = %+v", out)
	}
}

func TestMarkDownstreamCompleteRejectsMissingChecklist(t *testing.T) {
	a := &Activities{Downstream: fakeDownstreamCompletionStore{err: event.ErrDownstreamWorkflowNotFound}}
	_, err := a.MarkDownstreamComplete(context.Background(), MarkDownstreamCompleteInput{
		EventID: uuid.New(), WorkflowType: "discovery", WorkflowID: "event-missing",
		OutcomeClass: "no_candidates",
	})
	if !errors.Is(err, event.ErrDownstreamWorkflowNotFound) {
		t.Fatalf("missing checklist error = %v", err)
	}
}
