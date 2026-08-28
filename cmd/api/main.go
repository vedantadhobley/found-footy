// Command api serves the read-only Chi HTTP contract consumed by
// vedanta-systems. It reads Postgres, presigns Garage objects, and does not own
// SSE fan-out. See docs/api.md.
package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/vedantadhobley/found-footy/migrations"

	ffapi "github.com/vedantadhobley/found-footy/internal/api"
	"github.com/vedantadhobley/found-footy/internal/bootstrap"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/infra/s3"
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
		// The dedicated migrate command owns schema mutation. API startup is a
		// read-only gate on the exact checksummed ledger and required objects.
		if err := pool.VerifyMigrations(ctx, migrations.FS); err != nil {
			return err
		}

		s3Ins := s3.RegisterMetrics(deps.Metrics, deps.Log)
		s3c, err := s3.New(ctx, deps.Cfg.S3, s3Ins)
		if err != nil {
			return err
		}
		// s3 client has no explicit Close (no persistent connection); the
		// share-redirect handler presigns GETs through it.

		// No Temporal client here. The read API serves fixtures/events/videos
		// from Postgres + S3 and does not use Temporal. A prior placeholder
		// (`_ = tempClient`, for hypothetical on-demand StartWorkflow
		// endpoints) constructed a client whose FATAL health probe took the
		// whole public API down when Temporal briefly blinked during an air
		// rebuild (2026-08-14) — a read surface must not die on a service it
		// doesn't use. Re-add it lazily / non-fatally (cf. #170) when the
		// StartWorkflow endpoints actually exist. See decisions.md 2026-08-14.

		// Public read-API surface. Chi router on cfg.API.ListenAddr
		// (Caddy fronts it — container port only). Graceful drain is a closer
		// so SIGTERM stops accepting + finishes in-flight requests before the
		// Postgres closes after HTTP drains (LIFO). A listen failure (e.g. port in
		// use) fails the binary fast rather than running degraded.
		handlers := &ffapi.Handlers{
			Fixtures:   pg.NewFixtureRepo(pool),
			Events:     pg.NewEventRepo(pool),
			Videos:     pg.NewShareRepo(pool),
			Presign:    s3c,
			Bucket:     s3c.Bucket(),
			PresignTTL: deps.Cfg.S3.PresignedURLTTL,
			Log:        deps.Log,
		}
		srv := &http.Server{
			Addr:         deps.Cfg.API.ListenAddr,
			Handler:      ffapi.NewRouter(handlers),
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
