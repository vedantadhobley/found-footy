// Downstream checklist completion types distinguish a new durable transition
// from an idempotent observation of an already-completed workflow row.
package event

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrDownstreamWorkflowNotFound means the exact checklist identity was never
// registered. It is an orchestration invariant failure, not idempotent success.
var ErrDownstreamWorkflowNotFound = errors.New("event: downstream workflow not found")

// DownstreamCompletionState classifies how a completion request resolved.
type DownstreamCompletionState string

const (
	// DownstreamCompletedNow means this call closed the pending checklist row.
	DownstreamCompletedNow DownstreamCompletionState = "completed_now"
	// DownstreamAlreadyCompleted means the exact row was already terminal.
	DownstreamAlreadyCompleted DownstreamCompletionState = "already_completed"
)

// DownstreamCompletion reports the durable state stored for one exact
// (event, workflow type, workflow ID) checklist identity.
type DownstreamCompletion struct {
	State        DownstreamCompletionState
	OutcomeClass string
	CompletedAt  time.Time
}

// DownstreamCompletionRepo is the narrow storage port used when a downstream
// workflow closes its exact durable checklist identity.
type DownstreamCompletionRepo interface {
	CompleteDownstreamWorkflow(
		ctx context.Context,
		eventID uuid.UUID,
		workflowType, workflowID, outcomeClass string,
	) (DownstreamCompletion, error)
}
