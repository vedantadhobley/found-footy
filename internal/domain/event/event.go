// Package event is the domain layer for match events (goals, cards,
// substitutions, VAR reviews). Pure Go — no HTTP, no DB, no Temporal.
// Mirrors the schema's events + event_*_workflows tables.
//
// An event's identity has two components:
//   - id (UUID) — internal, primary key, referenced by video_shares
//   - natural_key (TEXT) — "{team_id}_{player_id}_{type}_{seq}",
//     UNIQUE per fixture. This is what prevents concurrent
//     MonitorWorkflows from double-inserting the same detected goal.
//
// Both are set at construction and immutable. Player refinement
// (unknown → known scorer) is handled by orchestration removing the
// old row + inserting a new one, NOT by mutating natural_key in place.
package event

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"

	"github.com/google/uuid"
)

// Type is the API-Football event classification. Matches the event_type
// enum in the schema. API-Football sends lowercase (subst); ingest-side
// normalization title-cases before hitting the domain.
type Type string

const (
	TypeGoal  Type = "Goal"
	TypeCard  Type = "Card"
	TypeSubst Type = "Subst"
	TypeVar   Type = "Var"
)

// Valid reports whether t is one of the known event types.
func (t Type) Valid() bool {
	switch t {
	case TypeGoal, TypeCard, TypeSubst, TypeVar:
		return true
	}
	return false
}

// TrackableEventType classifies an already-decoded API-Football event
// by its typed Type + Detail + free-text Comments, returning the
// domain event.Type to store OR the zero-value + false if we don't
// track this event.
//
// The Type + Detail params are the canonical enum values produced by
// apifootball.APIEventType / APIEventDetail — normalized at wire
// unmarshal so this function's switches use `==` on typed constants,
// no case-normalization needed.
//
// Whitelist per docs/api-football/events-shape.md:
//
//   Goal + detail in {Normal Goal, Penalty, Own Goal} + no
//     "Penalty Shootout" in comments → TypeGoal
//   Card + detail = Red card                              → TypeCard
//
// Everything else (yellow cards, substitutions, VAR events, missed
// penalties, penalty-shootout goals, or unknown enum values) → skip.
func TrackableEventType(apiType apifootball.APIEventType, apiDetail apifootball.APIEventDetail, apiComments string) (Type, bool) {
	switch apiType {
	case apifootball.EventTypeGoal:
		if strings.Contains(strings.ToLower(apiComments), "penalty shootout") {
			return "", false
		}
		switch apiDetail {
		case apifootball.DetailNormalGoal,
			apifootball.DetailPenalty,
			apifootball.DetailOwnGoal:
			return TypeGoal, true
		}
	case apifootball.EventTypeCard:
		if apiDetail == apifootball.DetailRedCard {
			return TypeCard, true
		}
	}
	return "", false
}

// RemovalReason captures why an event was marked removed. Matches the
// removal_reason enum in the schema.
type RemovalReason string

const (
	RemovalVAR       RemovalReason = "var"        // VAR reversed the call
	RemovalPolicy    RemovalReason = "policy"     // manual policy decision
	RemovalAssetGone RemovalReason = "asset_gone" // underlying asset deleted
)

// Valid reports whether r is one of the known removal reasons.
func (r RemovalReason) Valid() bool {
	switch r {
	case RemovalVAR, RemovalPolicy, RemovalAssetGone:
		return true
	}
	return false
}

// Team is a minimal team reference — just the fields events carry.
type Team struct {
	ID   int
	Name string
}

// Player is a possibly-unknown player reference. The API sometimes
// reports a goal with player.id = null before identifying the scorer;
// both fields are pointers to represent that legitimately-nullable
// state.
type Player struct {
	ID   *int
	Name *string
}

// Known returns true when both ID and Name are populated. Callers gate
// on this before starting the DiscoveryWorkflow — no point searching
// Twitter for "goal by unknown player".
func (p Player) Known() bool {
	return p.ID != nil && p.Name != nil
}

// Event is the domain type. Fields align with the events table for
// straightforward scanner mapping in the pg adapter. Nothing here
// depends on pgx or SQL.
type Event struct {
	ID         uuid.UUID
	FixtureID  int64
	NaturalKey string // immutable after construction

	Type   Type
	Detail apifootball.APIEventDetail // canonical enum value from the vendor
	Team   Team
	Player Player
	Minute int
	Extra  *int

	FirstSeenAt time.Time

	// Symmetric debounce counter, 0..3. Seeded to 1 on Insert.
	// RegisterEventPresence increments (cap 3). RegisterEventAbsence
	// decrements (floor 0). On hitting 0 the repo atomically soft-
	// deletes (sets Removed + RemovedReason + RemovedAt).
	DebounceCount int
	// DownstreamTriggered flips FALSE→TRUE exactly once, the moment
	// DebounceCount first reaches 3. Never flipped back — the workflows
	// it spawns run to their own completion regardless of subsequent
	// debounce oscillation.
	DownstreamTriggered bool

	// Legacy monitor_complete — kept during the O2 transition. New
	// callers use DownstreamTriggered.
	MonitorComplete  bool
	DownloadComplete bool // 10 download workflows registered
	Removed          bool
	RemovedReason    *RemovalReason
	RemovedAt        *time.Time

	// Telemetry is the JSONB column carrying per-event counters +
	// failure-class summaries. Kept as map[string]any to stay flexible
	// while the shape evolves.
	Telemetry map[string]any

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ComposeNaturalKey builds the natural_key string. Format:
// "{team_id}_{player_id}_{type}_{seq}". Unknown player renders as
// "unknown" — differentiating pre-identification events from
// post-identification ones for the "player refinement" flow.
//
// seq is the per-(fixture, team, player, type) sequence — 1 for the
// first goal by this player, 2 for the second, etc. Assigned by the
// caller (usually orchestration counting existing events + 1).
func ComposeNaturalKey(teamID int, playerID *int, t Type, seq int) string {
	pid := "unknown"
	if playerID != nil {
		pid = strconv.Itoa(*playerID)
	}
	return fmt.Sprintf("%d_%s_%s_%d", teamID, pid, t, seq)
}

// New constructs a fresh Event with a new UUID + composed natural_key.
// seq is passed in because computing it requires knowing what other
// events exist in the fixture — that's a repo concern, not a domain
// concern. Callers do `count := repo.CountForNaturalKeyPrefix(...); e := New(..., count+1)`.
func New(
	fixtureID int64,
	team Team,
	player Player,
	eventType Type,
	detail apifootball.APIEventDetail,
	minute int,
	extra *int,
	seq int,
	at time.Time,
) *Event {
	utc := at.UTC()
	return &Event{
		ID:          uuid.New(),
		FixtureID:   fixtureID,
		NaturalKey:  ComposeNaturalKey(team.ID, player.ID, eventType, seq),
		Type:        eventType,
		Detail:      detail,
		Team:        team,
		Player:      player,
		Minute:      minute,
		Extra:       extra,
		FirstSeenAt: utc,
		CreatedAt:   utc,
		UpdatedAt:   utc,
	}
}
