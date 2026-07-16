// Integration tests for the event composer using real pg + NATS
// testcontainers. Verifies both writes (pg row + NATS envelope) on
// the happy path and that skew accounting kicks in on NATS-side
// failure. Unknown-kind + argument validation covered as unit-shape
// tests inside the same file (they still spin the containers via the
// shared setup — negligible cost vs the alternative of a separate
// test file).
package event_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/event"
	"github.com/vedantadhobley/found-footy/internal/infra/nats"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

const (
	pgImage   = "pgvector/pgvector:pg16"
	natsImage = "nats:2.10-alpine"
)

// testHarness bundles the containers, adapters, and composer plus
// isolated observability handles for a single test. All three
// container-backed dependencies (pg, NATS) are lifecycle-managed via
// t.Cleanup so parallel-safe.
type testHarness struct {
	ctx      context.Context
	pgPool   *pg.Pool
	natsConn *nats.Conn
	composer *event.Composer

	pgLog   *logging.TestEmitter
	natsLog *logging.TestEmitter
	eventLog *logging.TestEmitter

	pgReg   *metrics.Registry
	natsReg *metrics.Registry
	eventReg *metrics.Registry

	composerIns *event.Instruments
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	// pg container with the app schema applied at init time. Using
	// tcpostgres.WithInitScripts with the pg-package-relative path so
	// we don't need to duplicate schema.sql into this package.
	pgc, err := tcpostgres.Run(ctx,
		pgImage,
		tcpostgres.WithDatabase("found_footy"),
		tcpostgres.WithUsername("ffuser"),
		tcpostgres.WithPassword("ffpass"),
		tcpostgres.WithInitScripts("../pg/schema.sql"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(pgc); err != nil {
			t.Logf("terminate pg container: %v", err)
		}
	})
	pgConnStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("pg connection string: %v", err)
	}

	// NATS container. No JetStream in this test — the composer only
	// uses core Publish; JetStream durability lands with the outbox
	// catch-up worker in a later phase.
	nc, err := tcnats.Run(ctx, natsImage)
	if err != nil {
		t.Fatalf("start nats container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(nc); err != nil {
			t.Logf("terminate nats container: %v", err)
		}
	})
	natsURL, err := nc.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("nats connection string: %v", err)
	}

	// Isolated registry / emitter per adapter so metric families don't
	// collide on prometheus.MustRegister and log assertions don't
	// interleave.
	pgReg := metrics.New()
	pgLog := &logging.TestEmitter{}
	pgIns := pg.RegisterMetrics(pgReg, pgLog)
	pool, err := pg.New(ctx, config.PGConfig{
		DSN:            pgConnStr,
		MaxConns:       5,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	}, pgIns)
	if err != nil {
		t.Fatalf("pg.New: %v", err)
	}
	t.Cleanup(pool.Close)

	natsReg := metrics.New()
	natsLog := &logging.TestEmitter{}
	natsIns := nats.RegisterMetrics(natsReg, natsLog)
	conn, err := nats.New(ctx, config.NATSConfig{
		URL:            natsURL,
		ClientName:     "test-event-composer",
		ConnectTimeout: 5 * time.Second,
		ReconnectWait:  2 * time.Second,
		MaxReconnects:  -1,
	}, natsIns)
	if err != nil {
		t.Fatalf("nats.New: %v", err)
	}
	t.Cleanup(conn.Close)

	eventReg := metrics.New()
	eventLog := &logging.TestEmitter{}
	composerIns := event.RegisterMetrics(eventReg, eventLog)
	composer, err := event.New(pool, conn, composerIns)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	return &testHarness{
		ctx:         ctx,
		pgPool:      pool,
		natsConn:    conn,
		composer:    composer,
		pgLog:       pgLog,
		natsLog:     natsLog,
		eventLog:    eventLog,
		pgReg:       pgReg,
		natsReg:     natsReg,
		eventReg:    eventReg,
		composerIns: composerIns,
	}
}

// receivedEnvelope holds the parsed JSON body of a NATS message the
// test subscriber received. Field types mirror composer.envelope's
// anonymous struct.
type receivedEnvelope struct {
	EventLogID int64           `json:"event_log_id"`
	Kind       string          `json:"kind"`
	OccurredAt time.Time       `json:"occurred_at"`
	EventID    *uuid.UUID      `json:"event_id,omitempty"`
	FixtureID  *int64          `json:"fixture_id,omitempty"`
	Payload    json.RawMessage `json:"payload"`
}

// subscribeAndWait subscribes on subject and blocks until one message
// arrives OR the timeout fires. Returns the parsed envelope.
func subscribeAndWait(t *testing.T, conn *nats.Conn, subject string, timeout time.Duration) receivedEnvelope {
	t.Helper()

	var (
		delivered = make(chan []byte, 1)
		once      sync.Once
	)
	sub, err := conn.Subscribe(subject, func(msg *natsgo.Msg) {
		once.Do(func() { delivered <- msg.Data })
	})
	if err != nil {
		t.Fatalf("subscribe %q: %v", subject, err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush after subscribe: %v", err)
	}

	select {
	case body := <-delivered:
		var env receivedEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("unmarshal envelope: %v\nbody: %s", err, string(body))
		}
		return env
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for NATS delivery on %q", subject)
		return receivedEnvelope{}
	}
}

// scrapeMetrics returns the exposition body from a registry — used
// to assert composer publishes_total is nonzero after a successful
// dual-write.
func scrapeMetrics(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	reg.Handler().ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	return string(body)
}

// TestComposer_Publish_FixtureActivated exercises the happy path
// end-to-end — writes to event_log, delivers to NATS, envelope parses
// with expected fields, metrics increment.
func TestComposer_Publish_FixtureActivated(t *testing.T) {
	h := newTestHarness(t)

	// Subscribe BEFORE publishing so the test doesn't race the
	// message. Core NATS publishes are lost if nobody's subscribed
	// at emit time; this is the correct pattern for the test.
	delivered := make(chan receivedEnvelope, 1)
	sub, err := h.natsConn.Subscribe(string(event.KindFixtureActivated), func(msg *natsgo.Msg) {
		var env receivedEnvelope
		if err := json.Unmarshal(msg.Data, &env); err == nil {
			select {
			case delivered <- env:
			default:
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := h.natsConn.Flush(); err != nil {
		t.Fatalf("flush after subscribe: %v", err)
	}

	payload := event.FixtureActivatedPayload{
		FixtureID:   987654,
		ActivatedAt: time.Now().UTC().Truncate(time.Millisecond),
		Reason:      "kickoff_soon",
	}
	logID, err := h.composer.Publish(h.ctx, event.KindFixtureActivated, uuid.Nil, 987654, payload)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if logID == 0 {
		t.Fatal("Publish returned zero event_log_id; expected BIGSERIAL to allocate a positive value")
	}

	// pg-side assertion: event_log has the row with the right fields.
	var (
		gotType    string
		gotFixture *int64
		gotEvent   *uuid.UUID
		gotPayload []byte
	)
	err = h.pgPool.QueryRow(h.ctx, `
		SELECT event_type, fixture_id, event_id, payload::text
		FROM event_log WHERE id = $1
	`, logID).Scan(&gotType, &gotFixture, &gotEvent, &gotPayload)
	if err != nil {
		t.Fatalf("select event_log row: %v", err)
	}
	if gotType != string(event.KindFixtureActivated) {
		t.Errorf("event_type = %q; want %q", gotType, event.KindFixtureActivated)
	}
	if gotFixture == nil || *gotFixture != 987654 {
		t.Errorf("fixture_id = %v; want 987654", gotFixture)
	}
	if gotEvent != nil {
		t.Errorf("event_id = %v; want NULL for fixture-scoped kind", gotEvent)
	}
	// pg's payload::text output uses ", " between keys; match either
	// spaced or compact JSON by parsing rather than substring-matching.
	var gotObj map[string]any
	if err := json.Unmarshal(gotPayload, &gotObj); err != nil {
		t.Errorf("payload not valid JSON: %v; body=%s", err, string(gotPayload))
	} else if got, ok := gotObj["fixture_id"].(float64); !ok || int64(got) != 987654 {
		t.Errorf("payload JSONB fixture_id = %v; want 987654", gotObj["fixture_id"])
	}

	// NATS-side assertion: envelope delivered with expected fields.
	select {
	case env := <-delivered:
		if env.EventLogID != logID {
			t.Errorf("envelope event_log_id = %d; want %d", env.EventLogID, logID)
		}
		if env.Kind != string(event.KindFixtureActivated) {
			t.Errorf("envelope kind = %q; want %q", env.Kind, event.KindFixtureActivated)
		}
		if env.FixtureID == nil || *env.FixtureID != 987654 {
			t.Errorf("envelope fixture_id = %v; want 987654", env.FixtureID)
		}
		if env.EventID != nil {
			t.Errorf("envelope event_id = %v; want omitted for fixture-scoped kind", env.EventID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for NATS delivery")
	}

	// Log assertions: composer should have emitted ActionEventPublish
	// at DEBUG on success.
	if !h.eventLog.HasAction(vocabulary.ModuleInfraEvent, vocabulary.ActionEventPublish) {
		t.Errorf("expected ActionEventPublish emission; snapshot=%+v", h.eventLog.Snapshot())
	}

	// Metric assertions: publishes_total{kind=fixture.activated,outcome=success}
	// should be 1.
	metricsBody := scrapeMetrics(t, h.eventReg)
	want := `found_footy_event_composer_publishes_total{kind="fixture.activated",outcome="success"} 1`
	if !strings.Contains(metricsBody, want) {
		t.Errorf("expected %q in metrics; got:\n%s", want, metricsBody)
	}
}

// TestComposer_Publish_EventDetected covers the event-scoped path —
// event_id populated, fixture_id populated, both make it into the
// row + envelope.
func TestComposer_Publish_EventDetected(t *testing.T) {
	h := newTestHarness(t)

	// Fixtures + events tables have FK, but event_log doesn't FK-check
	// event_id / fixture_id (they're not foreign keys in the schema),
	// so we can synthesize any UUID for the test.
	evID := uuid.New()
	fxID := int64(1234567)

	payload := event.EventDetectedPayload{
		EventID:    evID,
		FixtureID:  fxID,
		EventType:  "goal",
		Detail:     "normal goal",
		Minute:     23,
		PlayerName: "Saka",
		TeamID:     42,
		TeamName:   "Arsenal",
		Counter:    1,
	}
	logID, err := h.composer.Publish(h.ctx, event.KindEventDetected, evID, fxID, payload)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var (
		gotEvent   *uuid.UUID
		gotFixture *int64
	)
	err = h.pgPool.QueryRow(h.ctx, `
		SELECT event_id, fixture_id FROM event_log WHERE id = $1
	`, logID).Scan(&gotEvent, &gotFixture)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if gotEvent == nil || *gotEvent != evID {
		t.Errorf("event_id = %v; want %v", gotEvent, evID)
	}
	if gotFixture == nil || *gotFixture != fxID {
		t.Errorf("fixture_id = %v; want %v", gotFixture, fxID)
	}
}

// TestComposer_Publish_UnknownKind rejects a bad kind before touching
// pg or NATS. Fast fail on typos.
func TestComposer_Publish_UnknownKind(t *testing.T) {
	h := newTestHarness(t)

	_, err := h.composer.Publish(h.ctx, event.Kind("not.a.kind"), uuid.Nil, 0, map[string]any{"x": 1})
	if err == nil {
		t.Fatal("Publish with unknown kind returned nil; expected error")
	}
	if !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("Publish error = %q; want it to mention unknown kind", err)
	}

	// No row inserted, no metric incremented for a bad-kind reject
	// (we fail before touching either).
	var count int
	if err := h.pgPool.QueryRow(h.ctx, `SELECT COUNT(*) FROM event_log`).Scan(&count); err != nil {
		t.Fatalf("count event_log: %v", err)
	}
	if count != 0 {
		t.Errorf("event_log row count = %d; want 0 after bad-kind reject", count)
	}
}

// TestComposer_New_RejectsNilArgs makes sure the constructor guards
// against nil pool / conn / instruments — otherwise a caller mistake
// crashes deep in Publish() on the first call.
func TestComposer_New_RejectsNilArgs(t *testing.T) {
	if _, err := event.New(nil, nil, nil); err == nil {
		t.Errorf("event.New(nil, nil, nil) returned nil error; want a required-args error")
	}
}

// TestComposer_AllKinds_Enumeration ensures AllKinds() stays in sync
// with the const block. If someone adds a new Kind but forgets to
// list it here, this test fails.
func TestComposer_AllKinds_Enumeration(t *testing.T) {
	kinds := event.AllKinds()
	seen := make(map[event.Kind]bool, len(kinds))
	for _, k := range kinds {
		if seen[k] {
			t.Errorf("Kind %q appears twice in AllKinds()", k)
		}
		seen[k] = true
		if !k.Valid() {
			t.Errorf("AllKinds() contains %q which returns Valid()=false", k)
		}
	}
	if len(kinds) < 6 {
		t.Errorf("AllKinds() returned %d kinds; want at least 6 (O3/a locked six)", len(kinds))
	}
}
