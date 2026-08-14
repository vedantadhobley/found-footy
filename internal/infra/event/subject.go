// Subject is the typed NATS subject set for the found-footy live-feed
// producer — the 3-subject model (fixture.clock, fixture.update,
// event.video) that supersedes the 6 transition-subjects. See
// decisions.md 2026-08-14 + docs/design/proposals/nats-producer-rebuild.md.
package event

// Subject is a fully-qualified NATS subject the found-footy producer
// publishes on. Unlike the legacy Kind (double-duty: NATS subject AND
// event_log.event_type), Subject is NATS-only — the audit plane keeps
// its own semantic types on event_log.event_type.
type Subject string

// subjectPrefix namespaces every found-footy subject with the project
// identifier in hyphen form, matching the repo / network / container
// names. Per decisions.md 2026-08-14 this supersedes the underscore
// `found_footy` prefix once floated on 2026-08-04.
const subjectPrefix = "found-footy."

const (
	// SubjectFixtureClock — batch of live clock ticks: fixtures whose
	// only change this monitor cycle was the match minute advancing.
	// Payload: FixtureClockPayload. Disjoint from SubjectFixtureUpdate
	// per cycle; a frozen clock (half-time / pre-kickoff) emits nothing.
	SubjectFixtureClock Subject = subjectPrefix + "fixture.clock"

	// SubjectFixtureUpdate — batch of fixture ids that changed
	// structurally this cycle (new/removed event, kickoff, FT, score,
	// penalty, winner, status). Payload: FixtureUpdatePayload. The
	// consumer bulk-refetches GET /fixtures?ids=.
	SubjectFixtureUpdate Subject = subjectPrefix + "fixture.update"

	// SubjectEventVideo — one event's surfaced clip set / rank changed.
	// Payload: EventVideoPayload. Emitted per-event by the async
	// downstream (not batched with the monitor cycle); fires regardless
	// of fixture state (a clip can land after the final whistle).
	SubjectEventVideo Subject = subjectPrefix + "event.video"
)

// String satisfies fmt.Stringer + is the value passed to nats Publish.
func (s Subject) String() string { return string(s) }

// Valid reports whether s is one of the 3 registered subjects. Guards
// the publisher against a typo before a message reaches the bus.
func (s Subject) Valid() bool {
	switch s {
	case SubjectFixtureClock, SubjectFixtureUpdate, SubjectEventVideo:
		return true
	default:
		return false
	}
}
