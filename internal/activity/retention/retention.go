// Package retention plans media expiry without deleting durable match history.
package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// FixtureWindow resolves the shared public completed-fixture cutoff.
type FixtureWindow interface {
	PublicCompletedCutoff(ctx context.Context, completedFixtureDates int) (*time.Time, error)
}

// MediaAssets lists events whose object bytes remain outside that window.
type MediaAssets interface {
	ListUnreclaimedEventIDsBefore(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error)
}

// Activities owns the SQL-only planning half of daily media retention. Object
// mutation remains one retryable DestroyEvent activity per event.
type Activities struct {
	Fixtures FixtureWindow
	Assets   MediaAssets
}

// PlanMediaRetentionInput carries the shared public-window size.
type PlanMediaRetentionInput struct {
	CompletedFixtureDates int
}

// PlanMediaRetentionOutput is the deterministic worklist plus its UTC cutoff.
type PlanMediaRetentionOutput struct {
	Cutoff   *time.Time
	EventIDs []uuid.UUID
}

// PlanMediaRetention returns events with unreclaimed objects strictly older
// than the oldest public fixture date. It never deletes SQL rows.
func (a *Activities) PlanMediaRetention(
	ctx context.Context,
	in PlanMediaRetentionInput,
) (PlanMediaRetentionOutput, error) {
	if in.CompletedFixtureDates <= 0 {
		return PlanMediaRetentionOutput{}, fmt.Errorf("retention.PlanMediaRetention: completed fixture dates must be > 0")
	}
	cutoff, err := a.Fixtures.PublicCompletedCutoff(ctx, in.CompletedFixtureDates)
	if err != nil {
		return PlanMediaRetentionOutput{}, fmt.Errorf("retention.PlanMediaRetention: public cutoff: %w", err)
	}
	out := PlanMediaRetentionOutput{Cutoff: cutoff}
	if cutoff == nil {
		return out, nil
	}
	eventIDs, err := a.Assets.ListUnreclaimedEventIDsBefore(ctx, *cutoff)
	if err != nil {
		return PlanMediaRetentionOutput{}, fmt.Errorf("retention.PlanMediaRetention: list media: %w", err)
	}
	out.EventIDs = eventIDs
	return out, nil
}
