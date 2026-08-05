// Command api is the HTTP + SSE service serving the vedanta-systems
// frontend and any other external callers. Runs Chi + Huma per §8, with
// SSE fan-out subscribing to workspace NATS (§11 decision 2026-07-01).
//
// Phase S2.4: opens the pg pool at startup, blocks until SIGINT/SIGTERM,
// closes the pool cleanly on shutdown. Chi + Huma router lands in
// Phase A when the read endpoints are ready to serve real data.
package main

import (
	"context"
	"errors"
	"net/http"

	ffapi "github.com/vedantadhobley/found-footy/internal/api"
	"github.com/vedantadhobley/found-footy/internal/bootstrap"
	"github.com/vedantadhobley/found-footy/internal/infra/nats"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/infra/s3"
	"github.com/vedantadhobley/found-footy/internal/infra/temporal"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11
// deploy tracking.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("api", gitSHA, builtAt, func(ctx context.Context, deps *bootstrap.Deps) error {
		pgIns := pg.RegisterMetrics(deps.Metrics, deps.Log)
		pool, err := pg.New(ctx, deps.Cfg.Postgres, pgIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("pg", func(_ context.Context) error {
			pool.Close()
			return nil
		})

		natsIns := nats.RegisterMetrics(deps.Metrics, deps.Log)
		nc, err := nats.New(ctx, deps.Cfg.NATS, natsIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("nats", func(_ context.Context) error {
			nc.Close()
			return nil
		})

		s3Ins := s3.RegisterMetrics(deps.Metrics, deps.Log)
		s3c, err := s3.New(ctx, deps.Cfg.S3, s3Ins)
		if err != nil {
			return err
		}
		_ = s3c // consumed by the /videos/{share_id} presign path in Phase A
		// s3 client has no explicit Close (no persistent connection).

		tempIns := temporal.RegisterMetrics(deps.Metrics, deps.Log)
		tempClient, err := temporal.NewClient(ctx, deps.Cfg.Temporal, tempIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("temporal-client", func(_ context.Context) error {
			tempClient.Close()
			return nil
		})
		_ = tempClient // consumed by on-demand StartWorkflow endpoints in Phase A

		// Public read-API surface (#167a). Chi router on cfg.API.ListenAddr
		// (Caddy fronts it — container port only). Graceful drain is a closer
		// so SIGTERM stops accepting + finishes in-flight requests before the
		// pool/nats/temporal deps close (LIFO). A listen failure (e.g. port in
		// use) fails the binary fast rather than running degraded.
		srv := &http.Server{
			Addr:         deps.Cfg.API.ListenAddr,
			Handler:      ffapi.NewRouter(&ffapi.Handlers{}),
			ReadTimeout:  deps.Cfg.API.ReadTimeout,
			WriteTimeout: deps.Cfg.API.WriteTimeout,
		}
		serveErr := make(chan error, 1)
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErr <- err
			}
		}()
		deps.RegisterCloser("api-http", func(shutdownCtx context.Context) error {
			return srv.Shutdown(shutdownCtx)
		})

		select {
		case <-ctx.Done():
			return nil
		case err := <-serveErr:
			return err
		}
	})
}
