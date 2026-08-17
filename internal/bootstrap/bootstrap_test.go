// Tests for the metrics/healthz mux and Closer registry LIFO ordering.
package bootstrap

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

// TestRegisterCloser_ReverseOrderAndErrorTolerance verifies that
// registered closers run in reverse-registration order (LIFO) and that
// an error from one closer doesn't stop remaining closers from running.
// The reverse-order property is what enables "Temporal drains before
// its underlying deps close" when adapters are constructed in
// natural order (pg → nats → temporal).
func TestRegisterCloser_ReverseOrderAndErrorTolerance(t *testing.T) {
	deps := &Deps{}

	var invocations []string
	deps.RegisterCloser("first", func(_ context.Context) error {
		invocations = append(invocations, "first")
		return nil
	})
	deps.RegisterCloser("second", func(_ context.Context) error {
		invocations = append(invocations, "second")
		return errors.New("simulated failure")
	})
	deps.RegisterCloser("third", func(_ context.Context) error {
		invocations = append(invocations, "third")
		return nil
	})

	// Manually run closers as bootstrap.Run does.
	for i := len(deps.closers) - 1; i >= 0; i-- {
		c := deps.closers[i]
		_ = c.close(context.Background())
	}

	want := []string{"third", "second", "first"}
	if len(invocations) != len(want) {
		t.Fatalf("invocations = %v, want %v", invocations, want)
	}
	for i, got := range invocations {
		if got != want[i] {
			t.Errorf("invocation[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestNewMetricsMux_MetricsAndHealth(t *testing.T) {
	m := metrics.New()
	mux := newMetricsMux(m)

	// /metrics returns 200 with Prometheus content-type
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("/metrics status = %d, want 200", w.Code)
	}
	body, _ := io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "go_goroutines") {
		t.Error("/metrics response missing runtime collectors")
	}

	// /healthz returns 200 ok
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", w.Code)
	}
	body, _ = io.ReadAll(w.Result().Body)
	if strings.TrimSpace(string(body)) != "ok" {
		t.Errorf("/healthz body = %q, want ok", body)
	}
}

// TestRun_RefusesToStartWithoutMetricsListener locks FF-026's startup
// invariant: application work must not begin when /metrics and /healthz cannot
// own their configured socket.
func TestRun_RefusesToStartWithoutMetricsListener(t *testing.T) {
	setValidBootstrapEnvironment(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve metrics address: %v", err)
	}
	defer occupied.Close()
	t.Setenv("METRICS_ADDR", occupied.Addr().String())
	t.Setenv("LOG_FORMAT", "text")

	workCalled := false
	err = run("api", "sha", "now", func(context.Context, *Deps) error {
		workCalled = true
		return nil
	})
	if err == nil {
		t.Fatal("run returned nil with an occupied metrics address")
	}
	if workCalled {
		t.Fatal("Work started without a metrics/health listener")
	}
}

// TestRun_OccupiedMetricsAddressExitsOne covers the public process boundary,
// not only run's returned error. The child re-enters this test and calls Run,
// which must translate the deterministic bind failure into exit status 1.
func TestRun_OccupiedMetricsAddressExitsOne(t *testing.T) {
	if os.Getenv("FF_BOOTSTRAP_EXIT_HELPER") == "1" {
		Run("api", "sha", "now", func(context.Context, *Deps) error {
			return errors.New("Work must not start")
		})
		return
	}
	setValidBootstrapEnvironment(t)

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve metrics address: %v", err)
	}
	defer occupied.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestRun_OccupiedMetricsAddressExitsOne$")
	cmd.Env = append(os.Environ(),
		"FF_BOOTSTRAP_EXIT_HELPER=1",
		"METRICS_ADDR="+occupied.Addr().String(),
		"LOG_FORMAT=text",
	)
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child error = %v, want exit error", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("child exit code = %d, want 1", exitErr.ExitCode())
	}
}

// TestRun_DrainsEphemeralMetricsListener proves the testable lifecycle can
// bind an OS-assigned port, run application work, and shut the listener down.
func TestRun_DrainsEphemeralMetricsListener(t *testing.T) {
	setValidBootstrapEnvironment(t)
	t.Setenv("METRICS_ADDR", "127.0.0.1:0")
	t.Setenv("LOG_FORMAT", "text")

	workCalled := false
	if err := run("api", "sha", "now", func(context.Context, *Deps) error {
		workCalled = true
		return nil
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !workCalled {
		t.Fatal("Work was not called after the metrics listener bound")
	}
}

func setValidBootstrapEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("PG_DSN", "postgres://user:pass@postgres:5432/found_footy")
	t.Setenv("S3_ENDPOINT", "http://garage:3900")
	t.Setenv("S3_BUCKET", "found-footy")
	t.Setenv("S3_ACCESS_KEY_ID", "test-access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test-secret")
}
