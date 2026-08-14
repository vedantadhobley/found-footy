// NatsPublisher is the found-footy live-feed producer: it stamps the
// standard envelope around each of the 3 subjects' payloads and ships
// them on the core NATS bus. It is the NATS half of the old Composer,
// extracted so event_log (the audit plane) and NATS (the live-fanout
// plane) are independent. Publish metrics live on the nats.Conn layer;
// this type does not double-count. See decisions.md 2026-08-14.
package event

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/nats"
)

// NatsPublisher wraps a nats.Conn + the producer's source identity.
// Concurrent-safe: nats.Conn is safe for concurrent use and source is
// immutable after construction.
type NatsPublisher struct {
	conn   *nats.Conn
	source string
}

// NewPublisher constructs a NatsPublisher. Both arguments are required:
// conn is the live bus connection; source is this deployment's envelope
// identity (found-footy-dev / found-footy-prod), from EventConfig.
func NewPublisher(conn *nats.Conn, source string) (*NatsPublisher, error) {
	if conn == nil {
		return nil, fmt.Errorf("event.NewPublisher: nats.Conn is required")
	}
	if source == "" {
		return nil, fmt.Errorf("event.NewPublisher: source is required (EVENT_SOURCE)")
	}
	return &NatsPublisher{conn: conn, source: source}, nil
}

// PublishFixtureClock emits SubjectFixtureClock for the fixtures whose
// minute advanced this cycle. An empty batch is a no-op (a frozen clock —
// half-time / pre-kickoff — emits nothing), NOT an error.
func (p *NatsPublisher) PublishFixtureClock(fixtures []FixtureClock) error {
	if len(fixtures) == 0 {
		return nil
	}
	return p.publish(SubjectFixtureClock, FixtureClockPayload{Fixtures: fixtures})
}

// PublishFixtureUpdate emits SubjectFixtureUpdate for the fixtures that
// changed structurally this cycle. Ids are deduped to satisfy the
// contract's uniqueItems; an empty batch is a no-op.
func (p *NatsPublisher) PublishFixtureUpdate(fixtureIDs []int64) error {
	ids := dedupeInt64(fixtureIDs)
	if len(ids) == 0 {
		return nil
	}
	return p.publish(SubjectFixtureUpdate, FixtureUpdatePayload{FixtureIDs: ids})
}

// PublishEventVideo emits SubjectEventVideo for one event whose clip set
// / rank changed. fixtureID routes the consumer to the parent fixture.
func (p *NatsPublisher) PublishEventVideo(eventID uuid.UUID, fixtureID int64) error {
	return p.publish(SubjectEventVideo, EventVideoPayload{EventID: eventID, FixtureID: fixtureID})
}

// publish stamps the envelope, marshals it, and ships it on the bus.
// Marshal failure returns before touching the bus (nothing published); a
// bus failure is surfaced by nats.Conn.Publish (which also meters + logs
// it). A nil error means the message was handed to NATS.
func (p *NatsPublisher) publish(subject Subject, payload any) error {
	b, err := encodeEnvelope(p.source, subject, payload)
	if err != nil {
		return err
	}
	return p.conn.Publish(subject.String(), b)
}

// encodeEnvelope builds + JSON-encodes an enveloped message. Free
// function (not a method) so tests validate the wire bytes against the
// committed schemas without a live bus. Guards against an unknown subject
// (a typo that would otherwise ship to a dead subject).
func encodeEnvelope(source string, subject Subject, payload any) ([]byte, error) {
	if !subject.Valid() {
		return nil, fmt.Errorf("event: unknown subject %q", subject)
	}
	b, err := json.Marshal(newEnvelope(source, subject, payload))
	if err != nil {
		return nil, fmt.Errorf("event: marshal envelope for %s: %w", subject, err)
	}
	return b, nil
}

// dedupeInt64 returns xs with duplicates removed, preserving first-seen
// order. Satisfies the fixture.update contract's uniqueItems without
// pushing the burden onto callers.
func dedupeInt64(xs []int64) []int64 {
	if len(xs) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(xs))
	out := make([]int64, 0, len(xs))
	for _, x := range xs {
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}
