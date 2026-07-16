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

// Repo is the storage port. The debounce methods (RegisterEventPresence,
// RegisterEventAbsence) implement the symmetric-counter model designed
// in the O2 planning session — see docs/decisions.md 2026-07-07 debounce
// entry. The counter increments on presence (cap 3), decrements on
// absence (floor 0), triggers downstream once when it first hits 3,
// and soft-deletes the event when it hits 0.
type Repo interface {
	// Get returns the event by UUID or ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (*Event, error)

	// GetByNaturalKey returns the event for (fixture_id, natural_key) or
	// ErrNotFound. Called by MonitorWorkflow when it sees an API event
	// and wants to know if we already track it. Returns removed events
	// too — callers filter based on their needs.
	GetByNaturalKey(ctx context.Context, fixtureID int64, naturalKey string) (*Event, error)

	// Insert creates a new event row and atomically records workflowID's
	// initial presence vote. The event lands with debounce_count=1
	// (seeded) and one row in event_monitor_workflows.
	//
	// Fails with a wrapped pgconn.PgError (23505 unique_violation) if
	// (fixture_id, natural_key) collides — the concurrent-detection-race
	// case where two monitor workflows both saw the event first. The
	// caller catches, GetByNaturalKey, then calls RegisterEventPresence
	// (which will be a no-op idempotent skip for the same workflowID
	// because Insert's vote is already recorded).
	Insert(ctx context.Context, e *Event, workflowID string) error

	// Upsert updates mutable state fields (monitor_complete,
	// download_complete, removed*, telemetry) on an existing row.
	// Does NOT touch debounce_count or downstream_triggered — those
	// are managed exclusively by RegisterEventPresence/Absence.
	Upsert(ctx context.Context, e *Event) error

	// ListPending returns events in the fixture that need more work
	// (NOT removed AND (NOT monitor_complete OR NOT download_complete)).
	// The MonitorWorkflow uses this to find events still in the
	// discovery/download pipeline.
	ListPending(ctx context.Context, fixtureID int64) ([]*Event, error)

	// ────── Symmetric debounce ──────

	// RegisterEventPresence records a presence vote by workflowID for
	// this event. Idempotent per (event_id, workflow_id): a retrying
	// activity from the same workflow_id is a no-op.
	//
	// Returns:
	//   newCount: post-vote debounce_count (0..3). Capped at 3.
	//   justTriggeredDownstream: true iff THIS call was the one that
	//     flipped downstream_triggered from FALSE→TRUE. Exactly one
	//     RegisterEventPresence call across the event's lifetime ever
	//     sees this true. The caller spawns downstream workflows when
	//     this is true.
	//   err: transport / DB error.
	RegisterEventPresence(ctx context.Context, eventID uuid.UUID, workflowID string) (
		newCount int, justTriggeredDownstream bool, err error,
	)

	// RegisterEventAbsence records an absence vote by workflowID for
	// this event. Idempotent per (event_id, workflow_id).
	//
	// Atomic soft-delete on hit-zero: when the decrement brings
	// debounce_count to 0, the same transaction also sets
	// removed=TRUE, removed_reason='var', removed_at=NOW(). The event
	// is FROZEN — subsequent presence votes from later cycles are
	// rejected via the collision handler in the caller (the removed
	// row still holds the natural_key, so no fresh Insert either).
	//
	// Returns:
	//   newCount: post-vote debounce_count (0..3). Floored at 0.
	//   hitZero: true iff THIS call brought the count to 0 AND
	//     soft-deleted the row. The caller runs the destroy pipeline
	//     (cancel in-flight Temporal workflows, soft-delete
	//     video_shares) — that's a separate activity, not a repo
	//     method.
	//   err: transport / DB error.
	RegisterEventAbsence(ctx context.Context, eventID uuid.UUID, workflowID string) (
		newCount int, hitZero bool, err error,
	)

	// RegisterVideoValidationWorkflow records that a VideoValidationWorkflow
	// attempted to download + AI-validate + hash a candidate video for
	// this event. outcomeClass captures the typed error class if the
	// workflow failed (empty string on success). Returns the new
	// monotonic count of registered attempts (max ~10 per event).
	//
	// This one is UNCHANGED by the O2 debounce redesign — it tracks
	// download attempts, not presence/absence stability.
	RegisterVideoValidationWorkflow(ctx context.Context, eventID uuid.UUID, workflowID string, outcomeClass string) (count int, err error)

	// RegisterDownstreamWorkflow inserts a fresh row into
	// event_downstream_workflows so the fixture-completion check
	// (FixtureRepo.FixtureReadyToComplete) sees the workflow as
	// pending. Idempotent via ON CONFLICT DO NOTHING on
	// (event_id, workflow_type, workflow_id) so activity retries after
	// partial-success crashes don't double-insert.
	//
	// Called by Monitor.ReconcileFixture when it flips
	// downstream_triggered=true and spawns Discovery — the row insert
	// runs BEFORE the Temporal spawn so the completion check in the
	// same or next cycle correctly sees "downstream pending."
	// See decisions.md 2026-07-16 "Downstream workflow spawn via
	// Temporal-direct + register-on-flip" for the load-bearing
	// rationale.
	//
	// workflowType is a short identifier ("discovery", "video",
	// "asset", …) matching the enum values on
	// event_downstream_workflows.workflow_type. workflowID is the
	// deterministic Temporal workflow_id (e.g. "discovery-{event_id}")
	// so Temporal's RejectDuplicate policy pairs 1:1 with a row.
	RegisterDownstreamWorkflow(ctx context.Context, eventID uuid.UUID, workflowType, workflowID string) error
}
