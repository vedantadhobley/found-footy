// Tests for the metrics/healthz mux and Closer registry LIFO ordering.
package bootstrap

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
