// candidate.go — durable evidence and lifecycle vocabulary for one Twitter
// video candidate observed by EventWorkflow.
package discovery

import "github.com/google/uuid"

// CandidateEvidence is the immutable search evidence required to process,
// explain, and recover one candidate. The workflow owns this value from the
// moment Twitter returns it and passes the same value to observation and
// terminal persistence.
type CandidateEvidence struct {
	EventID               uuid.UUID
	FixtureID             int64
	SearchAttempt         int
	Query                 string
	TweetURL              string
	TweetText             string
	VideoPageURL          string
	DurationSeconds       float64
	Username              string
	AgeMinutesAtDiscovery float64
}

// CandidateState describes workflow ownership independently of the final
// outcome class. A replacement execution restores durable pending rows as
// observed, marks them in-flight when re-driven, and excludes terminal rows.
type CandidateState string

const (
	CandidateObserved CandidateState = "observed"
	CandidateInFlight CandidateState = "in_flight"
	CandidateTerminal CandidateState = "terminal"
)
