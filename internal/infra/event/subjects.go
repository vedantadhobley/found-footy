// Kind is the event_log.event_type vocabulary — the semantic type of each
// durable audit row Composer.Publish appends. Per decisions.md 2026-08-14 the
// NATS live-fanout plane moved to Subject + NatsPublisher (subject.go); Kind is
// now event_log-only and is no longer a NATS subject.
package event

// Kind is the semantic type stamped on an event_log row.
type Kind string

const (
	// KindFixtureActivated — a fixture flipped staging → active.
	KindFixtureActivated Kind = "fixture.activated"

	// KindFixtureCompleted — a fixture transitioned active → completed.
	KindFixtureCompleted Kind = "fixture.completed"

	// KindEventDetected — first debounce vote for a natural key (fresh insert).
	KindEventDetected Kind = "event.detected"

	// KindEventStable — an event's debounce counter crossed to
	// downstream_triggered=true.
	KindEventStable Kind = "event.stable"

	// KindEventRemoved — an event's counter hit 0 and the row was soft-deleted.
	KindEventRemoved Kind = "event.removed"

	// KindEventRankRecalculated — an event's video_shares ranking changed.
	KindEventRankRecalculated Kind = "event.rank_recalculated"
)

// String satisfies fmt.Stringer + is the event_log.event_type value.
func (k Kind) String() string { return string(k) }

// Valid reports whether k is one of the registered event_log types.
// Composer.Publish uses it to fail fast on a typo before the INSERT.
func (k Kind) Valid() bool {
	switch k {
	case KindFixtureActivated,
		KindFixtureCompleted,
		KindEventDetected,
		KindEventStable,
		KindEventRemoved,
		KindEventRankRecalculated:
		return true
	default:
		return false
	}
}
