// Package fleet holds the Temporal activities that provision and release
// per-event Firefox instances (#160), thin wrappers over
// internal/infra/firefoxfleet. Registered on the worker; called by the
// monitor (provision at debounce count=1, release on pre-trigger decay)
// and the EventWorkflow (release on finish) + #172 VAR cancel-cleanup.
//
// A nil Fleet means the per-event-instance path is disabled
// (FIREFOXFLEET_ENABLED=false): every activity becomes a safe no-op that
// returns an empty address, so callers transparently fall back to the
// single shared twitter service. This lets the fleet ship dark and flip
// on once verified.
package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/firefoxfleet"
)

// liveEventsLister returns the events that should currently hold a fleet
// instance — the reaper's "keep" set. Satisfied by *pg.EventRepo.
type liveEventsLister interface {
	ListLiveFleetEventIDs(ctx context.Context) ([]uuid.UUID, error)
}

// Activities bundles the fleet dependency. Constructed at worker startup;
// Fleet is nil when the fleet is disabled. LiveEvents backs the reaper (nil
// when the fleet is off → ReapOrphanedFirefox no-ops).
type Activities struct {
	Fleet      *firefoxfleet.Fleet
	LiveEvents liveEventsLister
}

// InstanceAddr re-exports firefoxfleet.InstanceAddr — the deterministic,
// registry-free per-event address — so the EventWorkflow derives its
// instance URL without importing infra directly. Pure function of the
// event ID, so it is determinism-safe inside a workflow.
func InstanceAddr(eventID uuid.UUID) string {
	return firefoxfleet.InstanceAddr(eventID)
}

// ProvisionFirefoxInput identifies the event whose instance to warm.
type ProvisionFirefoxInput struct {
	EventID uuid.UUID
}

// ProvisionFirefoxOutput carries the instance address. Empty when the
// fleet is disabled — callers derive the address themselves via
// firefoxfleet.InstanceAddr, so an empty return just means "use the
// shared service."
type ProvisionFirefoxOutput struct {
	Address string
}

// ProvisionFirefox ensures a warm, healthy instance for the event and
// returns its address. Idempotent (safe under Temporal retry): a
// re-provision of a live instance returns the same address.
func (a *Activities) ProvisionFirefox(ctx context.Context, in ProvisionFirefoxInput) (ProvisionFirefoxOutput, error) {
	if a.Fleet == nil {
		return ProvisionFirefoxOutput{}, nil
	}
	addr, err := a.Fleet.Provision(ctx, in.EventID)
	if err != nil {
		return ProvisionFirefoxOutput{}, fmt.Errorf("fleet.ProvisionFirefox: %w", err)
	}
	return ProvisionFirefoxOutput{Address: addr}, nil
}

// ReleaseFirefoxInput identifies the event whose instance to tear down.
type ReleaseFirefoxInput struct {
	EventID uuid.UUID
}

// ReleaseFirefox stops + removes the event's instance. Idempotent — a
// missing instance is success, so the happy-path release, the decay
// release, and the VAR cancel-cleanup can all call it without racing.
func (a *Activities) ReleaseFirefox(ctx context.Context, in ReleaseFirefoxInput) error {
	if a.Fleet == nil {
		return nil
	}
	if err := a.Fleet.Release(ctx, in.EventID); err != nil {
		return fmt.Errorf("fleet.ReleaseFirefox: %w", err)
	}
	return nil
}

// ReapOrphanedFirefoxInput bounds the sweep. MinAgeSecs is the grace so a
// just-provisioned instance whose event has not yet landed in the DB is never
// reaped.
type ReapOrphanedFirefoxInput struct {
	MinAgeSecs int
}

// ReapOrphanedFirefoxOutput reports the instances swept this pass.
type ReapOrphanedFirefoxOutput struct {
	Reaped []string
}

// ReapOrphanedFirefox stop+rms fleet instances whose event is no longer live —
// crash-orphans (provisioned, worker died before release) and failed releases
// (exited-but-not-removed). Queries the live-event set, diffs it against the
// labeled containers, releases the strays. No-op when the fleet is disabled or
// no lister is wired. audit P0-5 / #183.
func (a *Activities) ReapOrphanedFirefox(ctx context.Context, in ReapOrphanedFirefoxInput) (ReapOrphanedFirefoxOutput, error) {
	if a.Fleet == nil || a.LiveEvents == nil {
		return ReapOrphanedFirefoxOutput{}, nil
	}
	ids, err := a.LiveEvents.ListLiveFleetEventIDs(ctx)
	if err != nil {
		return ReapOrphanedFirefoxOutput{}, fmt.Errorf("fleet.ReapOrphanedFirefox: live events: %w", err)
	}
	live := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		live[id] = true
	}
	reaped, err := a.Fleet.ReapOrphans(ctx, live, time.Duration(in.MinAgeSecs)*time.Second)
	if err != nil {
		return ReapOrphanedFirefoxOutput{}, fmt.Errorf("fleet.ReapOrphanedFirefox: %w", err)
	}
	return ReapOrphanedFirefoxOutput{Reaped: reaped}, nil
}
