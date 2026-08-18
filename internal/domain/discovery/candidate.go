// candidate.go — durable lifecycle vocabulary for one Twitter video candidate.
package discovery

// CandidateState describes workflow ownership independently of the final
// outcome class. A replacement execution restores durable pending rows as
// observed, marks them in-flight when re-driven, and excludes terminal rows.
type CandidateState string

const (
	CandidateObserved CandidateState = "observed"
	CandidateInFlight CandidateState = "in_flight"
	CandidateTerminal CandidateState = "terminal"
)
