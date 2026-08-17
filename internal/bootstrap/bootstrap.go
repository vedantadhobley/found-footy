// Package bootstrap is the shared startup scaffold every found-footy
// binary uses. It wires config → logging → metrics → deploy-info gauge
// → /metrics HTTP listener → signal-handled context, then invokes a
// per-binary Work function with the resulting dependencies.
//
// Each command passes its binary name and build metadata into Run, then wires
// binary-specific work in the callback. Startup, health/metrics serving,
// signal handling, and shutdown ordering stay uniform.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
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
//
// Adapters that need shutdown cleanup call RegisterCloser at
// construction time; bootstrap iterates registered closers in
// reverse-registration order (LIFO — mirrors defer semantics) after
// Work returns, so the last adapter constructed is the first to drain.
type Deps struct {
	Cfg     *config.Config
	Log     logging.Emitter
	Metrics *metrics.Registry

	closers []adapterCloser
}

// adapterCloser is one registered shutdown hook: an adapter's name
// (used in the shutdown log line) + the function bootstrap invokes to
// close it under a bounded context.
type adapterCloser struct {
	name  string
	close func(context.Context) error
}

// RegisterCloser hooks name+closeFn into the reverse-order shutdown
// sequence. Adapters call this at construction time (after their
// New succeeded). The closer is invoked with a per-adapter bounded
// context (10s default); its return error is logged with the adapter
// name but does not stop remaining closers from running.
//
// Ordering guarantee: closers run in reverse registration order, so
// stacking construction (pg → nats → temporal) drains as
// (temporal → nats → pg). This is the property Temporal worker drain
// needs — worker must finish activities before its downstream deps
// (pg, nats) close underneath it.
func (d *Deps) RegisterCloser(name string, closeFn func(context.Context) error) {
	d.closers = append(d.closers, adapterCloser{name: name, close: closeFn})
}

// Work is the per-binary body that runs after startup scaffolding is
// wired. Work receives a context that gets canceled on SIGINT/SIGTERM
// — the binary should return promptly when ctx.Done() fires.
type Work func(ctx context.Context, deps *Deps) error

// Run is the standard binary lifecycle: load config, build observability
// scaffolding, emit startup, run Work under a signal-handled context,
// emit shutdown. Exit code is 0 on clean shutdown, 1 on failure.
//
// binary is the short name ("worker", "api", "twitter") used
// in the deploy_info gauge label and the startup/shutdown log lines.
// gitSHA + builtAt are injected via -ldflags at build time.
func Run(binary, gitSHA, builtAt string, work Work) {
	if err := run(binary, gitSHA, builtAt, work); err != nil {
		os.Exit(1)
	}
}

// run owns the testable process lifecycle below Run's exit-code boundary.
// It returns only after every registered adapter and the metrics listener have
// drained, or before Work starts when the metrics socket cannot be bound.
func run(binary, gitSHA, builtAt string, work Work) error {
	// Config load — before logger so we can pick up LogLevel/LogFormat.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return err
	}

	// Metrics registry FIRST — the logger depends on it so baseline
	// calls_total + log_lines_total counters can increment on every
	// Emit (§11 four-pillars principle: same emissions drive logs +
	// metrics).
	m := metrics.New()
	imageTag := os.Getenv("IMAGE_TAG")
	m.DeployInfo.WithLabelValues(binary, gitSHA, imageTag, builtAt).Set(1)

	// Logger init with the registry attached.
	log := logging.New(cfg.Observability, m)
	ctx := context.Background()

	log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionConfigLoaded,
		"configuration loaded",
		logging.String("binary", binary),
		logging.String("log_level", cfg.Observability.LogLevel),
		logging.String("log_format", cfg.Observability.LogFormat),
	)

	// Bind synchronously before Work starts. ListenAndServe hides the bind
	// inside its goroutine, which previously allowed a binary to run without
	// /metrics or /healthz and eventually exit zero.
	metricsSrv := &http.Server{
		Addr:              cfg.Observability.MetricsAddr,
		Handler:           newMetricsMux(m),
		ReadHeaderTimeout: 5 * time.Second,
	}
	metricsListener, err := net.Listen("tcp", metricsSrv.Addr)
	if err != nil {
		bindErr := fmt.Errorf("metrics listener bind %q: %w", metricsSrv.Addr, err)
		log.Emit(ctx, logging.LevelError, vocabulary.ModuleDeploy, vocabulary.ActionShutdown,
			"metrics listener bind failed",
			logging.String("binary", binary),
			logging.String("metrics_addr", metricsSrv.Addr),
			logging.Err(bindErr),
		)
		return bindErr
	}

	metricsErrCh := make(chan error, 1)
	go func() {
		err := metricsSrv.Serve(metricsListener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		metricsErrCh <- err
	}()

	log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionStartup,
		"binary started",
		logging.String("binary", binary),
		logging.String("git_sha", gitSHA),
		logging.String("built_at", builtAt),
		logging.String("image_tag", imageTag),
		logging.String("metrics_addr", cfg.Observability.MetricsAddr),
	)

	// Signal-handled context. A post-bind listener failure cancels the same
	// context so Work drains rather than continuing without health/metrics.
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	deps := &Deps{Cfg: cfg, Log: log, Metrics: m}
	workErrCh := make(chan error, 1)
	go func() { workErrCh <- work(ctx, deps) }()

	var workErr error
	var metricsErr error
	metricsResultConsumed := false
	select {
	case workErr = <-workErrCh:
	case metricsErr = <-metricsErrCh:
		metricsResultConsumed = true
		if metricsErr == nil {
			metricsErr = errors.New("metrics listener stopped unexpectedly")
		}
		cancel()
		workErr = errors.Join(fmt.Errorf("metrics listener: %w", metricsErr), <-workErrCh)
	}

	// Drain adapters in reverse-registration order. Runs whether Work
	// returned an error or not — a partial-startup failure still needs
	// to close whatever adapters DID come up. Each closer gets a
	// bounded ctx so a stuck adapter can't hold the binary open.
	for i := len(deps.closers) - 1; i >= 0; i-- {
		c := deps.closers[i]
		closerCtx, closerCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := c.close(closerCtx); err != nil {
			log.Emit(ctx, logging.LevelWarn, vocabulary.ModuleDeploy, vocabulary.ActionShutdownForced,
				"adapter close returned error",
				logging.String("adapter", c.name),
				logging.Err(err),
			)
		}
		closerCancel()
	}

	// Graceful metrics-server shutdown (last — adapter close emissions
	// still flow through the logger and into the counters).
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		log.Emit(ctx, logging.LevelWarn, vocabulary.ModuleDeploy, vocabulary.ActionShutdownForced,
			"metrics server shutdown timed out",
			logging.Err(err),
		)
	}

	// Drain the listener result when the Work completion path won the select.
	// Shutdown closes the listener before it waits for active handlers, so the
	// channel read completes even when the shutdown context expires.
	if !metricsResultConsumed {
		metricsErr = <-metricsErrCh
		if metricsErr != nil {
			workErr = errors.Join(workErr, fmt.Errorf("metrics listener: %w", metricsErr))
		}
	}
	if metricsErr != nil {
		log.Emit(ctx, logging.LevelError, vocabulary.ModuleDeploy, vocabulary.ActionShutdown,
			"metrics listener error",
			logging.Err(metricsErr),
		)
	}

	if workErr != nil {
		log.Emit(ctx, logging.LevelError, vocabulary.ModuleDeploy, vocabulary.ActionShutdown,
			"binary shutting down with error",
			logging.String("binary", binary),
			logging.Err(workErr),
		)
		return workErr
	}

	log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleDeploy, vocabulary.ActionShutdown,
		"binary shut down cleanly",
		logging.String("binary", binary),
	)
	return nil
}

// newMetricsMux wires the /metrics endpoint. Kept tiny — real HTTP
// surfaces belong in cmd/api or twitter/, this is only for scraping.
func newMetricsMux(m *metrics.Registry) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	// /healthz is a process-liveness check used by Compose. Dependency
	// readiness remains the adapters' startup responsibility.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// BlockUntilDone is a convenience Work function for a process that needs only
// the shared bootstrap lifecycle.
func BlockUntilDone(ctx context.Context, _ *Deps) error {
	<-ctx.Done()
	return nil
}
