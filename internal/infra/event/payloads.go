// Typed payload structs — one per Kind. Payloads are JSON-serialized
// straight into event_log.payload (JSONB) and included inside the NATS
// envelope wrapped by Composer.Publish. Field names use snake_case per
// the SSE-consumer contract (SSE clients are browser JavaScript, JSON
// stays snake_case for consistency with pg output).
//
// Every payload here is intentionally minimal. If a consumer needs
// richer state, it should fetch from pg by (event_id / fixture_id /
// share_id) rather than expanding these structs — payload bloat costs
// NATS bandwidth AND JSONB size.
package event

import (
	"time"

	"github.com/google/uuid"
)

// FixtureActivatedPayload — staging → active transition.
type FixtureActivatedPayload struct {
	FixtureID   int64     `json:"fixture_id"`
	ActivatedAt time.Time `json:"activated_at"`
	// Reason: "kickoff_soon" (Monitor's ActivateUpcoming activity) or
	// "already_started" (Ingest saw kickoff already passed).
	Reason string `json:"reason"`
}

// FixtureCompletedPayload — active → completed transition.
type FixtureCompletedPayload struct {
	FixtureID   int64     `json:"fixture_id"`
	CompletedAt time.Time `json:"completed_at"`
}

// EventDetectedPayload — first vote for a natural key. Fires on every
// fresh insert into events. Consumers typically ignore this (waiting
// for stable) but SSE surfaces it as a "pending" event badge.
type EventDetectedPayload struct {
	EventID    uuid.UUID `json:"event_id"`
	FixtureID  int64     `json:"fixture_id"`
	EventType  string    `json:"event_type"` // domain classification (goal / red card / missed penalty / ...)
	Detail     string    `json:"detail"`
	Minute     int       `json:"minute"`
	Extra      *int      `json:"extra,omitempty"`
	PlayerName string    `json:"player_name"`
	TeamID     int64     `json:"team_id"`
	TeamName   string    `json:"team_name"`
	Counter    int       `json:"counter"` // debounce counter at time of emit (1 on first)
}

// EventStablePayload — event just crossed downstream_triggered=true.
// Same shape as detected minus the counter (which is always at
// threshold when this fires). Discovery spawn (once O3 ships) is
// atomic with the pg flag flip; this emission is for external
// consumers only.
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

// EventRemovedPayload — event's counter hit 0 and the row was
// soft-deleted (removed=true). Consumers should treat as "revoke
// everything you knew about this event." The destroy pipeline (cancel
// in-flight Discovery/Video/Asset workflows, mark video_shares
// removed) is a follow-up phase after V.
type EventRemovedPayload struct {
	EventID   uuid.UUID `json:"event_id"`
	FixtureID int64     `json:"fixture_id"`
	RemovedAt time.Time `json:"removed_at"`
	// Reason: "debounce_zero" (currently the only path).
	Reason string `json:"reason"`
}

// EventRankRecalculatedPayload — video_shares ranking for an event
// changed. Fires when a new upload, replacement, or popularity bump
// promotes a different share to rank 1. Consumers may re-fetch
// /events/{id}/shares to get the fresh order.
type EventRankRecalculatedPayload struct {
	EventID    uuid.UUID `json:"event_id"`
	FixtureID  int64     `json:"fixture_id"`
	TopShareID string    `json:"top_share_id"`  // video_shares.id of the new rank-1 share
	ShareCount int       `json:"share_count"`   // total non-removed video_shares for this event
}
