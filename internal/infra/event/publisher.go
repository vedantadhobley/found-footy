// NatsPublisher is the found-footy live-feed producer: it stamps the
// standard envelope around each of the 3 subjects' payloads and ships
// them on the core NATS bus. Durable transition audit is committed by the
// Postgres repositories; this package owns only live fanout. Publish metrics
// live on the nats.Conn layer;
// this type does not double-count. See decisions.md 2026-08-14.
package event

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/nats"
)

// NatsPublisher wraps a nats.Conn + the deployment environment, from which both
// the wire subject (found-footy.<env>.<topic>) and the envelope source stamp
// (found-footy-<env>) derive. Concurrent-safe: nats.Conn is safe for concurrent
// use and env/source are immutable after construction.
type NatsPublisher struct {
	conn   *nats.Conn
	source string // envelope stamp: found-footy-<env>
	env    string // wire subject token: found-footy.<env>.<topic>
}

// NewPublisher constructs a NatsPublisher. conn is the live bus connection; env
// is the deployment environment ("dev" / "prod", from EventConfig.Environment)
// — it drives both the subject token and the source stamp.
func NewPublisher(conn *nats.Conn, env string) (*NatsPublisher, error) {
	if conn == nil {
		return nil, fmt.Errorf("event.NewPublisher: nats.Conn is required")
	}
	if env == "" {
		return nil, fmt.Errorf("event.NewPublisher: env is required (EVENT_ENV)")
	}
	return &NatsPublisher{conn: conn, source: projectPrefix + "-" + env, env: env}, nil
}

// PublishFixtureClock emits TopicFixtureClock for the fixtures whose
// minute advanced this cycle. An empty batch is a no-op (a frozen clock —
// half-time / pre-kickoff — emits nothing), NOT an error.
func (p *NatsPublisher) PublishFixtureClock(fixtures []FixtureClock) error {
	if len(fixtures) == 0 {
		return nil
	}
	return p.publish(TopicFixtureClock, FixtureClockPayload{Fixtures: fixtures})
}

// PublishFixtureUpdate emits TopicFixtureUpdate for the fixtures that
// changed structurally this cycle. Ids are deduped to satisfy the
// contract's uniqueItems; an empty batch is a no-op.
func (p *NatsPublisher) PublishFixtureUpdate(fixtureIDs []int64) error {
	ids := dedupeInt64(fixtureIDs)
	if len(ids) == 0 {
		return nil
	}
	return p.publish(TopicFixtureUpdate, FixtureUpdatePayload{FixtureIDs: ids})
}

// PublishEventVideo emits TopicEventVideo for one event whose clip set
// / rank changed. fixtureID routes the consumer to the parent fixture.
func (p *NatsPublisher) PublishEventVideo(eventID uuid.UUID, fixtureID int64) error {
	return p.publish(TopicEventVideo, EventVideoPayload{EventID: eventID, FixtureID: fixtureID})
}

// publish stamps the envelope, marshals it, and ships it on the bus.
// Marshal failure returns before touching the bus (nothing published); a
// bus failure is surfaced by nats.Conn.Publish (which also meters + logs
// it). A nil error means the message was handed to NATS.
func (p *NatsPublisher) publish(topic Topic, payload any) error {
	if !topic.Valid() {
		return fmt.Errorf("event: unknown topic %q", topic)
	}
	wire := topic.Wire(p.env)
	b, err := encodeEnvelope(p.source, wire, payload)
	if err != nil {
		return err
	}
	return p.conn.Publish(wire, b)
}

// encodeEnvelope builds + JSON-encodes an enveloped message around the fully-
// qualified wire subject. Free function (not a method) so tests validate the
// wire bytes against the committed schemas without a live bus. The topic-typo
// guard lives in publish (its only caller with an unvalidated topic).
func encodeEnvelope(source, wireSubject string, payload any) ([]byte, error) {
	b, err := json.Marshal(newEnvelope(source, wireSubject, payload))
	if err != nil {
		return nil, fmt.Errorf("event: marshal envelope for %s: %w", wireSubject, err)
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
