// Payload structs for the 3 live-feed subjects — the Go mirrors of the
// per-subject schemas under nats/schemas/. Field names are snake_case to
// match the JSON contract + the frontend consumer. fixture.update / event.video
// remain thin dirty-signals; fixture.presentation embeds the same projection
// as the REST fixture so the frontend patches without interpreting status codes.
package event

import (
	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/contract/fixturepresentation"
)

// FixturePresentationPayload is the TopicFixturePresentation body. The schema
// requires at least one fixture; the publisher skips an empty batch.
type FixturePresentationPayload struct {
	Fixtures []FixturePresentation `json:"fixtures"`
}

// FixturePresentation associates the shared REST/NATS projection with its
// fixture. Embedding keeps the wire fields identical instead of remapping them.
type FixturePresentation struct {
	FixtureID int64 `json:"fixture_id"`
	fixturepresentation.Projection
}

// FixtureUpdatePayload — TopicFixtureUpdate body. The ids to
// bulk-refetch (GET /fixtures?ids=). The schema requires unique + min 1;
// the publisher dedups + skips empty.
type FixtureUpdatePayload struct {
	FixtureIDs []int64 `json:"fixture_ids"`
}

// EventVideoPayload — TopicEventVideo body. EventID is the event whose
// clip set changed; FixtureID is routing so the consumer knows which
// fixture to splice it into.
type EventVideoPayload struct {
	EventID   uuid.UUID `json:"event_id"`
	FixtureID int64     `json:"fixture_id"`
}
