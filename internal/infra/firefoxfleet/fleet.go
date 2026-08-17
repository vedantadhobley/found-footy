// fleet.go — the per-event Firefox fleet provisioner (#160). Drives the
// Docker API (socket mounted into the worker) to create/start one
// twitter/Firefox container per active event and stop/rm it on release.
//
// Compose selects an explicit Docker network for each stack. The provisioner
// uses that network as an opaque ownership scope for daemon-global names,
// labels, capacity, and cleanup; it never branches on dev/prod. Workflows keep
// deriving an event-only network alias, preserving the design's "no router /
// no registry" property (twitter-scaling.md). Every lifecycle op is
// idempotent: the happy-path release, the monitor's decay release, and #172's
// VAR cancel-cleanup may all try to release the same instance, and Provision
// may be retried by Temporal.
package firefoxfleet

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	specs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/vedantadhobley/found-footy/internal/config"
)

const (
	// instancePort — the twitter service's fixed listen port (:8888).
	instancePort = "8888"
	// labelFleet / labelEvent tag provisioned containers so the cap count
	// and reaper can find fleet members without guessing at names. labelScope
	// is the Compose-selected Docker network: an opaque deployment partition,
	// not an application-level dev/prod branch.
	labelFleet = "found-footy.fleet"
	labelEvent = "found-footy.fleet.event"
	labelScope = "found-footy.fleet.scope"
)

var containerScopePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

// dockerClient is the narrow Docker API surface the fleet owns. Keeping this
// seam local makes cross-scope lifecycle behavior testable without a daemon.
type dockerClient interface {
	ContainerCreate(context.Context, *container.Config, *container.HostConfig, *network.NetworkingConfig, *specs.Platform, string) (container.CreateResponse, error)
	ContainerInspect(context.Context, string) (types.ContainerJSON, error)
	ContainerList(context.Context, container.ListOptions) ([]types.Container, error)
	ContainerRemove(context.Context, string, container.RemoveOptions) error
	ContainerStart(context.Context, string, container.StartOptions) error
	ContainerStop(context.Context, string, container.StopOptions) error
	Close() error
}

// Fleet provisions + releases per-event Firefox instances via Docker.
type Fleet struct {
	cli dockerClient
	cfg config.FirefoxFleetConfig
}

// New builds a Fleet. The Docker client reads DOCKER_HOST from env
// (default unix:///var/run/docker.sock — mounted into the worker); API
// version is negotiated so it works across daemon versions.
func New(cfg config.FirefoxFleetConfig) (*Fleet, error) {
	if err := validateScope(cfg.Network); err != nil {
		return nil, err
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("firefoxfleet.New: docker client: %w", err)
	}
	return &Fleet{cli: cli, cfg: cfg}, nil
}

// newWithClient builds a Fleet around an injected Docker client. Tests use it
// to model multiple Compose networks sharing one daemon.
func newWithClient(cfg config.FirefoxFleetConfig, cli dockerClient) (*Fleet, error) {
	if err := validateScope(cfg.Network); err != nil {
		return nil, err
	}
	return &Fleet{cli: cli, cfg: cfg}, nil
}

// validateScope ensures the Compose-selected network is safe to embed in a
// daemon-global container name. Refusing ambiguous input is safer than a
// lossy sanitizer that could collapse two deployment scopes.
func validateScope(scope string) error {
	if !containerScopePattern.MatchString(scope) {
		return fmt.Errorf("firefoxfleet.New: network %q is not a safe fleet scope", scope)
	}
	return nil
}

// Close releases the underlying Docker client.
func (f *Fleet) Close() error { return f.cli.Close() }

// InstanceName is the daemon-global container name for one scoped event. The
// network comes from Compose, so two stacks can safely hold the same event ID
// on one Docker daemon.
func InstanceName(scope string, eventID uuid.UUID) string {
	return "ff-firefox-" + scope + "-ev-" + eventID.String()
}

// InstanceAlias is the network-local deterministic hostname workflows use.
// It intentionally preserves the pre-scope address contract: Docker networks
// isolate identical aliases, so Temporal histories need no version migration.
func InstanceAlias(eventID uuid.UUID) string {
	return "ff-firefox-ev-" + eventID.String()[:8]
}

// InstanceAddr is the base URL the worker uses to reach the event's
// instance over its Compose-selected Docker network (network-alias DNS). Pure
// function of the event ID — derivable without provisioning.
func InstanceAddr(eventID uuid.UUID) string {
	return "http://" + InstanceAlias(eventID) + ":" + instancePort
}

// Provision ensures a running, healthy instance for eventID and returns
// its address. Idempotent: an already-running instance returns its
// address without a second create. Blocks-and-waits (bounded by ctx /
// the activity timeout) when the fleet is at MaxInstances.
func (f *Fleet) Provision(ctx context.Context, eventID uuid.UUID) (string, error) {
	name := InstanceName(f.cfg.Network, eventID)
	addr := InstanceAddr(eventID)

	// Idempotent fast path: already exists → ensure started + healthy.
	if id, ok, err := f.find(ctx, eventID); err != nil {
		return "", err
	} else if ok {
		// Already provisioned — ensure started (create-without-start edge)
		// and return. Readiness is covered by the debounce window, not a
		// blocking health wait (which would stall the 30s poll cycle).
		_ = f.cli.ContainerStart(ctx, id, container.StartOptions{})
		return addr, nil
	}

	// Cap: block-and-wait for a free slot (soft cap — the count↔create
	// gap can momentarily overshoot by 1 under concurrent provisions,
	// acceptable for a resource ceiling).
	if err := f.waitForSlot(ctx); err != nil {
		return "", err
	}

	conf := &container.Config{
		Image: f.cfg.Image,
		Env:   []string{"TWITTER_HEADLESS=true"},
		Labels: map[string]string{
			labelFleet: "firefox",
			labelEvent: eventID.String(),
			labelScope: f.cfg.Network,
		},
	}
	host := &container.HostConfig{
		// Shared cookie file, RW — the fleet self-refreshes cookies via
		// atomic-write + fingerprint + mtime coordination (cookies_backup.go).
		// Mount the cookie file's PARENT DIR (not the file) at /config: the
		// cookie backup does an atomic temp-file+rename onto
		// /config/twitter_cookies.json, and rename(2) onto a single-file
		// bind MOUNTPOINT returns EBUSY — so a file mount silently drops every
		// cookie write-back and re-auth. A dir mount keeps the rename inside
		// the mount. Container path (/config/twitter_cookies.json) is unchanged.
		Binds: []string{filepath.Dir(f.cfg.CookieHostPath) + ":/config"},
		// Firefox's user-namespace clone3()/unshare() is blocked by the
		// default seccomp profile (mirrors the compose twitter service).
		SecurityOpt: []string{"seccomp=unconfined"},
		ShmSize:     1 << 30, // 1 GiB — Firefox rendering needs > default 64 MB
		Resources:   container.Resources{Memory: f.cfg.InstanceMemLimit},
		// Ephemeral: no restart. A crash fails the search activity (retried)
		// and the reaper sweeps the container.
		RestartPolicy: container.RestartPolicy{Name: "no"},
		AutoRemove:    false, // Release does stop+rm explicitly (idempotent)
	}
	netcfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			f.cfg.Network: {Aliases: []string{InstanceAlias(eventID)}},
		},
	}

	created, err := f.cli.ContainerCreate(ctx, conf, host, netcfg, nil, name)
	if err != nil {
		return "", fmt.Errorf("firefoxfleet.Provision: create %s: %w", name, err)
	}
	if err := f.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("firefoxfleet.Provision: start %s: %w", name, err)
	}
	// Return as soon as it is started — the container warms in the
	// background (~30s: Firefox launch + cookie load + auth-verify), which
	// hides behind the event's debounce window. Searches at count=3 hit a
	// warm instance (and retry if slightly early). waitHealthy stays as a
	// helper for a future explicit readiness gate.
	return addr, nil
}

// Release stops + removes the event's instance. Idempotent — a missing
// container is success, since the happy path, the VAR cancel-cleanup, and
// the monitor decay path may all attempt release.
func (f *Fleet) Release(ctx context.Context, eventID uuid.UUID) error {
	name := InstanceName(f.cfg.Network, eventID)
	id, ok, err := f.find(ctx, eventID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	timeout := 5
	_ = f.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	if err := f.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("firefoxfleet.Release: rm %s: %w", name, err)
	}
	return nil
}

// find inspects the scope-qualified container name and verifies its ownership
// labels plus network before any lifecycle mutation. A not-found daemon error
// is (·, false, nil), not an error. A same-named foreign container is an error,
// never something this fleet adopts or removes.
func (f *Fleet) find(ctx context.Context, eventID uuid.UUID) (string, bool, error) {
	name := InstanceName(f.cfg.Network, eventID)
	insp, err := f.cli.ContainerInspect(ctx, name)
	if err != nil {
		if client.IsErrNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("firefoxfleet.find %s: %w", name, err)
	}
	if insp.Config == nil ||
		insp.Config.Labels[labelFleet] != "firefox" ||
		insp.Config.Labels[labelScope] != f.cfg.Network ||
		insp.Config.Labels[labelEvent] != eventID.String() {
		return "", false, fmt.Errorf("firefoxfleet.find %s: ownership labels do not match scope %q and event %s", name, f.cfg.Network, eventID)
	}
	if insp.NetworkSettings == nil || insp.NetworkSettings.Networks[f.cfg.Network] == nil {
		return "", false, fmt.Errorf("firefoxfleet.find %s: container is not attached to scope network %q", name, f.cfg.Network)
	}
	return insp.ID, true, nil
}

// listFilters selects only fleet containers owned by this Compose network.
func (f *Fleet) listFilters() filters.Args {
	return filters.NewArgs(
		filters.Arg("label", labelFleet+"=firefox"),
		filters.Arg("label", labelScope+"="+f.cfg.Network),
		filters.Arg("network", f.cfg.Network),
	)
}

// count returns the number of live fleet containers (by label), the cap
// source of truth — robust across worker restarts (no in-process counter
// to desync from reality).
func (f *Fleet) count(ctx context.Context) (int, error) {
	// Running-only (default All:false): a stopped/exited orphan holds no
	// browser + no real slot, so it must NOT count against the cap. The reaper
	// (ListInstances/ReapOrphans) sweeps stopped orphans by label. audit P0-5.
	list, err := f.cli.ContainerList(ctx, container.ListOptions{
		Filters: f.listFilters(),
	})
	if err != nil {
		return 0, fmt.Errorf("firefoxfleet.count: %w", err)
	}
	return len(list), nil
}

// Instance is a live fleet member: its container name, the event it belongs to
// (from the found-footy.fleet.event label), and when it was created.
type Instance struct {
	Name      string
	EventID   uuid.UUID
	CreatedAt time.Time
}

// ListInstances returns every labeled fleet container, running OR stopped
// (All:true — the reaper needs the exited orphans too). Members whose event
// label is missing or unparseable are skipped.
func (f *Fleet) ListInstances(ctx context.Context) ([]Instance, error) {
	list, err := f.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f.listFilters(),
	})
	if err != nil {
		return nil, fmt.Errorf("firefoxfleet.ListInstances: %w", err)
	}
	out := make([]Instance, 0, len(list))
	for _, c := range list {
		if c.Labels[labelScope] != f.cfg.Network {
			continue
		}
		evID, perr := uuid.Parse(c.Labels[labelEvent])
		if perr != nil {
			continue
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, Instance{Name: name, EventID: evID, CreatedAt: time.Unix(c.Created, 0)})
	}
	return out, nil
}

// ReapOrphans stop+rms every fleet instance whose event is NOT in `live` and
// which is older than minAge (the grace — so a just-provisioned instance whose
// event has not hit the DB yet is never reaped). Best-effort + idempotent: a
// failed Release is skipped and retried next sweep. Returns the names reaped.
// Runs in an activity / at startup, never a workflow, so time.Now() is fine.
// audit P0-5 / #183.
func (f *Fleet) ReapOrphans(ctx context.Context, live map[uuid.UUID]bool, minAge time.Duration) ([]string, error) {
	insts, err := f.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-minAge)
	var reaped []string
	for _, in := range insts {
		if live[in.EventID] || in.CreatedAt.After(cutoff) {
			continue
		}
		if err := f.Release(ctx, in.EventID); err != nil {
			continue // best-effort; next sweep retries
		}
		reaped = append(reaped, in.Name)
	}
	return reaped, nil
}

// waitForSlot blocks until the fleet is below MaxInstances or ctx expires.
func (f *Fleet) waitForSlot(ctx context.Context) error {
	for {
		n, err := f.count(ctx)
		if err != nil {
			return err
		}
		if n < f.cfg.MaxInstances {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("firefoxfleet: at cap %d, no slot before deadline: %w", f.cfg.MaxInstances, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// waitHealthy polls the instance's /health (200) until healthy or the
// HealthTimeout elapses. The startup latency is meant to hide behind the
// event's debounce window (the zero-warm insight).
func (f *Fleet) waitHealthy(ctx context.Context, addr string) error {
	deadline := time.Now().Add(f.cfg.HealthTimeout)
	hc := &http.Client{Timeout: 5 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/health", nil)
		if resp, err := hc.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("firefoxfleet.waitHealthy: %s not healthy within %s", addr, f.cfg.HealthTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}
