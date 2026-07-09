// enums.go — typed enum values for the API-Sports fixtures endpoint.
// Three families: fixture status codes (19 values), event types
// (4 values), event details (varies per parent type).
//
// Canonical form matches the vendor doc casing exactly — see
// docs/api-football/vendor/api-football-v3.9.3.pdf for the source
// of truth. Rationale: log lines + debugging tools show what the
// vendor console shows, easy cross-reference during incident triage.
// Yes, that means `CardRed = "Red card"` (lowercase 'c') — the
// vendor writes it that way and we mirror.
//
// Parse functions:
//   1. Accept ANY casing (trim + case-insensitive match to known
//      constants; return canonical form on success).
//   2. Preserve unknown values as-is + return them wrapped so the
//      caller can log + skip rather than fail-hard. Vendor may add
//      new values without notice; ingest continuing is more important
//      than strict validation.
//
// UnmarshalJSON on the wire types (in fixtures.go / teams.go) calls
// these Parse functions so every value hitting domain code is either
// a canonical enum value or an explicitly-marked unknown.
package apifootball

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ── APIStatusCode ──────────────────────────────────────────────

// APIStatusCode is the fixture status short code — 19 documented
// values across 7 API-Sports buckets (Scheduled, In Play, Finished,
// Postponed, Cancelled, Abandoned, Not Played). See
// docs/api-football/status-codes.md for the full enum table.
type APIStatusCode string

const (
	// Scheduled
	StatusTBD           APIStatusCode = "TBD"  // Time To Be Defined
	StatusNotStarted    APIStatusCode = "NS"   // Not Started

	// In Play
	StatusFirstHalf     APIStatusCode = "1H"   // First Half, Kick Off
	StatusHalftime      APIStatusCode = "HT"   // Halftime
	StatusSecondHalf    APIStatusCode = "2H"   // Second Half, 2nd Half Started
	StatusExtraTime     APIStatusCode = "ET"   // Extra Time
	StatusBreakTime     APIStatusCode = "BT"   // Break Time (between periods)
	StatusPenaltyPlay   APIStatusCode = "P"    // Penalty In Progress
	StatusSuspended     APIStatusCode = "SUSP" // Match Suspended
	StatusInterrupted   APIStatusCode = "INT"  // Match Interrupted
	StatusLive          APIStatusCode = "LIVE" // In Progress (rare fallback — no elapsed data)

	// Finished
	StatusFullTime      APIStatusCode = "FT"  // Match Finished (regular)
	StatusAfterExtra    APIStatusCode = "AET" // Match Finished (after extra time)
	StatusPenaltyDone   APIStatusCode = "PEN" // Match Finished (after penalty shootout)

	// Postponed
	StatusPostponed     APIStatusCode = "PST" // Match Postponed (may flip back to NS)

	// Cancelled
	StatusCancelled     APIStatusCode = "CANC" // Match Cancelled

	// Abandoned
	StatusAbandoned     APIStatusCode = "ABD" // Match Abandoned (may or may not reschedule)

	// Not Played
	StatusTechnicalLoss APIStatusCode = "AWD" // Technical Loss
	StatusWalkover      APIStatusCode = "WO"  // WalkOver (forfeit)
)

// knownStatusCodes maps a lowercased vendor string to the canonical
// APIStatusCode. Parse uses this to normalize input.
var knownStatusCodes = map[string]APIStatusCode{
	"tbd":  StatusTBD,
	"ns":   StatusNotStarted,
	"1h":   StatusFirstHalf,
	"ht":   StatusHalftime,
	"2h":   StatusSecondHalf,
	"et":   StatusExtraTime,
	"bt":   StatusBreakTime,
	"p":    StatusPenaltyPlay,
	"susp": StatusSuspended,
	"int":  StatusInterrupted,
	"live": StatusLive,
	"ft":   StatusFullTime,
	"aet":  StatusAfterExtra,
	"pen":  StatusPenaltyDone,
	"pst":  StatusPostponed,
	"canc": StatusCancelled,
	"abd":  StatusAbandoned,
	"awd":  StatusTechnicalLoss,
	"wo":   StatusWalkover,
}

// ParseAPIStatusCode normalizes a raw vendor string to the canonical
// APIStatusCode. Unknown values are preserved as-is (returned as
// APIStatusCode(raw), known=false) so callers can log + continue
// rather than fail. Empty input returns ("", false, nil).
func ParseAPIStatusCode(raw string) (code APIStatusCode, known bool, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, nil
	}
	if v, ok := knownStatusCodes[strings.ToLower(trimmed)]; ok {
		return v, true, nil
	}
	return APIStatusCode(trimmed), false, nil
}

// UnmarshalJSON accepts any casing, normalizes to canonical, and
// preserves unknown values (logging is the caller's job — the
// unmarshal path shouldn't emit).
func (c *APIStatusCode) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("APIStatusCode: %w", err)
	}
	code, _, err := ParseAPIStatusCode(raw)
	if err != nil {
		return err
	}
	*c = code
	return nil
}

// ── APIEventType ───────────────────────────────────────────────

// APIEventType is the "type" field on a fixture event. Four documented
// values per docs/api-football/events-shape.md.
type APIEventType string

const (
	EventTypeGoal  APIEventType = "Goal"
	EventTypeCard  APIEventType = "Card"
	EventTypeSubst APIEventType = "Subst"
	EventTypeVar   APIEventType = "Var"
)

// knownEventTypes maps lowercased vendor strings to canonical types.
// Note: Python's event_config.py uses `subst` lowercase — the vendor
// may send either casing. Parse accepts both, canonical is "Subst".
var knownEventTypes = map[string]APIEventType{
	"goal":  EventTypeGoal,
	"card":  EventTypeCard,
	"subst": EventTypeSubst,
	"var":   EventTypeVar,
}

// ParseAPIEventType normalizes a raw event type string. Unknown
// values preserved as-is.
func ParseAPIEventType(raw string) (t APIEventType, known bool, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, nil
	}
	if v, ok := knownEventTypes[strings.ToLower(trimmed)]; ok {
		return v, true, nil
	}
	return APIEventType(trimmed), false, nil
}

// UnmarshalJSON — see UnmarshalJSON on APIStatusCode.
func (t *APIEventType) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("APIEventType: %w", err)
	}
	parsed, _, err := ParseAPIEventType(raw)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// ── APIEventDetail ─────────────────────────────────────────────

// APIEventDetail is the "detail" field on a fixture event. The valid
// value set depends on the parent APIEventType; see the enum table in
// docs/api-football/events-shape.md.
//
// Subst is special — the vendor sends `"Substitution 1"`,
// `"Substitution 2"`, ... (indexed per team). We collapse the family
// to a single canonical `"Substitution"` constant via prefix-parse;
// the numeric suffix is dropped. We don't currently track subs so
// the loss is inert.
type APIEventDetail string

const (
	// Goal
	DetailNormalGoal     APIEventDetail = "Normal Goal"
	DetailOwnGoal        APIEventDetail = "Own Goal"
	DetailPenalty        APIEventDetail = "Penalty"
	DetailMissedPenalty  APIEventDetail = "Missed Penalty"

	// Card. Vendor DOC says "Red card" (lowercase 'c') but LIVE
	// vendor emission uses "Red Card" (title case). Canonical follows
	// real emission for log-line consistency with the vendor console.
	// Parse still handles both casings.
	DetailYellowCard APIEventDetail = "Yellow Card"
	DetailRedCard    APIEventDetail = "Red Card"

	// Subst — canonical, prefix-parsed. Vendor sends "Substitution 1",
	// "Substitution 2", ... — Parse collapses all of them to this.
	DetailSubstitution APIEventDetail = "Substitution"

	// Var — vendor's exact strings, both lowercase second word.
	DetailGoalCancelled    APIEventDetail = "Goal cancelled"
	DetailPenaltyConfirmed APIEventDetail = "Penalty confirmed"
)

// knownEventDetails maps lowercased vendor strings to canonical
// details. Substitution* is handled separately via prefix.
var knownEventDetails = map[string]APIEventDetail{
	"normal goal":       DetailNormalGoal,
	"own goal":          DetailOwnGoal,
	"penalty":           DetailPenalty,
	"missed penalty":    DetailMissedPenalty,
	"yellow card":       DetailYellowCard,
	"red card":          DetailRedCard,
	"goal cancelled":    DetailGoalCancelled,
	"penalty confirmed": DetailPenaltyConfirmed,
}

// ParseAPIEventDetail normalizes a raw detail string. Handles the
// Substitution N family via prefix-check.
func ParseAPIEventDetail(raw string) (d APIEventDetail, known bool, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, nil
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "substitution") {
		return DetailSubstitution, true, nil
	}
	if v, ok := knownEventDetails[lower]; ok {
		return v, true, nil
	}
	return APIEventDetail(trimmed), false, nil
}

// UnmarshalJSON — see UnmarshalJSON on APIStatusCode.
func (d *APIEventDetail) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("APIEventDetail: %w", err)
	}
	parsed, _, err := ParseAPIEventDetail(raw)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// ── APICardComment ─────────────────────────────────────────────

// APICardComment is the "comments" field on Card events. The vendor
// doc calls this "free-text" but real emission (see decisions.md
// 2026-07-09 real-data audit + docs/api-football/events-shape.md)
// shows it's a discrete enum of foul-reason values.
//
// Empty comment (nil / "") is legal and semantically means "no
// annotation." Parse returns ("", false, nil) for empty input.
type APICardComment string

const (
	CardCommentFoul                  APICardComment = "Foul"
	CardCommentArgument              APICardComment = "Argument"
	CardCommentRoughing              APICardComment = "Roughing"
	CardCommentUnsportsmanlikeConduct APICardComment = "Unsportsmanlike conduct"
	CardCommentSeriousFoul           APICardComment = "Serious foul"
)

var knownCardComments = map[string]APICardComment{
	"foul":                    CardCommentFoul,
	"argument":                CardCommentArgument,
	"roughing":                CardCommentRoughing,
	"unsportsmanlike conduct": CardCommentUnsportsmanlikeConduct,
	"serious foul":            CardCommentSeriousFoul,
}

// ParseAPICardComment normalizes a raw card comment string. Empty
// input is legal (returns "" with known=false, err=nil). Unknown
// non-empty values are preserved with known=false — vendor may add
// new foul reasons over time.
func ParseAPICardComment(raw string) (c APICardComment, known bool, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, nil
	}
	if v, ok := knownCardComments[strings.ToLower(trimmed)]; ok {
		return v, true, nil
	}
	return APICardComment(trimmed), false, nil
}

// ── APIGoalComment ─────────────────────────────────────────────

// APIGoalComment is the "comments" field on Goal events. Currently
// only one known semantic value ("Penalty Shootout") — used to
// filter shootout goals out of match-goal tracking. Vendor uses
// nil / empty on regular-play goals.
//
// The "SubstrPenaltyShootout" naming reflects that Python + Go
// both do a case-insensitive SUBSTRING match against comments,
// not an equality check — vendor might wrap the marker in longer
// text in the future.
type APIGoalComment string

const (
	// GoalCommentPenaltyShootout — vendor marker on Goal events that
	// happened during a penalty shootout (not regular match goals).
	// See HasPenaltyShootoutComment for the load-bearing check.
	GoalCommentPenaltyShootout APIGoalComment = "Penalty Shootout"
)

var knownGoalComments = map[string]APIGoalComment{
	"penalty shootout": GoalCommentPenaltyShootout,
}

// ParseAPIGoalComment normalizes a raw goal comment string. Empty
// input is legal (returns "" with known=false, err=nil).
func ParseAPIGoalComment(raw string) (c APIGoalComment, known bool, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, nil
	}
	if v, ok := knownGoalComments[strings.ToLower(trimmed)]; ok {
		return v, true, nil
	}
	return APIGoalComment(trimmed), false, nil
}

// HasPenaltyShootoutComment reports whether the vendor-provided
// comments field flags this event as a penalty shootout goal.
// Case-insensitive substring match: vendor might embed the marker
// in longer text in the future (currently just emits the bare
// marker as observed in real data).
//
// Used by domain.event.TrackableEventType to filter shootout goals
// out of match-goal tracking.
func HasPenaltyShootoutComment(comments string) bool {
	return strings.Contains(
		strings.ToLower(comments),
		strings.ToLower(string(GoalCommentPenaltyShootout)),
	)
}
