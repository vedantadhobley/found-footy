// Package auditlog defines the durable semantic-transition records committed
// beside their authoritative Postgres state changes.
package auditlog

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// Record is an immutable, validated event_log row ready for transactional
// persistence. Build records before entering a repository transaction so an
// unencodable payload cannot leave a partial state change.
type Record struct {
	kind      Kind
	eventID   uuid.UUID
	fixtureID int64
	payload   json.RawMessage
}

// New builds and validates one durable audit record.
func New(kind Kind, eventID uuid.UUID, fixtureID int64, payload any) (Record, error) {
	if !kind.Valid() {
		return Record{}, fmt.Errorf("auditlog.New: unknown kind %q", kind)
	}
	if fixtureID == 0 {
		return Record{}, fmt.Errorf("auditlog.New: fixture_id is required for %s", kind)
	}
	if kind.EventScoped() && eventID == uuid.Nil {
		return Record{}, fmt.Errorf("auditlog.New: event_id is required for %s", kind)
	}
	if !kind.EventScoped() && eventID != uuid.Nil {
		return Record{}, fmt.Errorf("auditlog.New: event_id must be empty for %s", kind)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return Record{}, fmt.Errorf("auditlog.New: marshal %s payload: %w", kind, err)
	}
	return Record{kind: kind, eventID: eventID, fixtureID: fixtureID, payload: b}, nil
}

// Kind returns the semantic event_log.event_type value.
func (r Record) Kind() Kind { return r.kind }

// EventID returns the referenced event UUID, or uuid.Nil for fixture records.
func (r Record) EventID() uuid.UUID { return r.eventID }

// FixtureID returns the referenced fixture ID.
func (r Record) FixtureID() int64 { return r.fixtureID }

// Payload returns a copy of the encoded JSON payload.
func (r Record) Payload() json.RawMessage {
	return append(json.RawMessage(nil), r.payload...)
}

// Valid reports whether the record could have been produced by New.
func (r Record) Valid() bool {
	if !r.kind.Valid() || r.fixtureID == 0 || !json.Valid(r.payload) {
		return false
	}
	if r.kind.EventScoped() {
		return r.eventID != uuid.Nil
	}
	return r.eventID == uuid.Nil
}
