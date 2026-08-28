// Package discovery owns the stable data contracts shared across discovery
// orchestration, activities, persistence, and workflow-spawning call sites.
package discovery

import (
	"time"

	"github.com/google/uuid"
)

// CandidateOutcome is the terminal class for one discovered video candidate.
// It lives in the shared contract package because workflows, activities, and
// atomic placement persistence all write the same durable vocabulary.
type CandidateOutcome string

const (
	OutcomePromoted   CandidateOutcome = "promoted"
	OutcomeDuplicate  CandidateOutcome = "duplicate"
	OutcomeSuperseded CandidateOutcome = "superseded"
	OutcomeRejected   CandidateOutcome = "rejected"
	OutcomeFailed     CandidateOutcome = "failed"
)

// Terminal reports whether o is a schema-valid terminal candidate outcome.
func (o CandidateOutcome) Terminal() bool {
	switch o {
	case OutcomePromoted, OutcomeDuplicate, OutcomeSuperseded, OutcomeRejected, OutcomeFailed:
		return true
	default:
		return false
	}
}

// Credited reports whether o represents a candidate sighting assigned to a
// durable video asset. Superseded candidates retain their vote on the winner.
func (o CandidateOutcome) Credited() bool {
	switch o {
	case OutcomePromoted, OutcomeDuplicate, OutcomeSuperseded:
		return true
	default:
		return false
	}
}

// EventWorkflowInput carries the immutable event context supplied by the
// spawning monitor activity to EventWorkflow.
type EventWorkflowInput struct {
	EventID     uuid.UUID `json:"event_id"`
	FixtureID   int64     `json:"fixture_id"`
	PlayerName  string    `json:"player_name"`
	TeamName    string    `json:"team_name"`
	TeamID      int64     `json:"team_id"`
	Minute      int       `json:"minute"`        // API elapsed at the event.
	Extra       *int      `json:"extra"`         // Stoppage extra; nil outside stoppage.
	FirstSeenAt time.Time `json:"first_seen_at"` // Vendor observation time; zero in pre-FF-050 histories.
}

// CandidateEvidence is the immutable search evidence required to process,
// explain, and recover one candidate. EventWorkflow passes the same value to
// observation and terminal persistence.
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
