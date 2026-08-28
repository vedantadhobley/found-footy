// Typed JSON payloads for durable semantic-transition audit rows.
package auditlog

import (
	"time"

	"github.com/google/uuid"
)

// FixtureActivatedPayload describes staging to active.
type FixtureActivatedPayload struct {
	FixtureID   int64     `json:"fixture_id"`
	ActivatedAt time.Time `json:"activated_at"`
	Reason      string    `json:"reason"`
}

// FixtureCompletedPayload describes active to completed and its evidence.
type FixtureCompletedPayload struct {
	FixtureID                int64     `json:"fixture_id"`
	TerminalObservedAt       time.Time `json:"terminal_observed_at"`
	CompletedAt              time.Time `json:"completed_at"`
	GraceSeconds             int64     `json:"grace_seconds"`
	ProviderScoreEventParity *bool     `json:"provider_score_event_parity"`
	DurableScoreEventParity  *bool     `json:"durable_score_event_parity"`
	PenaltyResultDecided     *bool     `json:"penalty_result_decided"`
}

// EventDetectedPayload describes the first vote for a known-player event.
type EventDetectedPayload struct {
	EventID    uuid.UUID `json:"event_id"`
	FixtureID  int64     `json:"fixture_id"`
	EventType  string    `json:"event_type"`
	Detail     string    `json:"detail"`
	Minute     int       `json:"minute"`
	Extra      *int      `json:"extra,omitempty"`
	PlayerName string    `json:"player_name"`
	TeamID     int64     `json:"team_id"`
	TeamName   string    `json:"team_name"`
	Counter    int       `json:"counter"`
}

// EventStablePayload describes the downstream-trigger transition.
type EventStablePayload struct {
	EventID    uuid.UUID `json:"event_id"`
	FixtureID  int64     `json:"fixture_id"`
	EventType  string    `json:"event_type"`
	Detail     string    `json:"detail"`
	Minute     int       `json:"minute"`
	Extra      *int      `json:"extra,omitempty"`
	PlayerName string    `json:"player_name"`
	TeamID     int64     `json:"team_id"`
	TeamName   string    `json:"team_name"`
}

// EventRemovedPayload describes a debounce-zero soft removal.
type EventRemovedPayload struct {
	EventID   uuid.UUID `json:"event_id"`
	FixtureID int64     `json:"fixture_id"`
	RemovedAt time.Time `json:"removed_at"`
	Reason    string    `json:"reason"`
}
