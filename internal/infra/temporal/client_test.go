// Integration + unit tests for the Temporal client wrapper.
package temporal_test

import (
	"context"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/temporal"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// testFixture mirrors the pattern from pg / nats / s3 tests — one
// fresh Registry + TestEmitter + Instruments per test.
type testFixture struct {
	reg *metrics.Registry
	log *logging.TestEmitter
	ins *temporal.Instruments
}

func newTestFixture() *testFixture {
	reg := metrics.New()
	log := &logging.TestEmitter{}
	ins := temporal.RegisterMetrics(reg, log)
	return &testFixture{reg: reg, log: log, ins: ins}
}

// A full Temporal server ships as temporalio/auto-setup: an all-in-one
// image bundling frontend + history + matching + Postgres. It takes
// 15-30s to reach "ready" state (much heavier than pg/nats/s3). We
// use the dev-stack Temporal instead of testcontainers for integration
// tests — spun once, reused across tests. Fast-fail unit tests below
// don't need it.

// TestNewClient_NilInstruments_Errors — same fast-fail guard as
// pg / nats / s3.
func TestNewClient_NilInstruments_Errors(t *testing.T) {
	_, err := temporal.NewClient(context.Background(),
		config.TemporalConfig{HostPort: "temporal:7233"}, nil)
	if err == nil {
		t.Fatal("expected error for nil Instruments, got nil")
	}
}

// TestNewClient_EmptyHostPort_Errors — no HostPort = no client.
func TestNewClient_EmptyHostPort_Errors(t *testing.T) {
	fx := newTestFixture()
	_, err := temporal.NewClient(context.Background(),
		config.TemporalConfig{HostPort: ""}, fx.ins)
	if err == nil {
		t.Fatal("expected error for empty TEMPORAL_HOSTPORT, got nil")
	}
}

// TestNewClient_UnreachableHost_ErrorsQuickly bounds startup by
// ConnectTimeout — same guard as every other adapter.
func TestNewClient_UnreachableHost_ErrorsQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration-ish test skipped in -short mode")
	}

	fx := newTestFixture()
	start := time.Now()
	_, err := temporal.NewClient(context.Background(), config.TemporalConfig{
		HostPort:              "192.0.2.1:7233", // RFC 6890 discard address
		Namespace:             "default",
		TaskQueue:             "found-footy",
		ConnectTimeout:        2 * time.Second,
		WorkerShutdownTimeout: 60 * time.Second,
	}, fx.ins)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for unreachable host, got nil")
	}
	if elapsed > 6*time.Second {
		t.Errorf("NewClient took %v, want ≤ 6s (timeout was 2s)", elapsed)
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalConnectFailed) {
		t.Errorf("expected ActionTemporalConnectFailed; captured=%+v", fx.log.Snapshot())
	}
}
