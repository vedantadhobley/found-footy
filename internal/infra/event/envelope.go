// Envelope is the standard workspace NATS message wrapper — the Go
// mirror of nats/schemas/envelope.schema.json. Routing + domain data
// live in Payload; id/ts/source/version/subject are the generic framing
// every workspace consumer reads without knowing the subject. See
// decisions.md 2026-08-14.
package event

import (
	"time"

	"github.com/google/uuid"
)

// envelopeVersion is the contract version stamped on every message.
// Bump only on a breaking payload change (all 3 subjects share it for
// now). The envelope schema requires version >= 1.
const envelopeVersion = 1

// Envelope wraps a per-subject payload with generic framing. Field
// names + JSON shape match envelope.schema.json exactly (enforced by
// the golden round-trip in publisher_test.go). Payload is `any` so the
// publisher passes a typed payload struct straight in — json.Marshal
// nests it under "payload".
type Envelope struct {
	ID      string `json:"id"`      // unique message id (uuid) — dedup / idempotency
	Ts      string `json:"ts"`      // RFC 3339 UTC emit time
	Source  string `json:"source"`  // producer identity incl. env (found-footy-dev / -prod)
	Version int    `json:"version"` // contract version for the subject's payload
	Subject string `json:"subject"` // full NATS subject this was published on
	Payload any    `json:"payload"` // subject-specific body
}

// newEnvelope stamps a fresh envelope around payload: a new uuid id, the
// current UTC time in RFC 3339, the configured source, and the shared
// contract version. RFC3339 (second precision) matches the schema's
// date-time format + the committed goldens. Called from an activity, so
// time.Now / uuid are safe (no workflow-determinism constraint).
func newEnvelope(source, wireSubject string, payload any) Envelope {
	return Envelope{
		ID:      uuid.NewString(),
		Ts:      time.Now().UTC().Format(time.RFC3339),
		Source:  source,
		Version: envelopeVersion,
		Subject: wireSubject,
		Payload: payload,
	}
}
