// Audit event kinds and their scope rules.
package auditlog

// Kind is the semantic type stamped on an event_log row.
type Kind string

const (
	// KindFixtureActivated records staging to active.
	KindFixtureActivated Kind = "fixture.activated"
	// KindFixtureCompleted records active to completed.
	KindFixtureCompleted Kind = "fixture.completed"
	// KindEventDetected records the first known-player debounce vote.
	KindEventDetected Kind = "event.detected"
	// KindEventStable records the first downstream-trigger crossing.
	KindEventStable Kind = "event.stable"
	// KindEventRemoved records the first debounce-zero removal transition.
	KindEventRemoved Kind = "event.removed"
)

// String returns the event_log.event_type value.
func (k Kind) String() string { return string(k) }

// Valid reports whether k is a current durable transition kind.
func (k Kind) Valid() bool {
	switch k {
	case KindFixtureActivated, KindFixtureCompleted,
		KindEventDetected, KindEventStable, KindEventRemoved:
		return true
	default:
		return false
	}
}

// EventScoped reports whether the record must reference an event.
func (k Kind) EventScoped() bool {
	switch k {
	case KindEventDetected, KindEventStable, KindEventRemoved:
		return true
	default:
		return false
	}
}
