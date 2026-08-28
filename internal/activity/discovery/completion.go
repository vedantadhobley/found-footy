// Downstream-workflow checklist completion activity.
package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
)

// MarkDownstreamCompleteInput identifies the downstream checklist row by its
// event, workflow type, and workflow ID primary key.
type MarkDownstreamCompleteInput struct {
	EventID      uuid.UUID
	WorkflowType string
	WorkflowID   string
	// OutcomeClass — free-form short string. "stub_ok" from the
	// current Discovery stub. Later phases: "success", "no_candidates",
	// "twitter_rate_limited", etc.
	OutcomeClass string
}

// MarkDownstreamCompleteOutput distinguishes a new completion from an
// idempotent retry and returns the authoritative stored terminal evidence.
type MarkDownstreamCompleteOutput struct {
	RowsUpdated        int64
	State              event.DownstreamCompletionState
	StoredOutcomeClass string
	CompletedAt        time.Time
}

// MarkDownstreamComplete closes the exact checklist identity. A matching
// terminal row is idempotent success; a missing row is a typed repository
// error so the workflow cannot claim completion that was never registered.
func (a *Activities) MarkDownstreamComplete(ctx context.Context, in MarkDownstreamCompleteInput) (MarkDownstreamCompleteOutput, error) {
	if a.Downstream == nil {
		return MarkDownstreamCompleteOutput{}, fmt.Errorf("discovery.MarkDownstreamComplete: downstream store is required")
	}
	if in.EventID == uuid.Nil || in.WorkflowType == "" || in.WorkflowID == "" || in.OutcomeClass == "" {
		return MarkDownstreamCompleteOutput{}, fmt.Errorf("discovery.MarkDownstreamComplete: incomplete checklist identity or outcome")
	}
	// Use a short pg-side timeout on top of Temporal's activity
	// StartToClose — an activity retry is fine but a stuck query is
	// not. 5s covers the round trip comfortably.
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	result, err := a.Downstream.CompleteDownstreamWorkflow(
		callCtx, in.EventID, in.WorkflowType, in.WorkflowID, in.OutcomeClass,
	)
	if err != nil {
		return MarkDownstreamCompleteOutput{}, fmt.Errorf("discovery.MarkDownstreamComplete: %w", err)
	}
	rowsUpdated := int64(0)
	if result.State == event.DownstreamCompletedNow {
		rowsUpdated = 1
	}
	return MarkDownstreamCompleteOutput{
		RowsUpdated: rowsUpdated, State: result.State,
		StoredOutcomeClass: result.OutcomeClass, CompletedAt: result.CompletedAt,
	}, nil
}
