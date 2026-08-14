// Unit test for the live-feed publish activities — the thin passthrough to the
// NatsPublisher. The wire-shape conformance lives in the event package's golden
// tests; here we just prove the activity forwards its input to the publisher.
package livefeed

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/event"
)

// fakePublisher records calls + returns a configurable error.
type fakePublisher struct {
	calls         []EventVideoInput
	clockBatches  [][]event.FixtureClock
	updateBatches [][]int64
	err           error
}

func (f *fakePublisher) PublishEventVideo(eventID uuid.UUID, fixtureID int64) error {
	f.calls = append(f.calls, EventVideoInput{EventID: eventID, FixtureID: fixtureID})
	return f.err
}

func (f *fakePublisher) PublishFixtureClock(fixtures []event.FixtureClock) error {
	f.clockBatches = append(f.clockBatches, fixtures)
	return f.err
}

func (f *fakePublisher) PublishFixtureUpdate(ids []int64) error {
	f.updateBatches = append(f.updateBatches, ids)
	return f.err
}

// TestPublishEventVideoForwards confirms the activity passes event + fixture
// straight through to the publisher.
func TestPublishEventVideoForwards(t *testing.T) {
	f := &fakePublisher{}
	a := &Activities{Pub: f}
	eid := uuid.New()

	if err := a.PublishEventVideo(context.Background(), EventVideoInput{EventID: eid, FixtureID: 1530158}); err != nil {
		t.Fatalf("PublishEventVideo: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0].EventID != eid || f.calls[0].FixtureID != 1530158 {
		t.Fatalf("forwarded call = %+v, want one {%s, 1530158}", f.calls, eid)
	}
}

// TestPublishEventVideoSurfacesError confirms a publisher error propagates (so
// Temporal's retry policy can act on it before the caller drops the signal).
func TestPublishEventVideoSurfacesError(t *testing.T) {
	f := &fakePublisher{err: errors.New("bus down")}
	a := &Activities{Pub: f}

	if err := a.PublishEventVideo(context.Background(), EventVideoInput{EventID: uuid.New(), FixtureID: 42}); err == nil {
		t.Fatal("want error from publisher, got nil")
	}
}

// TestPublishFixtureBatchForwards confirms the batch activity converts the
// activity-layer clock entries to event.FixtureClock and forwards both subjects.
func TestPublishFixtureBatchForwards(t *testing.T) {
	f := &fakePublisher{}
	a := &Activities{Pub: f}
	in := FixtureBatchInput{
		Clock:     []FixtureClockEntry{{FixtureID: 1530158, Minute: 62, Extra: nil}},
		UpdateIDs: []int64{1530162, 1530163},
	}
	if err := a.PublishFixtureBatch(context.Background(), in); err != nil {
		t.Fatalf("PublishFixtureBatch: %v", err)
	}
	if len(f.clockBatches) != 1 || len(f.clockBatches[0]) != 1 ||
		f.clockBatches[0][0].FixtureID != 1530158 || f.clockBatches[0][0].Minute != 62 {
		t.Errorf("clock batch = %+v, want one {1530158, 62}", f.clockBatches)
	}
	if len(f.updateBatches) != 1 || len(f.updateBatches[0]) != 2 {
		t.Errorf("update batch = %+v, want one [1530162 1530163]", f.updateBatches)
	}
}
