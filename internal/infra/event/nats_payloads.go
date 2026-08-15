// Payload structs for the 3 live-feed subjects — the Go mirrors of the
// per-subject schemas under nats/schemas/. Field names are snake_case to
// match the JSON contract + the frontend consumer. Kept minimal:
// fixture.update / event.video are thin dirty-signals (ids only) — the
// consumer refetches full state over REST. Only fixture.clock is fat
// (inline ticks) so the frontend renders the minute with no fetch.
package event

import "github.com/google/uuid"

// FixtureClockPayload — TopicFixtureClock body. One entry per fixture
// whose match minute advanced this cycle. The schema requires minItems 1;
// the publisher skips an empty batch rather than emit an invalid message.
type FixtureClockPayload struct {
	Fixtures []FixtureClock `json:"fixtures"`
}

// FixtureClock is a single live tick. Extra is a pointer with NO
// omitempty: the contract carries stoppage time as an explicit null when
// there is none (matching the committed golden), not an absent key.
type FixtureClock struct {
	FixtureID int64 `json:"fixture_id"`
	Minute    int   `json:"minute"`
	Extra     *int  `json:"extra"`
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
