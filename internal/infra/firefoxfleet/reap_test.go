// reap_test.go pins partial failure, concurrent removal, and container identity safety.
package firefoxfleet

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"github.com/google/uuid"
)

// cleanupDocker injects daemon races without weakening the shared ownership fake.
type cleanupDocker struct {
	dockerClient
	remove func(context.Context, string, container.RemoveOptions) error
	list   func(context.Context, container.ListOptions) ([]types.Container, error)
}

// ContainerRemove intercepts only the final deletion boundary when requested.
func (d *cleanupDocker) ContainerRemove(ctx context.Context, id string, opts container.RemoveOptions) error {
	if d.remove != nil {
		return d.remove(ctx, id, opts)
	}
	return d.dockerClient.ContainerRemove(ctx, id, opts)
}

// ContainerList can replace a listed container before the sweep inspects it.
func (d *cleanupDocker) ContainerList(ctx context.Context, opts container.ListOptions) ([]types.Container, error) {
	if d.list != nil {
		return d.list(ctx, opts)
	}
	return d.dockerClient.ContainerList(ctx, opts)
}

// seedCleanupFleet creates only fake, owned containers for deterministic cleanup tests.
func seedCleanupFleet(t *testing.T, ids ...uuid.UUID) (*Fleet, *fakeDocker) {
	t.Helper()
	d := newFakeDocker()
	f, err := newWithClient(fleetConfig(devScope), d)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := f.Provision(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	return f, d
}

// TestReapOrphans_PartialFailure retries failed containers without hiding successful removals.
func TestReapOrphans_PartialFailure(t *testing.T) {
	evC := uuid.New()
	f, d := seedCleanupFleet(t, evA, evB, evC)
	failureA, failureB := errors.New("daemon removal A failed"), errors.New("daemon removal B failed")
	aID, bID := d.containers[InstanceName(devScope, evA)].id, d.containers[InstanceName(devScope, evB)].id
	attempts := map[string]int{}
	wrapper := &cleanupDocker{dockerClient: d}
	wrapper.remove = func(ctx context.Context, id string, opts container.RemoveOptions) error {
		attempts[id]++
		switch id {
		case aID:
			return failureA
		case bID:
			return failureB
		default:
			return d.ContainerRemove(ctx, id, opts)
		}
	}
	f.cli = wrapper
	reaped, err := f.ReapOrphans(context.Background(), nil, 0)
	if !errors.Is(err, failureA) || !errors.Is(err, failureB) {
		t.Errorf("sweep error = %v, want both causes", err)
	}
	if len(attempts) != 3 || len(reaped) != 1 || reaped[0] != InstanceName(devScope, evC) {
		t.Fatalf("attempts=%v reaped=%v, want all attempted and only C removed", attempts, reaped)
	}
	wrapper.remove = nil
	reaped, err = f.ReapOrphans(context.Background(), nil, 0)
	if err != nil || len(reaped) != 2 || len(d.containers) != 0 {
		t.Fatalf("retry = %v, %v; remaining=%d", reaped, err, len(d.containers))
	}
}

// TestRelease_ConcurrentNotFound accepts another owner's already-completed deletion.
func TestRelease_ConcurrentNotFound(t *testing.T) {
	f, d := seedCleanupFleet(t, evA)
	f.cli = &cleanupDocker{dockerClient: d, remove: func(ctx context.Context, id string, opts container.RemoveOptions) error {
		if err := d.ContainerRemove(ctx, id, opts); err != nil {
			return err
		}
		return errdefs.NotFound(errors.New("removed concurrently"))
	}}
	if err := f.Release(context.Background(), evA); err != nil {
		t.Fatalf("already removed = %v, want success", err)
	}
}

// TestReapOrphans_DoesNotDeleteReplacement pins deletion to the listed Docker ID, not its reusable name.
func TestReapOrphans_DoesNotDeleteReplacement(t *testing.T) {
	f, d := seedCleanupFleet(t, evA)
	oldID := d.containers[InstanceName(devScope, evA)].id
	wrapper := &cleanupDocker{dockerClient: d}
	wrapper.list = func(ctx context.Context, opts container.ListOptions) ([]types.Container, error) {
		listed, err := d.ContainerList(ctx, opts)
		wrapper.list = nil
		if err != nil {
			return nil, err
		}
		if err := d.ContainerRemove(ctx, oldID, container.RemoveOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Provision(ctx, evA); err != nil {
			t.Fatal(err)
		}
		return listed, nil
	}
	f.cli = wrapper
	if reaped, err := f.ReapOrphans(context.Background(), nil, 0); err != nil || len(reaped) != 0 {
		t.Fatalf("stale target = %v, %v; want no removal", reaped, err)
	}
	replacement := d.containers[InstanceName(devScope, evA)]
	if replacement == nil || replacement.id == oldID || !replacement.running {
		t.Fatalf("replacement lost: %+v", replacement)
	}
}

// TestReapOrphans_LeaseAndGrace keeps pending work and newly created instances untouched.
func TestReapOrphans_LeaseAndGrace(t *testing.T) {
	f, d := seedCleanupFleet(t, evA, evB)
	d.containers[InstanceName(devScope, evB)].created = time.Now().Unix()
	if reaped, err := f.ReapOrphans(context.Background(), map[uuid.UUID]bool{evA: true}, 30*time.Second); err != nil || len(reaped) != 0 || len(d.removals) != 0 {
		t.Fatalf("protected sweep = %v, %v, removals=%v", reaped, err, d.removals)
	}
}

// TestReapOrphans_Cancellation stops issuing new destructive calls after its activity expires.
func TestReapOrphans_Cancellation(t *testing.T) {
	f, d := seedCleanupFleet(t, evA, evB)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	f.cli = &cleanupDocker{dockerClient: d, remove: func(context.Context, string, container.RemoveOptions) error {
		attempts++
		cancel()
		return ctx.Err()
	}}
	_, err := f.ReapOrphans(ctx, nil, 0)
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("canceled sweep: attempts=%d error=%v", attempts, err)
	}
}

// TestReapOrphans_RechecksOwnership refuses a listed ID whose scope proof changed.
func TestReapOrphans_RechecksOwnership(t *testing.T) {
	for _, change := range []string{"label", "network"} {
		t.Run(change, func(t *testing.T) {
			f, d := seedCleanupFleet(t, evA)
			f.cli = &cleanupDocker{dockerClient: d, list: func(ctx context.Context, opts container.ListOptions) ([]types.Container, error) {
				rows, err := d.ContainerList(ctx, opts)
				for i := range rows {
					rows[i].Labels = maps.Clone(rows[i].Labels)
				}
				c := d.containers[InstanceName(devScope, evA)]
				if change == "label" {
					c.labels[labelScope] = prodScope
				} else {
					delete(c.networks, devScope)
				}
				return rows, err
			}}
			if reaped, err := f.ReapOrphans(context.Background(), nil, 0); err == nil || len(reaped) != 0 || len(d.removals) != 0 {
				t.Fatalf("ownership changed: reaped=%v error=%v removals=%v", reaped, err, d.removals)
			}
		})
	}
}
