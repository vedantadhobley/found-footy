// outcome.go — typed classification for how a downstream workflow
// (Discovery, Video, Asset, …) ended, stored in
// event_downstream_workflows.outcome_class. The column stays TEXT (that
// table is deliberately pluggable — workflow_type is free TEXT too), but
// call sites use these constants instead of magic strings, mirroring the
// TEXT-backed-but-Go-typed pattern of apifootball.APIStatusCode.
package event

// DownstreamOutcome is the terminal classification of a downstream
// workflow run. Add new values here as workflow types land.
type DownstreamOutcome string

const (
	// OutcomeSuccess — completed and produced a usable result.
	OutcomeSuccess DownstreamOutcome = "success"
	// OutcomeNoCandidates — completed cleanly but found nothing.
	OutcomeNoCandidates DownstreamOutcome = "no_candidates"
	// OutcomeEventRemoved — the event was VAR-removed while the workflow
	// was still pending; the run is abandoned and its checklist row
	// closed so fixture completion isn't blocked waiting on a discovery
	// for an event that no longer exists.
	OutcomeEventRemoved DownstreamOutcome = "event_removed"
	// OutcomeStubOK — placeholder outcome from the pre-production
	// Discovery stub path.
	OutcomeStubOK DownstreamOutcome = "stub_ok"
)

// Valid reports whether o is a known outcome.
func (o DownstreamOutcome) Valid() bool {
	switch o {
	case OutcomeSuccess, OutcomeNoCandidates, OutcomeEventRemoved, OutcomeStubOK:
		return true
	}
	return false
}
