// repo.go — storage-side port. The pg adapter implements this. Domain
// callers depend only on the interface.
package event

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Get when the event ID doesn't exist.
var ErrNotFound = errors.New("event: not found")

// Repo is the storage port. Domain code composes state transitions on
// *Event and then Upserts; the workflow-registration methods return
// the new count so callers can decide whether to mark monitor/download
// complete.
type Repo interface {
	// Get returns the event by UUID or ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (*Event, error)

	// GetByNaturalKey returns the event by (fixture_id, natural_key) or
	// ErrNotFound. Used at monitor-poll time to look up whether an
	// event we just saw is already tracked.
	GetByNaturalKey(ctx context.Context, fixtureID int64, naturalKey string) (*Event, error)

	// Insert creates a new event row. Fails with a wrapped
	// pgconn.PgError (23505 unique_violation) if (fixture_id, natural_key)
	// collides — that's the concurrent-detection-race case where two
	// MonitorWorkflows for the same fixture both try to insert the same
	// goal. The loser catches the error, calls GetByNaturalKey to fetch
	// the winner's UUID, and proceeds.
	Insert(ctx context.Context, e *Event) error

	// Upsert saves state changes (MarkMonitorComplete, Remove, etc.)
	// back to storage. Not for initial creation — use Insert for that.
	Upsert(ctx context.Context, e *Event) error

	// ListPending returns events in the fixture that need more work
	// (NOT monitor_complete OR NOT download_complete, AND NOT removed).
	// The MonitorWorkflow uses this to find events still in the
	// discovery/download pipeline.
	ListPending(ctx context.Context, fixtureID int64) ([]*Event, error)

	// ────── Workflow-ID array tables ──────
	//
	// These correspond to the event_monitor_workflows /
	// event_download_workflows / event_drop_workflows tables. Each
	// method inserts a (event_id, workflow_id) row with ON CONFLICT DO
	// NOTHING (idempotent per (event_id, workflow_id) primary key) and
	// returns the resulting count of rows for that event_id.
	//
	// The count return is what enables the "3-poll debounce" and
	// "10-download complete" thresholds — orchestration code checks the
	// returned count and calls MarkMonitorComplete / MarkDownloadComplete
	// when the threshold is reached.

	// RegisterMonitorWorkflow records that workflowID observed this event
	// during a monitor poll. Returns the new count of distinct monitor
	// workflows registered.
	RegisterMonitorWorkflow(ctx context.Context, eventID uuid.UUID, workflowID string) (count int, err error)

	// RegisterVideoValidationWorkflow records that a VideoValidationWorkflow
	// attempted to download + AI-validate + hash a candidate video for
	// this event. outcomeClass captures the typed error class if the
	// workflow failed (empty string on success). Returns the new count.
	//
	// Name reflects the workflow rename per decisions.md 2026-07-07 —
	// the old Python "DownloadWorkflow" undersold what the workflow
	// actually does (download + validate + hash), so both the workflow
	// and this registration method were renamed.
	RegisterVideoValidationWorkflow(ctx context.Context, eventID uuid.UUID, workflowID string, outcomeClass string) (count int, err error)

	// RegisterDropWorkflow records that workflowID observed this event
	// dropping out of the API poll — VAR-detection debounce. Returns the
	// new count.
	RegisterDropWorkflow(ctx context.Context, eventID uuid.UUID, workflowID string) (count int, err error)
}
