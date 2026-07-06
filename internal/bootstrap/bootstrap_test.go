package bootstrap

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

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
