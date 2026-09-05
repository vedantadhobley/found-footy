// Payload structs for the 3 live-feed subjects — the Go mirrors of the
// per-subject schemas under nats/schemas/. Field names are snake_case to
// match the JSON contract + the frontend consumer. fixture.update / event.update
// remain thin dirty-signals; fixture.status embeds the same projection
// as the REST fixture so the frontend patches without interpreting status codes.
package event

import (
	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/contract/fixturepresentation"
)

// FixtureStatusPayload is the TopicFixtureStatus body. The schema
// requires at least one fixture; the publisher skips an empty batch.
type FixtureStatusPayload struct {
	Fixtures []FixtureStatus `json:"fixtures"`
}

// FixtureStatus associates the shared REST/NATS projection with its
// fixture. Embedding keeps the wire fields identical instead of remapping them.
type FixtureStatus struct {
	FixtureID int64 `json:"fixture_id"`
	fixturepresentation.Projection
}

// FixtureUpdatePayload — TopicFixtureUpdate body. The ids to
// bulk-refetch (GET /fixtures?ids=). The schema requires unique + min 1;
// the publisher dedups + skips empty.
type FixtureUpdatePayload struct {
	FixtureIDs []int64 `json:"fixture_ids"`
}

// EventUpdatePayload — TopicEventUpdate body. EventID is the event whose
// public projection changed; FixtureID routes the consumer to its parent.
type EventUpdatePayload struct {
	EventID   uuid.UUID `json:"event_id"`
	FixtureID int64     `json:"fixture_id"`
}
