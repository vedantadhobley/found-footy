// Package discovery owns the stable data contracts shared across discovery
// orchestration, activities, persistence, and workflow-spawning call sites.
package discovery

import (
	"time"

	"github.com/google/uuid"
)

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
