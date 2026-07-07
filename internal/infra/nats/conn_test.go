package nats_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/nats"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// nats:2.10-alpine — small, current, no JetStream flag (JetStream lands
// with the durable-consumer work in a later phase).
const natsImage = "nats:2.10-alpine"

type testFixture struct {
	reg *metrics.Registry
	log *logging.TestEmitter
	ins *nats.Instruments
}

func newTestFixture() *testFixture {
	reg := metrics.New()
	log := &logging.TestEmitter{}
	ins := nats.RegisterMetrics(reg, log)
	return &testFixture{reg: reg, log: log, ins: ins}
}

// runTestNATS spins up an ephemeral NATS server via testcontainers-go.
// Returns the URL clients should dial. Registers a cleanup that
// terminates the container. Go convention: ctx first, then t.
func runTestNATS(ctx context.Context, t *testing.T) string {
	t.Helper()

	nc, err := tcnats.Run(ctx, natsImage)
	if err != nil {
		t.Fatalf("start nats container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(nc); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	url, err := nc.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return url
}

func scrapeMetrics(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	reg.Handler().ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	return string(body)
}

// TestConn_LifecycleAgainstRealNATS spins up a real NATS server,
// connects via nats.New, publishes a message that a subscriber
// receives, then closes cleanly. Verifies the corresponding log
// actions land.
func TestConn_LifecycleAgainstRealNATS(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	url := runTestNATS(ctx, t)
	fx := newTestFixture()

	conn, err := nats.New(ctx, config.NATSConfig{
		URL:            url,
		ClientName:     "test-client",
		ConnectTimeout: 5 * time.Second,
		ReconnectWait:  2 * time.Second,
		MaxReconnects:  -1,
	}, fx.ins)
	if err != nil {
		t.Fatalf("nats.New: %v", err)
	}

	if !fx.log.HasAction(vocabulary.ModuleInfraNATS, vocabulary.ActionNATSConnected) {
		t.Errorf("expected ActionNATSConnected; captured=%+v", fx.log.Snapshot())
	}

	// Subscribe, publish, wait for delivery.
	var (
		delivered = make(chan []byte, 1)
		once      sync.Once
	)
	sub, err := conn.Subscribe("event.detected", func(msg *natsgo.Msg) {
		once.Do(func() { delivered <- msg.Data })
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	if err := conn.Publish("event.detected", []byte(`{"fixture_id":1}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	select {
	case got := <-delivered:
		if string(got) != `{"fixture_id":1}` {
			t.Errorf("delivered payload = %q, want the JSON body", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscriber never received the published message")
	}

	conn.Close()

	if !fx.log.HasAction(vocabulary.ModuleInfraNATS, vocabulary.ActionNATSClosed) {
		t.Errorf("expected ActionNATSClosed after Close; captured=%+v", fx.log.Snapshot())
	}
}

// TestPublishSubscribe_MetricsAndClassification verifies the publish
// counter/histogram + subscribe counter series appear in the scrape,
// and that classifySubject buckets subjects correctly (event/fixture/
// other).
func TestPublishSubscribe_MetricsAndClassification(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	url := runTestNATS(ctx, t)
	fx := newTestFixture()

	conn, err := nats.New(ctx, config.NATSConfig{
		URL: url, ClientName: "test", ConnectTimeout: 5 * time.Second,
		ReconnectWait: 2 * time.Second, MaxReconnects: -1,
	}, fx.ins)
	if err != nil {
		t.Fatalf("nats.New: %v", err)
	}
	defer conn.Close()

	// Publish across all three subject kinds so each series shows up.
	if err := conn.Publish("event.detected", []byte("a")); err != nil {
		t.Fatalf("publish event: %v", err)
	}
	if err := conn.Publish("fixture.activated", []byte("b")); err != nil {
		t.Fatalf("publish fixture: %v", err)
	}
	if err := conn.Publish("weird_no_dot", []byte("c")); err != nil {
		t.Fatalf("publish other: %v", err)
	}
	_ = conn.Flush()

	// Give async publish accounting a moment.
	time.Sleep(100 * time.Millisecond)

	scrape := scrapeMetrics(t, fx.reg)
	wantContains := []string{
		`found_footy_nats_publishes_total{outcome="success",subject_kind="event"} 1`,
		`found_footy_nats_publishes_total{outcome="success",subject_kind="fixture"} 1`,
		`found_footy_nats_publishes_total{outcome="success",subject_kind="other"} 1`,
		`found_footy_nats_publish_duration_seconds_count{subject_kind="event"} 1`,
		`found_footy_nats_connection_state 1`,
	}
	for _, want := range wantContains {
		if !strings.Contains(scrape, want) {
			t.Errorf("scrape missing %q; got:\n%s", want, scrape)
		}
	}
}

// TestNew_NilInstruments_Errors matches the pg pattern — nil
// Instruments is a hard fail up front, no silent fallback.
func TestNew_NilInstruments_Errors(t *testing.T) {
	_, err := nats.New(context.Background(),
		config.NATSConfig{URL: "nats://x:4222"}, nil)
	if err == nil {
		t.Fatal("expected error for nil Instruments, got nil")
	}
}

// TestNew_EmptyURL_Errors matches pg — no URL means no pool.
func TestNew_EmptyURL_Errors(t *testing.T) {
	fx := newTestFixture()
	_, err := nats.New(context.Background(),
		config.NATSConfig{URL: ""}, fx.ins)
	if err == nil {
		t.Fatal("expected error for empty NATS_URL, got nil")
	}
}

// TestNew_UnreachableHost_ErrorsQuickly bounds startup by
// ConnectTimeout — a dead NATS mustn't hang the binary.
func TestNew_UnreachableHost_ErrorsQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration-ish test skipped in -short mode")
	}

	fx := newTestFixture()
	start := time.Now()
	_, err := nats.New(context.Background(), config.NATSConfig{
		// RFC 6890 discard address — should refuse-connect fast.
		URL:            "nats://192.0.2.1:4222",
		ClientName:     "test",
		ConnectTimeout: 2 * time.Second,
		ReconnectWait:  2 * time.Second,
		MaxReconnects:  0,
	}, fx.ins)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for unreachable host, got nil")
	}
	if elapsed > 6*time.Second {
		t.Errorf("New took %v, want ≤ 6s (timeout was 2s)", elapsed)
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraNATS, vocabulary.ActionNATSConnectFailed) {
		t.Errorf("expected ActionNATSConnectFailed; captured=%+v", fx.log.Snapshot())
	}
}
