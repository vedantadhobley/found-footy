// phase.go — the event's semantic lifecycle phase: the API-boundary truth the
// frontend renders WITHOUT re-deriving pipeline state. This is the layer-2
// seam from vedanta-systems' docs/design.md "Data contracts" — internal
// mechanism (edw.completed_at, outcome_class, the legacy monitor/download
// flags) never crosses the API; the frontend consumes this derived meaning
// as-is. See decisions.md 2026-08-13 (event phase contract).
package event

// Phase is the derived, stable meaning of where an event sits in the discovery
// pipeline. Deliberately coarse: "has clips" is NOT a phase — clips surface
// incrementally during `searching` and persist into `complete`, so the video
// count is an ORTHOGONAL axis the frontend reads separately, never a phase
// signal.
type Phase string

const (
	// PhaseDetected — the goal exists but discovery hasn't started. Either
	// debouncing toward the trigger (known scorer) or never to be searched
	// (unknown scorer). The nil player distinguishes the two; debounce_count
	// carries the "confirming N/3" progress. No separate `unsearched` phase.
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
