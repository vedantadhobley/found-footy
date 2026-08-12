// fleet.go — the per-event Firefox fleet provisioner (#160). Drives the
// Docker API (socket mounted into the worker) to create/start one
// twitter/Firefox container per active event and stop/rm it on release.
//
// Instances are named deterministically from the event ID, so any caller
// derives an instance's address without a registry — the design's
// "no router / no registry" property (twitter-scaling.md). Every
// lifecycle op is idempotent: the happy-path release, the monitor's
// decay release, and #172's VAR cancel-cleanup may all try to release the
// same instance, and Provision may be retried by Temporal.
package firefoxfleet

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/config"
)

const (
	// instancePort — the twitter service's fixed listen port (:8888).
	instancePort = "8888"
	// labelFleet / labelEvent tag provisioned containers so the cap count
	// and the (future) reaper can find fleet members without guessing at
	// names.
	labelFleet = "found-footy.fleet"
	labelEvent = "found-footy.fleet.event"
)

// Fleet provisions + releases per-event Firefox instances via Docker.
type Fleet struct {
	cli *client.Client
	cfg config.FirefoxFleetConfig
}

// New builds a Fleet. The Docker client reads DOCKER_HOST from env
// (default unix:///var/run/docker.sock — mounted into the worker); API
// version is negotiated so it works across daemon versions.
func New(cfg config.FirefoxFleetConfig) (*Fleet, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("firefoxfleet.New: docker client: %w", err)
	}
	return &Fleet{cli: cli, cfg: cfg}, nil
}

// Close releases the underlying Docker client.
func (f *Fleet) Close() error { return f.cli.Close() }

// InstanceName is the deterministic container name for an event. Both
// Provision (monitor, at debounce count=1) and the EventWorkflow (which
// derives the address at count=3) compute the same value — that is how
// the model avoids a registry.
func InstanceName(eventID uuid.UUID) string {
	return "ff-firefox-ev-" + eventID.String()[:8]
}

// InstanceAddr is the base URL the worker uses to reach the event's
// instance over the shared docker network (container-name DNS). Pure
// function of the event ID — derivable without provisioning.
func InstanceAddr(eventID uuid.UUID) string {
	return "http://" + InstanceName(eventID) + ":" + instancePort
}

// Provision ensures a running, healthy instance for eventID and returns
// its address. Idempotent: an already-running instance returns its
// address without a second create. Blocks-and-waits (bounded by ctx /
// the activity timeout) when the fleet is at MaxInstances.
func (f *Fleet) Provision(ctx context.Context, eventID uuid.UUID) (string, error) {
	name := InstanceName(eventID)
	addr := InstanceAddr(eventID)

	// Idempotent fast path: already exists → ensure started + healthy.
	if id, ok, err := f.find(ctx, name); err != nil {
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
		},
	}
	host := &container.HostConfig{
		// Shared cookie file, RW — the fleet self-refreshes cookies via
		// atomic-write + fingerprint + mtime coordination (cookies_backup.go).
		Binds: []string{f.cfg.CookieHostPath + ":/config/twitter_cookies.json"},
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
			f.cfg.Network: {},
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
	name := InstanceName(eventID)
	id, ok, err := f.find(ctx, name)
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

// find inspects a container by name; returns (id, exists, err). A
// not-found daemon error is (·, false, nil), not an error.
func (f *Fleet) find(ctx context.Context, name string) (string, bool, error) {
	insp, err := f.cli.ContainerInspect(ctx, name)
	if err != nil {
		if client.IsErrNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("firefoxfleet.find %s: %w", name, err)
	}
	return insp.ID, true, nil
}

// count returns the number of live fleet containers (by label), the cap
// source of truth — robust across worker restarts (no in-process counter
// to desync from reality).
func (f *Fleet) count(ctx context.Context) (int, error) {
	list, err := f.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", labelFleet+"=firefox")),
	})
	if err != nil {
		return 0, fmt.Errorf("firefoxfleet.count: %w", err)
	}
	return len(list), nil
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
