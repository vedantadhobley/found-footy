// Integration tests for the event composer using a real pg testcontainer. Per
// decisions.md 2026-08-14 the composer is event_log-only (the NATS half moved to
// NatsPublisher), so these verify the event_log row + metrics; there is no bus
// assertion anymore.
package event_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/event"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

const pgImage = "pgvector/pgvector:pg16"

// testHarness bundles the pg container, pool, and composer plus isolated
// observability handles for a single test.
type testHarness struct {
	ctx      context.Context
	pgPool   *pg.Pool
	composer *event.Composer

	eventLog *logging.TestEmitter
	eventReg *metrics.Registry
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

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

	eventReg := metrics.New()
	eventLog := &logging.TestEmitter{}
	composerIns := event.RegisterMetrics(eventReg, eventLog)
	composer, err := event.New(pool, composerIns)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	return &testHarness{
		ctx:      ctx,
		pgPool:   pool,
		composer: composer,
		eventLog: eventLog,
		eventReg: eventReg,
	}
}

// scrapeMetrics returns the exposition body from a registry — used to assert
// composer publishes_total is nonzero after a successful write.
func scrapeMetrics(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	reg.Handler().ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	return string(body)
}

// TestComposer_Publish_FixtureActivated — the fixture-scoped happy path:
// event_log row written with the right type/fixture/payload, event_id NULL,
// metric + DEBUG log emitted.
func TestComposer_Publish_FixtureActivated(t *testing.T) {
	h := newTestHarness(t)

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
	// pg's payload::text output may space keys; parse rather than substring-match.
	var gotObj map[string]any
	if err := json.Unmarshal(gotPayload, &gotObj); err != nil {
		t.Errorf("payload not valid JSON: %v; body=%s", err, string(gotPayload))
	} else if got, ok := gotObj["fixture_id"].(float64); !ok || int64(got) != 987654 {
		t.Errorf("payload JSONB fixture_id = %v; want 987654", gotObj["fixture_id"])
	}

	if !h.eventLog.HasAction(vocabulary.ModuleInfraEvent, vocabulary.ActionEventPublish) {
		t.Errorf("expected ActionEventPublish emission; snapshot=%+v", h.eventLog.Snapshot())
	}

	metricsBody := scrapeMetrics(t, h.eventReg)
	want := `found_footy_event_composer_publishes_total{kind="fixture.activated",outcome="success"} 1`
	if !strings.Contains(metricsBody, want) {
		t.Errorf("expected %q in metrics; got:\n%s", want, metricsBody)
	}
}

// TestComposer_Publish_EventDetected — the event-scoped path: event_id +
// fixture_id both land in the row.
func TestComposer_Publish_EventDetected(t *testing.T) {
	h := newTestHarness(t)

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

// TestComposer_Publish_UnknownKind rejects a bad kind before the INSERT.
func TestComposer_Publish_UnknownKind(t *testing.T) {
	h := newTestHarness(t)

	_, err := h.composer.Publish(h.ctx, event.Kind("not.a.kind"), uuid.Nil, 0, map[string]any{"x": 1})
	if err == nil {
		t.Fatal("Publish with unknown kind returned nil; expected error")
	}
	if !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("Publish error = %q; want it to mention unknown kind", err)
	}

	var count int
	if err := h.pgPool.QueryRow(h.ctx, `SELECT COUNT(*) FROM event_log`).Scan(&count); err != nil {
		t.Fatalf("count event_log: %v", err)
	}
	if count != 0 {
		t.Errorf("event_log row count = %d; want 0 after bad-kind reject", count)
	}
}

// TestComposer_New_RejectsNilArgs guards the constructor against nil pool /
// instruments — otherwise a caller mistake crashes deep in Publish().
func TestComposer_New_RejectsNilArgs(t *testing.T) {
	if _, err := event.New(nil, nil); err == nil {
		t.Errorf("event.New(nil, nil) returned nil error; want a required-args error")
	}
}
