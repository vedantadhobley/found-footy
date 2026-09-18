// phase.go — the event's coarse semantic lifecycle phase. The API retains it
// during the additive consumer cutover; eventpresentation.DeriveState splits
// its ambiguous detected value into unidentified versus confirming. Internal
// mechanism (edw.completed_at, outcome_class, legacy monitor/download flags)
// still never crosses the API. See decisions.md 2026-08-13 and FF-100.
package event

// Phase is the derived, coarse meaning of where an event sits in the discovery
// pipeline. Deliberately coarse: "has clips" is NOT a phase — clips surface
// incrementally during `searching` and persist into `complete`, so the video
// count is an ORTHOGONAL axis the frontend reads separately, never a phase
// signal.
type Phase string

const (
	// PhaseDetected — the event exists but discovery hasn't started. Either
	// debouncing toward the trigger (known scorer) or never to be searched
	// (unknown scorer). New consumers use event presentation_state rather than
	// combining this compatibility value with player nullability.
	PhaseDetected Phase = "detected"
	// PhaseSearching — discovery is in flight (downstream triggered, the
	// discovery workflow not yet complete). Clips may ALREADY be surfaced.
	PhaseSearching Phase = "searching"
	// PhaseComplete — discovery finished. Clips may or may not be present.
	PhaseComplete Phase = "complete"
	// PhaseRemoved — VAR/overturn: the event was soft-removed and its clips
	// revoked. Terminal — wins even after complete.
	PhaseRemoved Phase = "removed"
)

// DerivePhase computes the semantic phase, first-match-wins, from the three
// signals that define pipeline position. Order is load-bearing: `removed`
// wins over everything (an overturn after clips surfaced still reads
// `removed`); then a completed discovery; then an in-flight one; else the goal
// is merely detected. Deliberately does NOT consult outcome_class or the video
// count — "found clips" is the orthogonal videos axis, not a phase.
func DerivePhase(removed, downstreamTriggered, discoveryComplete bool) Phase {
	switch {
	case removed:
		return PhaseRemoved
	case discoveryComplete:
		return PhaseComplete
	case downstreamTriggered:
		return PhaseSearching
	default:
		return PhaseDetected
	}
}
