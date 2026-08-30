// subject.go — the topic/subject model for the found-footy live-feed producer.
// A Topic is the env-agnostic message kind (fixture.status, fixture.update,
// event.video); the full NATS subject on the wire is found-footy.<env>.<topic>.
//
// Env is a SUBJECT token, not merely the envelope `source` field — so a consumer
// filters by environment at subscription time (a prod bridge subscribes
// found-footy.prod.> and never sees dev events). One shared bus (NATS is a
// single always-on infra stack, not a per-env product), environments separated
// in the subject. decisions.md 2026-08-15 — supersedes the flat
// found-footy.<topic> + source-field-only scheme of 2026-08-14.
package event

// projectPrefix is the first subject token — the project identifier, matching
// the repo / container / network names.
const projectPrefix = "found-footy"

// Topic is the env-agnostic message kind — the segment the JSON-Schema files
// are named after. It is what's registered + validated; the project + env
// tokens are added at publish time via Wire. Unlike the legacy Kind (double
// duty: NATS subject AND event_log.event_type), Topic is NATS-only.
type Topic string

const (
	// TopicFixtureStatus — batch of complete inline presentation
	// projections. It covers clock movement and status changes that stay within
	// one presentation state. Payload: FixtureStatusPayload. Disjoint from
	// TopicFixtureUpdate per cycle.
	TopicFixtureStatus Topic = "fixture.status"

	// TopicFixtureUpdate — batch of fixture ids whose authoritative snapshot
	// changed this cycle (new/removed event, kickoff, FT, score, result, metadata).
	// Payload: FixtureUpdatePayload. The consumer bulk-refetches GET /fixtures?ids=.
	TopicFixtureUpdate Topic = "fixture.update"

	// TopicEventVideo — one event's surfaced clip set / rank changed. Payload:
	// EventVideoPayload. Emitted per-event by the async downstream (not batched
	// with the monitor cycle); fires regardless of fixture state (a clip can land
	// after the final whistle).
	TopicEventVideo Topic = "event.video"
)

// Wire renders the fully-qualified NATS subject this topic publishes on in the
// given environment: found-footy.<env>.<topic> — e.g.
// found-footy.prod.fixture.update.
func (t Topic) Wire(env string) string {
	return projectPrefix + "." + env + "." + string(t)
}

// String satisfies fmt.Stringer — the bare topic, NOT the wire subject.
func (t Topic) String() string { return string(t) }

// Valid reports whether t is one of the 3 registered topics. Guards the
// publisher against a typo before a message reaches the bus.
func (t Topic) Valid() bool {
	switch t {
	case TopicFixtureStatus, TopicFixtureUpdate, TopicEventVideo:
		return true
	default:
		return false
	}
}
