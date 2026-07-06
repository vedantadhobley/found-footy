// Package bootstrap is the shared startup scaffold every found-footy
// binary uses. It wires config → logging → metrics → deploy-info gauge
// → /metrics HTTP listener → signal-handled context, then invokes a
// per-binary Work function with the resulting dependencies.
//
// Each cmd/*/main.go becomes ~10 lines: pass its binary name +
// build-time ldflags into Run, and inside the callback do binary-
// specific work (Temporal worker registration, HTTP router setup, etc).
// Later phases fill in what the Work function does. In Phase S1, every
// binary's Work is just "block until context canceled" — the point is
// that startup + shutdown observability is uniform before any real
// logic exists.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Deps is the dependency bundle handed to each binary's Work function.
// Adapter constructors take this bundle rather than reaching into
// package-level globals — matches the ports-and-adapters pattern from
// rebuild-plan.md §2.
type Deps struct {
	Cfg     *config.Config
	Log     logging.Emitter
	Metrics *metrics.Registry
}

// Work is the per-binary body that runs after startup scaffolding is
// wired. Work receives a context that gets canceled on SIGINT/SIGTERM
// — the binary should return promptly when ctx.Done() fires.
type Work func(ctx context.Context, deps *Deps) error

// Run is the standard binary lifecycle: load config, build observability
// scaffolding, emit startup, run Work under a signal-handled context,
// emit shutdown. Exit code is 0 on clean shutdown, 1 on failure.
//
// binary is the short name ("worker", "api", "scaler", "twitter") used
// in the deploy_info gauge label and the startup/shutdown log lines.
// gitSHA + builtAt are injected via -ldflags at build time.
func Run(binary, gitSHA, builtAt string, work Work) {
	// Config load — before logger so we can pick up LogLevel/LogFormat.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		os.Exit(1)
	}

	// Logger init.
	log := logging.New(cfg.Observability)
	ctx := context.Background()

	log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionConfigLoaded,
		"configuration loaded",
		logging.String("binary", binary),
		logging.String("log_level", cfg.Observability.LogLevel),
		logging.String("log_format", cfg.Observability.LogFormat),
		logging.Bool("loki_enabled", cfg.Observability.LokiEnabled),
	)

	// Metrics registry + deploy-info gauge. Empty image_tag for now —
	// docker-compose passes it via env when a real tag exists.
	m := metrics.New()
	m.DeployInfo.WithLabelValues(binary, gitSHA, os.Getenv("IMAGE_TAG"), builtAt).Set(1)

	// Metrics HTTP listener. Runs on a goroutine so signal handling
	// stays on the main goroutine; ListenAndServe errors are logged
	// and forwarded to shutdown.
	metricsErrCh := make(chan error, 1)
	metricsSrv := &http.Server{
		Addr:              cfg.Observability.MetricsAddr,
		Handler:           newMetricsMux(m),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			metricsErrCh <- err
			return
		}
		metricsErrCh <- nil
	}()

	log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionStartup,
		"binary started",
		logging.String("binary", binary),
		logging.String("git_sha", gitSHA),
		logging.String("built_at", builtAt),
		logging.String("metrics_addr", cfg.Observability.MetricsAddr),
	)

	// Signal-handled context. Work returns when ctx.Done() fires.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	deps := &Deps{Cfg: cfg, Log: log, Metrics: m}
	workErr := work(ctx, deps)

	// Graceful metrics-server shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		log.Emit(ctx, logging.LevelWarn, vocabulary.ModuleDeploy, vocabulary.ActionShutdownForced,
			"metrics server shutdown timed out",
			logging.Err(err),
		)
	}

	// Drain any listener error the goroutine posted.
	select {
	case err := <-metricsErrCh:
		if err != nil {
			log.Emit(ctx, logging.LevelError, vocabulary.ModuleDeploy, vocabulary.ActionShutdown,
				"metrics listener error",
				logging.Err(err),
			)
		}
	default:
	}

	if workErr != nil {
		log.Emit(ctx, logging.LevelError, vocabulary.ModuleDeploy, vocabulary.ActionShutdown,
			"binary shutting down with error",
			logging.String("binary", binary),
			logging.Err(workErr),
		)
		os.Exit(1)
	}

	log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionShutdown,
		"binary shut down cleanly",
		logging.String("binary", binary),
	)
}

// newMetricsMux wires the /metrics endpoint. Kept tiny — real HTTP
// surfaces belong in cmd/api or twitter/, this is only for scraping.
func newMetricsMux(m *metrics.Registry) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	// /healthz is a bare 200; per §11 real liveness/readiness lands with
	// Chi + Huma in Phase A. Sufficient for compose healthchecks now.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// BlockUntilDone is a convenience Work function for binaries that
// don't have anything real to do yet — they just need to stay running
// so signal + observability plumbing works end-to-end. Every S1 main
// uses this; adapters replace it as they land.
func BlockUntilDone(ctx context.Context, _ *Deps) error {
	<-ctx.Done()
	return nil
}
