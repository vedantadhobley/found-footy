package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNew_ExposesBaselineMetrics(t *testing.T) {
	r := New()

	// Increment each baseline metric so it shows up in the scrape output
	// (Prometheus omits zero-value metrics from vec collectors).
	r.CallsTotal.WithLabelValues("infra_pg", "query", "success", "").Inc()
	r.LogLinesTotal.WithLabelValues("infra_pg", "INFO").Inc()
	r.DeployInfo.WithLabelValues("worker", "abc123", "2026-07-06", "2026-07-06T00:00:00Z").Set(1)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("scrape status = %d, want 200", w.Code)
	}
	body, _ := io.ReadAll(w.Result().Body)
	scrape := string(body)

	want := []string{
		"found_footy_calls_total",
		"found_footy_log_lines_total",
		"found_footy_deploy_git_sha_info",
		// Standard collectors
		"go_goroutines",
		"process_cpu_seconds_total",
	}
	for _, needle := range want {
		if !strings.Contains(scrape, needle) {
			t.Errorf("scrape output missing metric %q", needle)
		}
	}
}

func TestCallsTotal_LabelCombinationsAreIndependent(t *testing.T) {
	r := New()

	r.CallsTotal.WithLabelValues("infra_pg", "query", "success", "").Inc()
	r.CallsTotal.WithLabelValues("infra_pg", "query", "success", "").Inc()
	r.CallsTotal.WithLabelValues("infra_pg", "query", "failure", "pg.connection_lost").Inc()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	scrape := string(body)

	// Two independent time series: outcome="success" at 2, outcome="failure" at 1.
	if !strings.Contains(scrape, `found_footy_calls_total{action="query",error_class="",module="infra_pg",outcome="success"} 2`) {
		t.Errorf("success series not observed at value 2:\n%s", scrape)
	}
	if !strings.Contains(scrape, `found_footy_calls_total{action="query",error_class="pg.connection_lost",module="infra_pg",outcome="failure"} 1`) {
		t.Errorf("failure series not observed at value 1:\n%s", scrape)
	}
}

func TestPrometheusRegistry_UsableForCustomMetrics(t *testing.T) {
	// Adapter packages will register their own metrics against the same
	// registry. Verify that path works via PrometheusRegistry().
	r := New()

	// Simulate an adapter registering a counter — deferred until real
	// adapters land, but the path needs to exist now.
	custom := r.PrometheusRegistry()
	if custom == nil {
		t.Fatal("PrometheusRegistry returned nil")
	}
}
