// Completed-fixture retention and clip-reclamation activity.
package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PruneOldFixturesInput carries the retention cutoff.
type PruneOldFixturesInput struct {
	Threshold time.Time
}

// PruneOldFixturesOutput carries both halves of the retention rule.
// Deleted is the number of clip-LESS completed fixtures hard-deleted
// (rows gone — safe, they never had a URL). ReclaimEventIDs are the
// events of clip-BEARING aged fixtures whose Garage bytes the workflow
// must reclaim via DestroyEvent (#176 option B: bytes reclaimed, rows
// kept as 410 tombstones for URL-stability).
type PruneOldFixturesOutput struct {
	Deleted         int
	ReclaimEventIDs []uuid.UUID
}

// PruneOldFixtures runs the SQL side of the two-part retention rule for
// completed fixtures older than in.Threshold:
//
//  1. Clip-BEARING fixtures — collect their events that still have live
//     shares (ListReclaimableEventIDs) so the workflow can DestroyEvent
//     each (revoke shares → 410, delete Garage bytes). Rows stay: the KB
//     tombstones preserve URL-stability (rebuild-plan §3 as revised
//     2026-08-11); the GB video bytes are the actual reclaimable cost.
//  2. Clip-LESS fixtures — hard-delete outright (PruneCompleted). They
//     never minted a share, so removing their rows 404s nothing.
//
// The two sets are disjoint (a completed fixture either has surviving
// shares or it doesn't), so order is immaterial. This activity does only
// the SQL; the byte reclaim is the workflow's DestroyEvent loop — that
// activity owns the S3 client, this one doesn't need it.
func (a *Activities) PruneOldFixtures(ctx context.Context, in PruneOldFixturesInput) (PruneOldFixturesOutput, error) {
	reclaim, err := a.FixtureRepo.ListReclaimableEventIDs(ctx, in.Threshold)
	if err != nil {
		return PruneOldFixturesOutput{}, fmt.Errorf("ingest.PruneOldFixtures: list reclaimable: %w", err)
	}
	n, err := a.FixtureRepo.PruneCompleted(ctx, in.Threshold)
	if err != nil {
		return PruneOldFixturesOutput{}, fmt.Errorf("ingest.PruneOldFixtures: prune clipless: %w", err)
	}
	return PruneOldFixturesOutput{Deleted: n, ReclaimEventIDs: reclaim}, nil
}
