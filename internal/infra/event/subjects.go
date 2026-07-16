// Kind enum + subject helpers for the semantic event bus. Kind
// serves two roles simultaneously: (1) the NATS subject name used for
// fan-out, and (2) the event_log.event_type column value used for the
// durable audit trail. That coupling is deliberate — one string, one
// truth. See decisions.md 2026-07-01 (workspace NATS as event bus) +
// 2026-07-02 (metadata plane; bytes never on NATS).
package event

// Kind is the semantic event kind. Every Publish() call names one.
// Consumers subscribe to a specific Kind or to a pattern like
// "event.>" (all event kinds) or "fixture.>" (both fixture kinds).
type Kind string

const (
	// KindFixtureActivated fires when Ingest or Monitor flips a
	// fixture from staging → active. Payload: FixtureActivatedPayload.
	KindFixtureActivated Kind = "fixture.activated"

	// KindFixtureCompleted fires when Monitor's FixtureReadyToComplete
	// gate opens and the fixture transitions active → completed.
	// Payload: FixtureCompletedPayload.
	KindFixtureCompleted Kind = "fixture.completed"

	// KindEventDetected fires on every fresh insert into events (the
	// first debounce vote for a natural key). Payload:
	// EventDetectedPayload.
	KindEventDetected Kind = "event.detected"

	// KindEventStable fires the cycle an event's debounce counter
	// first crosses to downstream_triggered=true. Discovery (once O3
	// ships) is spawned atomically alongside the flag flip; this
	// emission is for external consumers (SSE, webhooks).
	// Payload: EventStablePayload.
	KindEventStable Kind = "event.stable"

	// KindEventRemoved fires the cycle an event's counter hits 0 and
	// the row is soft-deleted (removed=true). Consumers should treat
	// this as "revoke everything you knew about this event."
	// Payload: EventRemovedPayload.
	KindEventRemoved Kind = "event.removed"

	// KindEventRankRecalculated fires when video_shares ranking for an
	// event changes — new upload, replacement, or popularity bump
	// promotes a different share to rank 1. Payload:
	// EventRankRecalculatedPayload.
	KindEventRankRecalculated Kind = "event.rank_recalculated"
)

// String satisfies fmt.Stringer for logging + assertion contexts.
func (k Kind) String() string { return string(k) }

// Valid reports whether k is one of the registered kinds. Used by
// Composer.Publish to fail fast on typos; also useful for consumer
// validation of incoming NATS messages.
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

// AllKinds returns every Kind currently registered. Test helpers +
// consumer registration code can iterate this to subscribe to all
// event families without duplicating the constant list.
func AllKinds() []Kind {
	return []Kind{
		KindFixtureActivated,
		KindFixtureCompleted,
		KindEventDetected,
		KindEventStable,
		KindEventRemoved,
		KindEventRankRecalculated,
	}
}
