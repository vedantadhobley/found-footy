// storage_test.go pins disposable-profile cleanup across fleet lifecycle paths.
package firefoxfleet

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// TestFleet_ProfileStorageLifecycle requires every removal path to clean old
// anonymous profiles while provisioning only the shared cookie-directory bind.
func TestFleet_ProfileStorageLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failStart    bool
		recoverStart bool
		reap         bool
	}{
		{name: "normal release"},
		{name: "orphan reap", reap: true},
		{name: "failed start release", failStart: true},
		{name: "failed start reap", failStart: true, reap: true},
		{name: "start retry then release", failStart: true, recoverStart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			daemon := newFakeDocker()
			cfg := fleetConfig(devScope)
			f, err := newWithClient(cfg, daemon)
			if err != nil {
				t.Fatal(err)
			}
			startErr := errors.New("start failed")
			if tc.failStart {
				daemon.startErr = startErr
			}
			_, err = f.Provision(ctx, evA)
			if tc.failStart && !errors.Is(err, startErr) || !tc.failStart && err != nil {
				t.Fatalf("Provision: %v", err)
			}
			name := InstanceName(devScope, evA)
			created := daemon.containers[name]
			wantBind := filepath.Dir(cfg.CookieHostPath) + ":/config"
			if created == nil || len(created.binds) != 1 || created.binds[0] != wantBind {
				t.Fatalf("provision must bind only shared cookies: %#v", created)
			}
			if tc.recoverStart {
				daemon.startErr = nil
				if _, err := f.Provision(ctx, evA); err != nil {
					t.Fatal(err)
				}
				if daemon.nextID != 1 || !created.running {
					t.Fatal("start retry must reuse the stopped container")
				}
			}
			if tc.reap {
				reaped, err := f.ReapOrphans(ctx, map[uuid.UUID]bool{}, 0)
				if err != nil || len(reaped) != 1 || reaped[0] != name {
					t.Fatalf("ReapOrphans = %v, %v", reaped, err)
				}
			} else if err := f.Release(ctx, evA); err != nil {
				t.Fatal(err)
			}
			if err := f.Release(ctx, evA); err != nil {
				t.Fatalf("repeat release: %v", err)
			}
			if len(daemon.removals) != 1 || !daemon.removals[0].RemoveVolumes || !daemon.removals[0].Force {
				t.Fatalf("removal options = %+v, want one complete removal", daemon.removals)
			}
			if len(daemon.containers) != 0 {
				t.Fatal("owned event container survived release")
			}
		})
	}
}

// TestFleet_ProfileRemovalFailureRetriable keeps the container available for
// an explicit retry when Docker cannot complete its removal.
func TestFleet_ProfileRemovalFailureRetriable(t *testing.T) {
	ctx := context.Background()
	daemon := newFakeDocker()
	f, err := newWithClient(fleetConfig(devScope), daemon)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Provision(ctx, evA); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("remove failed")
	daemon.removeErr = wantErr
	if err := f.Release(ctx, evA); !errors.Is(err, wantErr) {
		t.Fatalf("Release = %v, want removal failure", err)
	}
	if len(daemon.containers) != 1 {
		t.Fatal("failed removal lost the retry target")
	}
	daemon.removeErr = nil
	if err := f.Release(ctx, evA); err != nil {
		t.Fatal(err)
	}
	if len(daemon.removals) != 2 || !daemon.removals[0].RemoveVolumes || !daemon.removals[1].RemoveVolumes {
		t.Fatalf("retry lost volume cleanup: %+v", daemon.removals)
	}
}
