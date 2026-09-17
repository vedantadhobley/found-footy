// mutation.go defines the guarded provider-write command shared by every
// provider-backed fixture writer and implemented atomically by Postgres.
package providerintegrity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
)

// ErrStaleMutationPlan means a newer provider observation or canonical fixture
// write won the race. The caller must discard this observation; a later poll
// will reconcile from the newer baseline.
var ErrStaleMutationPlan = errors.New("provider integrity: stale mutation plan")

// MutationMode identifies the provider writer without duplicating its policy.
type MutationMode string

const (
	MutationModeActive  MutationMode = "active"
	MutationModeStaging MutationMode = "staging"
	MutationModeIngest  MutationMode = "ingest"
)

// ObservationGuard binds a write to one logical provider observation. Active
// writes require the exact durable health receipt produced before fan-out.
type ObservationGuard struct {
	ObservationID        string
	ObservedAt           time.Time
	ExpectedGeneration   int64
	RequireHealthReceipt bool
}

// Validate rejects commands that could not be made retry-idempotent.
func (g ObservationGuard) Validate() error {
	if g.ObservationID == "" || len(g.ObservationID) > 200 || g.ObservedAt.IsZero() || g.ExpectedGeneration < 0 {
		return fmt.Errorf("invalid provider mutation observation")
	}
	return nil
}

// PlannedEventInsert is one new event and its optional detected audit.
type PlannedEventInsert struct {
	Event *event.Event
	Audit *auditlog.Record
}

// PlannedEventPresence is one idempotent debounce vote and stable audit.
type PlannedEventPresence struct {
	EventID uuid.UUID
	Audit   auditlog.Record
}

// PlannedEventUpdate carries provider-mutable fields for one existing event.
type PlannedEventUpdate struct {
	EventID uuid.UUID
	Fresh   *event.Event
}

// PlannedEventAbsence is one idempotent drop vote and removal audit.
type PlannedEventAbsence struct {
	EventID uuid.UUID
	Audit   auditlog.Record
}

// ActiveMutationPlan is the complete possible mutation set for one active
// fixture observation. The repository derives the effective policy under lock.
type ActiveMutationPlan struct {
	Guard              ObservationGuard
	Fixture            *fixture.Fixture
	BaselineExists     bool
	BaselineLastPolled *time.Time
	LocalPolicy        MutationPolicy
	WorkflowID         string
	Inserts            []PlannedEventInsert
	Presences          []PlannedEventPresence
	Updates            []PlannedEventUpdate
	Absences           []PlannedEventAbsence
	UnknownDeletes     []uuid.UUID
	Completion         CompletionPlan
}

// CompletionPlan carries evidence that is not derivable from durable rows.
// Readiness and the final audit record are calculated inside the transaction.
type CompletionPlan struct {
	TerminalBefore           time.Time
	CompletedAt              time.Time
	GraceSeconds             int64
	ProviderScoreEventParity *bool
}

// FixtureMutationPlan covers ingest and passive staging writes. Activate is
// staging-only; ingest preserves an existing row's lifecycle.
type FixtureMutationPlan struct {
	Guard              ObservationGuard
	Mode               MutationMode
	Fixture            *fixture.Fixture
	BaselineExists     bool
	BaselineLastPolled *time.Time
	LocalPolicy        MutationPolicy
	Activate           bool
	ActivationAudit    *auditlog.Record
}

// MutationResult is both the activity result and the durable retry receipt.
// External effects run only after this committed result is returned.
type MutationResult struct {
	Applied bool
	Stale   bool
	Policy  MutationPolicy
	// Nil for old receipts or stale plans that never reached policy evaluation.
	Decision       *MutationDecision `json:",omitempty"`
	Fixture        *fixture.Fixture
	FixtureChanged bool
	// These effects are classified against the original locked fixture. An
	// activity retry must not recalculate them against already-committed data.
	FixtureStructuralChanged   bool
	FixturePresentationChanged bool
	EventChanged               bool
	InsertedEventIDs           []uuid.UUID
	NewNamedEventIDs           []uuid.UUID
	StableEventIDs             []uuid.UUID
	RemovedEventIDs            []uuid.UUID
	UnknownDropped             int
	Completed                  bool
}

// MutationStore owns the single transactional enforcement boundary.
type MutationStore interface {
	ApplyActive(context.Context, ActiveMutationPlan) (MutationResult, error)
	ApplyFixture(context.Context, FixtureMutationPlan) (MutationResult, error)
}
