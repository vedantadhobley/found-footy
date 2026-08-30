// Package livefeed hosts the Temporal activities that publish found-footy's
// live-feed NATS messages — the single "announce" boundary the workflows call
// AFTER a durable change commits. Workflows can't do NATS I/O directly (no
// side-effects in workflow code), so every emit of the 3 subjects goes through
// an activity here: PublishEventVideo (this file, N3) and PublishFixtureBatch
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
	PublishEventVideo(eventID uuid.UUID, fixtureID int64) error
	PublishFixtureStatus(fixtures []event.FixtureStatus) error
	PublishFixtureUpdate(fixtureIDs []int64) error
}

// Activities bundles the live-feed publish activities + the publisher. One
// instance per worker, constructed in cmd/worker with the shared NatsPublisher
// and registered like every other activity struct.
type Activities struct {
	Pub publisher
}

// EventVideoInput names the event whose surfaced clip set changed + its parent
// fixture (routing, so the consumer knows which fixture to splice it into).
type EventVideoInput struct {
	EventID   uuid.UUID
	FixtureID int64
}

// PublishEventVideo emits the event.video dirty-signal for one event. The
// EventWorkflow pipeline calls it AFTER a promote/supersede has durably
// committed a clip-set change, so a consumer that refetches on the signal
// always sees the new state. Best-effort by design — the caller ignores the
// result — but the activity still returns the publish error so Temporal's
// retry policy gets a couple of cheap attempts before the signal is dropped
// (a dropped event.video heals on the frontend's next refetch).
func (a *Activities) PublishEventVideo(_ context.Context, in EventVideoInput) error {
	return a.Pub.PublishEventVideo(in.EventID, in.FixtureID)
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
