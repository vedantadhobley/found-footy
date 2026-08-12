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

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/firefoxfleet"
)

// Activities bundles the fleet dependency. Constructed at worker startup;
// Fleet is nil when the fleet is disabled.
type Activities struct {
	Fleet *firefoxfleet.Fleet
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
