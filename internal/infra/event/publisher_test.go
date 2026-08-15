// Golden + envelope conformance tests for the NatsPublisher — the Go
// producer structs must serialize to exactly the shapes the committed
// nats/schemas contract defines. Goldens are copied verbatim from
// nats/schemas/examples/ into testdata/ (see the README there).
package event

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/nats"
)

// goldenFiles are the committed envelope examples, one per subject.
var goldenFiles = []string{
	"found-footy.fixture.clock.json",
	"found-footy.fixture.update.json",
	"found-footy.event.video.json",
}

// readGolden loads a committed golden envelope from testdata.
func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return b
}

// asMap normalizes JSON bytes to a map for order-independent comparison.
func asMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal to map: %v\n%s", err, b)
	}
	return m
}

// TestEnvelopeRoundTripsGoldens verifies the Envelope struct's JSON tags
// match the contract: unmarshal each committed golden into Envelope,
// re-marshal, and require the result to equal the golden shape (compared
// as maps, so key order / whitespace don't matter). Catches a
// renamed/missing/extra envelope field.
func TestEnvelopeRoundTripsGoldens(t *testing.T) {
	for _, name := range goldenFiles {
		golden := readGolden(t, name)
		var env Envelope
		if err := json.Unmarshal(golden, &env); err != nil {
			t.Fatalf("%s: unmarshal into Envelope: %v", name, err)
		}
		got, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("%s: re-marshal Envelope: %v", name, err)
		}
		if !reflect.DeepEqual(asMap(t, golden), asMap(t, got)) {
			t.Errorf("%s: envelope round-trip diverged\n golden: %s\n    got: %s", name, golden, got)
		}
	}
}

// TestPayloadStructsRoundTripGoldens verifies each typed payload struct
// matches its schema: unmarshal the golden's `payload` object into the Go
// struct, re-marshal, and require equality (as maps). Catches a wrong
// json tag, a wrong type, or a broken nullability (the FixtureClock.Extra
// null-not-omitted contract).
func TestPayloadStructsRoundTripGoldens(t *testing.T) {
	cases := []struct {
		file string
		into func() any
	}{
		{"found-footy.fixture.clock.json", func() any { return &FixtureClockPayload{} }},
		{"found-footy.fixture.update.json", func() any { return &FixtureUpdatePayload{} }},
		{"found-footy.event.video.json", func() any { return &EventVideoPayload{} }},
	}
	for _, c := range cases {
		golden := readGolden(t, c.file)
		var wrapper struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(golden, &wrapper); err != nil {
			t.Fatalf("%s: extract payload: %v", c.file, err)
		}
		dst := c.into()
		if err := json.Unmarshal(wrapper.Payload, dst); err != nil {
			t.Fatalf("%s: unmarshal payload into %T: %v", c.file, dst, err)
		}
		got, err := json.Marshal(dst)
		if err != nil {
			t.Fatalf("%s: re-marshal payload: %v", c.file, err)
		}
		if !reflect.DeepEqual(asMap(t, wrapper.Payload), asMap(t, got)) {
			t.Errorf("%s: payload round-trip diverged\n golden: %s\n    got: %s", c.file, wrapper.Payload, got)
		}
	}
}

// TestEncodeEnvelope verifies the publisher stamps a schema-valid
// envelope: a parseable uuid id, an RFC3339 ts, the configured source,
// version 1, the right subject, and the payload nested under "payload".
func TestEncodeEnvelope(t *testing.T) {
	b, err := encodeEnvelope("found-footy-prod", TopicEventVideo.Wire("prod"),
		EventVideoPayload{EventID: uuid.New(), FixtureID: 1530158})
	if err != nil {
		t.Fatalf("encodeEnvelope: %v", err)
	}
	m := asMap(t, b)

	if _, err := uuid.Parse(m["id"].(string)); err != nil {
		t.Errorf("id not a uuid: %v", m["id"])
	}
	if _, err := time.Parse(time.RFC3339, m["ts"].(string)); err != nil {
		t.Errorf("ts not RFC3339: %v", m["ts"])
	}
	if m["source"] != "found-footy-prod" {
		t.Errorf("source = %v, want found-footy-prod", m["source"])
	}
	if m["version"] != float64(1) {
		t.Errorf("version = %v, want 1", m["version"])
	}
	if m["subject"] != "found-footy.prod.event.video" {
		t.Errorf("subject = %v, want found-footy.prod.event.video", m["subject"])
	}
	if _, ok := m["payload"].(map[string]any); !ok {
		t.Errorf("payload not an object: %v", m["payload"])
	}
}

// TestTopicWireAndValid — Wire renders found-footy.<env>.<topic>, and Valid
// gates the 3 registered topics against a typo (the guard publish relies on).
func TestTopicWireAndValid(t *testing.T) {
	if got := TopicFixtureUpdate.Wire("prod"); got != "found-footy.prod.fixture.update" {
		t.Errorf("Wire(prod) = %q, want found-footy.prod.fixture.update", got)
	}
	if got := TopicEventVideo.Wire("dev"); got != "found-footy.dev.event.video" {
		t.Errorf("Wire(dev) = %q, want found-footy.dev.event.video", got)
	}
	if !TopicFixtureClock.Valid() || !TopicFixtureUpdate.Valid() || !TopicEventVideo.Valid() {
		t.Error("registered topics must be Valid")
	}
	if Topic("bogus").Valid() {
		t.Error("unregistered topic must be invalid")
	}
}

// TestNewPublisherValidates requires a connection + a non-empty env, and
// derives the source stamp (found-footy-<env>) from that env.
func TestNewPublisherValidates(t *testing.T) {
	if _, err := NewPublisher(nil, "dev"); err == nil {
		t.Error("want error on nil conn")
	}
	if _, err := NewPublisher(&nats.Conn{}, ""); err == nil {
		t.Error("want error on empty env")
	}
	p, err := NewPublisher(&nats.Conn{}, "prod")
	if err != nil {
		t.Fatalf("NewPublisher(prod): %v", err)
	}
	if p.source != "found-footy-prod" {
		t.Errorf("derived source = %q, want found-footy-prod", p.source)
	}
}

// TestPublishEmptyBatchIsNoOp verifies a frozen clock / no-op update
// publishes nothing and returns nil (never dereferences the bus).
func TestPublishEmptyBatchIsNoOp(t *testing.T) {
	p, err := NewPublisher(&nats.Conn{}, "dev")
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if err := p.PublishFixtureClock(nil); err != nil {
		t.Errorf("empty clock batch: %v", err)
	}
	if err := p.PublishFixtureUpdate(nil); err != nil {
		t.Errorf("empty update batch: %v", err)
	}
}

// TestDedupeInt64 preserves first-seen order while dropping repeats.
func TestDedupeInt64(t *testing.T) {
	got := dedupeInt64([]int64{3, 1, 3, 2, 1})
	want := []int64{3, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dedupeInt64 = %v, want %v", got, want)
	}
}
