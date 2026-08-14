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

	"github.com/vedantadhobley/found-footy/internal/infra/event"
)

// publisher is the NATS-producer subset these activities need. Satisfied by
// *event.NatsPublisher; an interface so tests inject a fake without a bus.
type publisher interface {
	PublishEventVideo(eventID uuid.UUID, fixtureID int64) error
	PublishFixtureClock(fixtures []event.FixtureClock) error
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

// FixtureClockEntry is one live clock tick in a batch — the activity-layer
// mirror of event.FixtureClock, kept here so the ActivePoll workflow can build
// the batch without importing infra (activities are the boundary).
type FixtureClockEntry struct {
	FixtureID int64
	Minute    int
	Extra     *int
}

// FixtureBatchInput is one ActivePoll cycle's disjoint partition: fixtures whose
// only change was the clock (Clock) and fixtures with a structural change
// (UpdateIDs). Either may be empty; the publisher skips an empty subject.
type FixtureBatchInput struct {
	Clock     []FixtureClockEntry
	UpdateIDs []int64
}

// PublishFixtureBatch emits both fixture subjects for one poll cycle:
// fixture.clock (the inline ticks) + fixture.update (the ids to bulk-refetch).
// Best-effort at the caller, but both publishes are attempted and any error is
// returned so Temporal retries — a re-published batch is harmless (a re-tick or
// a re-signal the consumer refetches idempotently).
func (a *Activities) PublishFixtureBatch(_ context.Context, in FixtureBatchInput) error {
	clock := make([]event.FixtureClock, 0, len(in.Clock))
	for _, c := range in.Clock {
		clock = append(clock, event.FixtureClock{FixtureID: c.FixtureID, Minute: c.Minute, Extra: c.Extra})
	}
	return errors.Join(
		a.Pub.PublishFixtureClock(clock),
		a.Pub.PublishFixtureUpdate(in.UpdateIDs),
	)
}
