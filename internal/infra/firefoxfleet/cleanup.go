// cleanup.go owns identity-safe release and retryable orphan reconciliation.
package firefoxfleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
)

// Release locates the current event container and proves its scope before removal.
// Concurrent release and already-missing containers are successful terminal states.
func (f *Fleet) Release(ctx context.Context, eventID uuid.UUID) error {
	inst, ok, err := f.find(ctx, eventID)
	if err != nil || !ok {
		return err
	}
	return f.removeInstance(ctx, inst.ID)
}

// removeInstance removes a previously ownership-verified immutable container ID.
// Force removal recovers a failed stop; anonymous volumes are disposable, while
// cookie binds and explicitly named operator profiles remain owned elsewhere.
func (f *Fleet) removeInstance(ctx context.Context, id string) error {
	timeout := 5
	_ = f.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !client.IsErrNotFound(err) {
		return fmt.Errorf("firefoxfleet.Release: rm %s: %w", id, err)
	}
	return nil
}

// ReapOrphans reconciles unleased containers older than minAge. It attempts every
// eligible ID while the activity remains alive, preserving both partial success
// and joined failures. Retries relist current state, so completed deletes are safe.
func (f *Fleet) ReapOrphans(ctx context.Context, live map[uuid.UUID]bool, minAge time.Duration) ([]string, error) {
	insts, err := f.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-minAge)
	var reaped []string
	var failures []error
	for _, in := range insts {
		if err := ctx.Err(); err != nil {
			return reaped, errors.Join(append(failures, err)...)
		}
		if live[in.EventID] || in.CreatedAt.After(cutoff) {
			continue
		}
		// Do not resolve the reusable name again. A newer container must not
		// inherit the old container's age or this sweep's ownership snapshot.
		inst, exists, err := f.inspect(ctx, in.ID, in.EventID)
		if err == nil && !exists {
			continue
		}
		if err == nil && exists {
			err = f.removeInstance(ctx, inst.ID)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("reap %s (%s): %w", in.Name, in.ID, err))
			continue
		}
		reaped = append(reaped, in.Name)
	}
	return reaped, errors.Join(failures...)
}
