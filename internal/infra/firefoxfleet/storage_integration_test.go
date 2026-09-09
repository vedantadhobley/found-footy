// storage_integration_test.go verifies Docker volume deletion, not just API flags.
package firefoxfleet

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
)

// TestFleet_RemovesOnlyAnonymousStorage models an old image's implicit /data
// volume beside explicit named storage. All resources belong to this test;
// no application image, credential directory, or deployment network is used.
func TestFleet_RemovesOnlyAnonymousStorage(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated Docker resources")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Ping(ctx); err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	// This is the already-pulled test toolchain image, not a second runtime
	// dependency. Never pull an image implicitly during a storage regression.
	const image = "golang:1.25.11-bookworm"
	if _, _, err := d.ImageInspectWithRaw(ctx, image); err != nil {
		t.Fatalf("test toolchain image unavailable: %v", err)
	}
	scope := "ff-storage-test-" + uuid.NewString()
	net, err := d.NetworkCreate(ctx, scope, network.CreateOptions{Internal: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := d.NetworkRemove(cleanup, net.ID); err != nil {
			t.Errorf("remove test network: %v", err)
		}
	})
	manual, err := d.VolumeCreate(ctx, volume.CreateOptions{Name: scope + "-manual-profile"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := d.VolumeRemove(cleanup, manual.Name, false); err != nil {
			t.Errorf("remove test named volume: %v", err)
		}
	})
	eventID := uuid.New()
	created, err := d.ContainerCreate(ctx, &container.Config{
		Image: image, Entrypoint: []string{"sleep", "300"},
		Volumes: map[string]struct{}{"/data": {}},
		Labels:  map[string]string{labelFleet: "firefox", labelScope: scope, labelEvent: eventID.String()},
	}, &container.HostConfig{
		Binds:          []string{manual.Name + ":/manual-profile"},
		Resources:      container.Resources{Memory: 128 << 20, NanoCPUs: 1_000_000_000},
		ReadonlyRootfs: true, CapDrop: []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
	}, &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{scope: {}},
	}, nil, InstanceName(scope, eventID))
	if err != nil {
		t.Fatal(err)
	}
	var anonymous string
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := d.ContainerRemove(cleanup, created.ID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !client.IsErrNotFound(err) {
			t.Errorf("remove test container: %v", err)
		}
		// Release may have leaked the now-detached test volume if this
		// regression failed. Delete only the exact name captured below.
		if anonymous != "" {
			if err := d.VolumeRemove(cleanup, anonymous, false); err != nil && !client.IsErrNotFound(err) {
				t.Errorf("remove test anonymous volume: %v", err)
			}
		}
	})
	if err := d.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspected, err := d.ContainerInspect(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range inspected.Mounts {
		if m.Destination == "/data" {
			anonymous = m.Name
		}
	}
	if anonymous == "" || anonymous == manual.Name {
		t.Fatal("test did not create a distinct anonymous profile volume")
	}
	f, err := newWithClient(fleetConfig(scope), d)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Release(ctx, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.VolumeInspect(ctx, anonymous); !client.IsErrNotFound(err) {
		t.Fatalf("anonymous profile survived release: %v", err)
	}
	if _, err := d.VolumeInspect(ctx, manual.Name); err != nil {
		t.Fatalf("explicit named storage did not survive: %v", err)
	}
	if err := f.Release(ctx, eventID); err != nil {
		t.Fatalf("repeat release: %v", err)
	}
}
