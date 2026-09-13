// Selection persistence keeps planning snapshots and atomic application explicit.
package video

import (
	"context"

	"github.com/google/uuid"
)

// SelectionRequest identifies one operation and its prepared snapshot. Standalone
// selection binds retries to every field; incoming placement binds accepted input
// and policy while allowing preparation to refresh on activity retries.
// PreparedAssetIDs attest to caller-side object checks, not merely SQL retention flags.
type SelectionRequest struct {
	ID               uuid.UUID
	EventID          uuid.UUID
	FixtureID        int64
	SnapshotHash     string
	Policy           SelectionPolicy
	PreparedAssetIDs []uuid.UUID
}

// SelectionResult retains the original committed decision on retry. A consumer
// must reload before using it as current state after other operations have run.
type SelectionResult struct {
	Plan     SelectionPlan
	ShareIDs map[uuid.UUID]string
	Replayed bool
	Skipped  SelectionSkip `json:"Skipped,omitempty"`
}

// SelectionSkip distinguishes an accepted ordinary placement from a topology
// repair that could not safely split incomplete historical source attribution.
type SelectionSkip string

const SelectionIncompleteCredits SelectionSkip = "incomplete_credits"

// SelectionRepo exposes consistent reads and standalone reselection. Routine
// workflows compose selection through PlacementRepo, never a second public write.
type SelectionRepo interface {
	LoadSelection(context.Context, uuid.UUID) (SelectionSnapshot, error)
	CommitSelection(context.Context, SelectionRequest) (SelectionResult, error)
}
