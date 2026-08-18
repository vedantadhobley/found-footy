// Downstream-workflow checklist completion activity.
package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// MarkDownstreamCompleteOutput reports whether a row was actually
// updated. If not, either the row wasn't inserted (bug in the spawn
// path) or was already completed (retry, expected).
type MarkDownstreamCompleteOutput struct {
	RowsUpdated int64
}

// MarkDownstreamComplete UPDATEs the pending row for the given
// (event_id, workflow_type, workflow_id) triple, setting completed_at
// = NOW() and outcome_class. If completed_at is already set (activity
// retry after the UPDATE landed but the return was lost), leaves it
// alone. RowsUpdated tells callers which case they hit.
func (a *Activities) MarkDownstreamComplete(ctx context.Context, in MarkDownstreamCompleteInput) (MarkDownstreamCompleteOutput, error) {
	// Use a short pg-side timeout on top of Temporal's activity
	// StartToClose — an activity retry is fine but a stuck query is
	// not. 5s covers the round trip comfortably.
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tag, err := a.Pool.Exec(callCtx, `
		UPDATE event_downstream_workflows
		SET completed_at = NOW(), outcome_class = $4
		WHERE event_id = $1
		  AND workflow_type = $2
		  AND workflow_id = $3
		  AND completed_at IS NULL
	`, in.EventID, in.WorkflowType, in.WorkflowID, in.OutcomeClass)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Not fatal — either the row exists but is already
			// completed, or it never got inserted. Both are recoverable.
			return MarkDownstreamCompleteOutput{RowsUpdated: 0}, nil
		}
		return MarkDownstreamCompleteOutput{}, fmt.Errorf("discovery.MarkDownstreamComplete: %w", err)
	}
	return MarkDownstreamCompleteOutput{RowsUpdated: tag.RowsAffected()}, nil
}
