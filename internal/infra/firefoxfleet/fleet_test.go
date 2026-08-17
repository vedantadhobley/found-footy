// fleet_test.go — tests for the per-event Firefox fleet (#160).
//
// Two tiers:
//   - Pure-function tests (InstanceName/InstanceAddr) run always. They pin
//     the registry-free contract: the name/address are a deterministic
//     function of the event ID, so the monitor (provision at count=1) and
//     the EventWorkflow (derive address at count=3) compute the same value.
//   - Socket-backed tests (New + find/count/Release wiring) run only with a
//     real Docker daemon and are skipped under -short. They exercise the
//     Docker API plumbing — not-found → success, label counting, idempotent
//     release-on-missing — WITHOUT spinning a real Firefox container. The
//     full Provision→healthy→Release round-trip stays a live-verification
//     step (Firefox startup is ~30s and flaky; it does not belong in unit CI).
package firefoxfleet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/google/uuid"
	specs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/vedantadhobley/found-footy/internal/config"
)

// fixed event IDs so the determinism assertions don't depend on randomness.
var (
	evA = uuid.MustParse("11111111-2222-3333-4444-555555555555")
	evB = uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
)

const (
	devScope  = "found-footy-dev"
	prodScope = "found-footy-prod"
)

// TestInstanceName_Deterministic pins daemon-name ownership: scope and the
// full event ID both participate, while repeated input remains stable.
func TestInstanceName_Deterministic(t *testing.T) {
	got1 := InstanceName(devScope, evA)
	got2 := InstanceName(devScope, evA)
	if got1 != got2 {
		t.Fatalf("InstanceName not deterministic: %q != %q", got1, got2)
	}
	if !strings.HasPrefix(got1, "ff-firefox-"+devScope+"-ev-") {
		t.Errorf("InstanceName %q missing scoped prefix", got1)
	}
	if !strings.HasSuffix(got1, evA.String()) {
		t.Errorf("InstanceName %q should end in full event ID %q", got1, evA)
	}
	if same := InstanceName(devScope, evB); same == got1 {
		t.Errorf("distinct events produced identical names: %q", same)
	}
	if same := InstanceName(prodScope, evA); same == got1 {
		t.Errorf("distinct scopes produced identical names: %q", same)
	}
}

// TestInstanceAddr pins the address shape the worker dials over the shared
// network (container-name DNS + fixed :8888), derivable without provisioning.
func TestInstanceAddr(t *testing.T) {
	addr := InstanceAddr(evA)
	want := "http://" + InstanceAlias(evA) + ":8888"
	if addr != want {
		t.Errorf("InstanceAddr = %q, want %q", addr, want)
	}
	// Same purity guarantee as the name.
	if InstanceAddr(evA) != InstanceAddr(evA) {
		t.Error("InstanceAddr not deterministic")
	}
}

// TestValidateScope rejects values that cannot be embedded losslessly in a
// Docker container name. Silent sanitization could collapse two scopes.
func TestValidateScope(t *testing.T) {
	for _, scope := range []string{"", "found footy dev", "/found-footy-dev", strings.Repeat("a", 64)} {
		if err := validateScope(scope); err == nil {
			t.Errorf("validateScope(%q) = nil, want error", scope)
		}
	}
	for _, scope := range []string{devScope, prodScope, "preview_12.test"} {
		if err := validateScope(scope); err != nil {
			t.Errorf("validateScope(%q) = %v", scope, err)
		}
	}
}

// fakeContainer is the minimum daemon state needed to exercise fleet
// ownership. A single fakeDocker is shared by dev and prod Fleet objects.
type fakeContainer struct {
	id       string
	name     string
	labels   map[string]string
	networks map[string]*network.EndpointSettings
	created  int64
	running  bool
}

// fakeDocker implements the fleet's narrow Docker API without a socket.
type fakeDocker struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	nextID     int
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{containers: make(map[string]*fakeContainer)}
}

func (d *fakeDocker) lookup(ref string) (*fakeContainer, bool) {
	if c, ok := d.containers[ref]; ok {
		return c, true
	}
	for _, c := range d.containers {
		if c.id == ref {
			return c, true
		}
	}
	return nil, false
}

func (d *fakeDocker) ContainerCreate(_ context.Context, cfg *container.Config, _ *container.HostConfig, netcfg *network.NetworkingConfig, _ *specs.Platform, name string) (container.CreateResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.containers[name]; exists {
		return container.CreateResponse{}, errdefs.Conflict(fmt.Errorf("container %s exists", name))
	}
	d.nextID++
	labels := make(map[string]string, len(cfg.Labels))
	for k, v := range cfg.Labels {
		labels[k] = v
	}
	networks := make(map[string]*network.EndpointSettings, len(netcfg.EndpointsConfig))
	for name, settings := range netcfg.EndpointsConfig {
		copySettings := *settings
		copySettings.Aliases = append([]string(nil), settings.Aliases...)
		networks[name] = &copySettings
	}
	id := fmt.Sprintf("fake-%d", d.nextID)
	d.containers[name] = &fakeContainer{
		id: id, name: name, labels: labels, networks: networks,
		created: time.Now().Add(-time.Minute).Unix(),
	}
	return container.CreateResponse{ID: id}, nil
}

func (d *fakeDocker) ContainerInspect(_ context.Context, ref string) (types.ContainerJSON, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.lookup(ref)
	if !ok {
		return types.ContainerJSON{}, errdefs.NotFound(errors.New("container not found"))
	}
	labels := make(map[string]string, len(c.labels))
	for k, v := range c.labels {
		labels[k] = v
	}
	return types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: c.id, Name: "/" + c.name},
		Config:            &container.Config{Labels: labels},
		NetworkSettings:   &types.NetworkSettings{Networks: c.networks},
	}, nil
}

func (d *fakeDocker) ContainerList(_ context.Context, opts container.ListOptions) ([]types.Container, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []types.Container
	for _, c := range d.containers {
		if !opts.All && !c.running {
			continue
		}
		if !opts.Filters.MatchKVList("label", c.labels) {
			continue
		}
		matchesNetwork := len(opts.Filters.Get("network")) == 0
		for _, wanted := range opts.Filters.Get("network") {
			if c.networks[wanted] != nil {
				matchesNetwork = true
				break
			}
		}
		if !matchesNetwork {
			continue
		}
		state := "exited"
		if c.running {
			state = "running"
		}
		out = append(out, types.Container{
			ID: c.id, Names: []string{"/" + c.name}, Created: c.created,
			Labels: c.labels, State: state,
			NetworkSettings: &types.SummaryNetworkSettings{Networks: c.networks},
		})
	}
	return out, nil
}

func (d *fakeDocker) ContainerStart(_ context.Context, ref string, _ container.StartOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.lookup(ref)
	if !ok {
		return errdefs.NotFound(errors.New("container not found"))
	}
	c.running = true
	return nil
}

func (d *fakeDocker) ContainerStop(_ context.Context, ref string, _ container.StopOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.lookup(ref)
	if !ok {
		return errdefs.NotFound(errors.New("container not found"))
	}
	c.running = false
	return nil
}

func (d *fakeDocker) ContainerRemove(_ context.Context, ref string, _ container.RemoveOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.lookup(ref)
	if !ok {
		return errdefs.NotFound(errors.New("container not found"))
	}
	delete(d.containers, c.name)
	return nil
}

func (d *fakeDocker) Close() error { return nil }

func fleetConfig(scope string) config.FirefoxFleetConfig {
	return config.FirefoxFleetConfig{
		Image:            "found-footy-dev-twitter:latest",
		Network:          scope,
		CookieHostPath:   "/home/vedanta/.config/found-footy/twitter_cookies.json",
		InstanceMemLimit: 2 << 30,
		MaxInstances:     16,
		HealthTimeout:    45 * time.Second,
	}
}

// TestFleet_IsolatesComposeNetworks reproduces FF-001: two fleets share one
// daemon and even the same event ID, but capacity, listing, reaping, release,
// names, and DNS ownership remain network-scoped.
func TestFleet_IsolatesComposeNetworks(t *testing.T) {
	ctx := context.Background()
	daemon := newFakeDocker()
	dev, err := newWithClient(fleetConfig(devScope), daemon)
	if err != nil {
		t.Fatal(err)
	}
	prod, err := newWithClient(fleetConfig(prodScope), daemon)
	if err != nil {
		t.Fatal(err)
	}

	devAddr, err := dev.Provision(ctx, evA)
	if err != nil {
		t.Fatalf("dev Provision: %v", err)
	}
	prodAddr, err := prod.Provision(ctx, evA)
	if err != nil {
		t.Fatalf("prod Provision: %v", err)
	}
	if devAddr != prodAddr || devAddr != InstanceAddr(evA) {
		t.Fatalf("network-local address changed: dev=%q prod=%q", devAddr, prodAddr)
	}
	if InstanceName(devScope, evA) == InstanceName(prodScope, evA) {
		t.Fatal("daemon-global names collide across scopes")
	}

	for scope, f := range map[string]*Fleet{devScope: dev, prodScope: prod} {
		n, countErr := f.count(ctx)
		if countErr != nil || n != 1 {
			t.Fatalf("%s count = %d, %v; want 1, nil", scope, n, countErr)
		}
		instances, listErr := f.ListInstances(ctx)
		if listErr != nil || len(instances) != 1 {
			t.Fatalf("%s instances = %v, %v; want one", scope, instances, listErr)
		}
		if instances[0].Name != InstanceName(scope, evA) {
			t.Fatalf("%s listed foreign name %q", scope, instances[0].Name)
		}
		created := daemon.containers[InstanceName(scope, evA)]
		if created == nil || created.networks[scope] == nil || len(created.networks[scope].Aliases) != 1 || created.networks[scope].Aliases[0] != InstanceAlias(evA) {
			t.Fatalf("%s network alias not preserved: %#v", scope, created)
		}
	}

	reaped, err := dev.ReapOrphans(ctx, map[uuid.UUID]bool{}, 0)
	if err != nil {
		t.Fatalf("dev ReapOrphans: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != InstanceName(devScope, evA) {
		t.Fatalf("dev reaped %v, want only its own container", reaped)
	}
	if n, err := prod.count(ctx); err != nil || n != 1 {
		t.Fatalf("prod count after dev reap = %d, %v; want 1, nil", n, err)
	}
	if err := prod.Release(ctx, evA); err != nil {
		t.Fatalf("prod Release: %v", err)
	}
}

// TestFleet_RefusesForeignOwnership ensures a global name collision never
// grants this fleet permission to start or remove a container it did not own.
func TestFleet_RefusesForeignOwnership(t *testing.T) {
	ctx := context.Background()
	daemon := newFakeDocker()
	f, err := newWithClient(fleetConfig(devScope), daemon)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Provision(ctx, evA); err != nil {
		t.Fatal(err)
	}
	name := InstanceName(devScope, evA)
	daemon.containers[name].labels[labelScope] = prodScope

	if err := f.Release(ctx, evA); err == nil {
		t.Fatal("Release adopted a same-named container with foreign ownership labels")
	}
	if daemon.containers[name] == nil {
		t.Fatal("Release removed a same-named foreign container")
	}
}

// testFleet builds a dev-network-scoped Fleet against the ambient Docker
// daemon, skipping when unavailable (no socket / -short). These tests only
// inspect or release random nonexistent IDs; they never provision a container.
func testFleet(t *testing.T) *Fleet {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping socket-backed fleet test in -short mode")
	}
	f, err := New(fleetConfig(devScope))
	if err != nil {
		t.Skipf("no docker daemon available: %v", err)
	}
	// Probe the daemon; skip (don't fail) when the socket isn't mounted so
	// the suite stays green on hosts without Docker.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.count(ctx); err != nil {
		_ = f.Close()
		t.Skipf("docker daemon not reachable: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestFleet_FindMissing verifies the not-found path is success, not error —
// the invariant every idempotent lifecycle op leans on.
func TestFleet_FindMissing(t *testing.T) {
	f := testFleet(t)
	ctx := context.Background()
	_, ok, err := f.find(ctx, uuid.New())
	if err != nil {
		t.Fatalf("find on missing container errored: %v", err)
	}
	if ok {
		t.Error("find reported a never-provisioned container as existing")
	}
}

// TestFleet_ReleaseIdempotent verifies releasing a never-provisioned event
// is a no-op success — the happy-path, VAR cancel-cleanup, and monitor-decay
// release paths all rely on double-release being harmless.
func TestFleet_ReleaseIdempotent(t *testing.T) {
	f := testFleet(t)
	ctx := context.Background()
	ev := uuid.New()
	if err := f.Release(ctx, ev); err != nil {
		t.Fatalf("Release on missing container should be nil, got %v", err)
	}
	// Second release — still nil.
	if err := f.Release(ctx, ev); err != nil {
		t.Fatalf("double Release should be nil, got %v", err)
	}
}

// TestFleet_CountNonNegative sanity-checks the label-filtered count wiring
// (the cap's source of truth) returns a usable number against a real daemon.
func TestFleet_CountNonNegative(t *testing.T) {
	f := testFleet(t)
	n, err := f.count(context.Background())
	if err != nil {
		t.Fatalf("count errored: %v", err)
	}
	if n < 0 {
		t.Errorf("count returned negative: %d", n)
	}
}
