// Package livefeed hosts the Temporal activities that publish found-footy's
// live-feed NATS messages — the single "announce" boundary the workflows call
// AFTER a durable change commits. Workflows can't do NATS I/O directly (no
// side-effects in workflow code), so every emit of the 3 subjects goes through
// an activity here: PublishEventUpdate (this file) and PublishFixtureBatch
// (N5). Keeping them in one struct means the NatsPublisher has exactly one
// caller boundary. See decisions.md 2026-08-14.
package livefeed

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/contract/fixturepresentation"
	"github.com/vedantadhobley/found-footy/internal/infra/event"
)

// publisher is the NATS-producer subset these activities need. Satisfied by
// *event.NatsPublisher; an interface so tests inject a fake without a bus.
type publisher interface {
	PublishEventUpdate(eventID uuid.UUID, fixtureID int64) error
	PublishFixtureStatus(fixtures []event.FixtureStatus) error
	PublishFixtureUpdate(fixtureIDs []int64) error
}

// Activities bundles the live-feed publish activities + the publisher. One
// instance per worker, constructed in cmd/worker with the shared NatsPublisher
// and registered like every other activity struct.
type Activities struct {
	Pub publisher
}

// EventUpdateInput names the event whose public projection changed plus its
// parent fixture, which the consumer uses for routing.
type EventUpdateInput struct {
	EventID   uuid.UUID
	FixtureID int64
}

// EventVideoInput preserves the historical Temporal activity payload type.
// Existing histories still schedule PublishEventVideo during replay.
type EventVideoInput = EventUpdateInput

// PublishEventUpdate emits the event.update dirty signal after a durable
// event-local mutation. The consumer refetches the authoritative event.
func (a *Activities) PublishEventUpdate(_ context.Context, in EventUpdateInput) error {
	return a.Pub.PublishEventUpdate(in.EventID, in.FixtureID)
}

// PublishEventVideo preserves the historical Temporal activity name for
// workflows started before FF-085. It intentionally publishes the current
// event.update wire contract; remove it only after those histories age out.
func (a *Activities) PublishEventVideo(ctx context.Context, in EventVideoInput) error {
	return a.PublishEventUpdate(ctx, in)
}

// FixtureStatusEntry is one inline projection in a status batch. The workflow
// carries the shared contract type without importing the NATS adapter.
type FixtureStatusEntry struct {
	FixtureID int64
	fixturepresentation.Projection
}

// FixtureBatchInput is one ActivePoll cycle's disjoint partition: fixtures whose
// inline status projection changed (Statuses) and fixtures requiring an
// authoritative snapshot (UpdateIDs). Either may be empty.
type FixtureBatchInput struct {
	Statuses  []FixtureStatusEntry
	UpdateIDs []int64
}

// PublishFixtureBatch emits both fixture subjects for one poll cycle:
// fixture.status (inline projection) + fixture.update (ids to refetch).
// Best-effort at the caller, but both publishes are attempted and any error is
// returned so Temporal retries — a re-published batch is harmless (a re-tick or
// a re-signal the consumer refetches idempotently).
func (a *Activities) PublishFixtureBatch(_ context.Context, in FixtureBatchInput) error {
	statuses := make([]event.FixtureStatus, 0, len(in.Statuses))
	for _, projection := range in.Statuses {
		statuses = append(statuses, event.FixtureStatus{
			FixtureID:  projection.FixtureID,
			Projection: projection.Projection,
		})
	}
	return errors.Join(
		a.Pub.PublishFixtureStatus(statuses),
		a.Pub.PublishFixtureUpdate(in.UpdateIDs),
	)
}
