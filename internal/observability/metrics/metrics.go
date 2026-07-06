// Package metrics owns the Prometheus registry + baseline collectors
// every found-footy binary exposes.
//
// The Registry struct wraps *prometheus.Registry with the standard
// collectors + a small set of baseline metrics from rebuild-plan.md
// §11 (calls_total, log_lines_total, deploy_info_gauge). Adapter- and
// domain-specific metrics get added in later S-phase commits.
//
// Callers get an http.Handler for /metrics via Handler(), which they
// wire into their binary's HTTP mux.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is the injectable metrics registry every binary constructs
// at startup. Wraps *prometheus.Registry with the baseline metrics
// pre-registered.
type Registry struct {
	reg *prometheus.Registry

	// Baseline metrics from §11 that ship in Phase S1. More get added
	// as adapters land.

	// CallsTotal counts every logged event (derived from logging.Emit)
	// labeled by (module, action, outcome, error_class). Populated by
	// the logging package's downstream hook in a later commit.
	CallsTotal *prometheus.CounterVec

	// LogLinesTotal counts emissions by (module, level). Cheaper cardinality
	// than CallsTotal — safe as a saturation early-warning even when the
	// full CallsTotal dimensionality is disabled.
	LogLinesTotal *prometheus.CounterVec

	// DeployInfo is a gauge that always reads 1; its labels carry the
	// binary's identity (binary, git_sha, image_tag, built_at). Set
	// once at startup by the binary's Init call.
	DeployInfo *prometheus.GaugeVec
}

// New builds a fresh Registry with the standard collectors + baseline
// metrics registered. Standard collectors: Go runtime (goroutines, GC,
// memory), process (CPU, RSS, open FDs).
func New() *Registry {
	reg := prometheus.NewRegistry()

	// Standard collectors — Go runtime + OS process.
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	r := &Registry{
		reg: reg,
		CallsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "found_footy_calls_total",
				Help: "Cumulative count of logged events, by module + action + outcome + error class.",
			},
			[]string{"module", "action", "outcome", "error_class"},
		),
		LogLinesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "found_footy_log_lines_total",
				Help: "Cumulative log-line count by module + level.",
			},
			[]string{"module", "level"},
		),
		DeployInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "found_footy_deploy_git_sha_info",
				Help: "Deploy identity carried in labels; value is always 1.",
			},
			[]string{"binary", "git_sha", "image_tag", "built_at"},
		),
	}

	reg.MustRegister(r.CallsTotal, r.LogLinesTotal, r.DeployInfo)
	return r
}

// Handler returns the http.Handler serving /metrics for scraping.
// Wire it into the binary's HTTP mux (or standalone listener for
// binaries without a main HTTP surface, like worker + scaler).
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{
		// Report errors in the response body — silent metric drops are
		// the worst debugging experience.
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// PrometheusRegistry exposes the underlying *prometheus.Registry so
// adapter packages can register their own metrics against this
// registry (via r.PrometheusRegistry().MustRegister(...)) rather than
// spinning up their own.
func (r *Registry) PrometheusRegistry() *prometheus.Registry {
	return r.reg
}
