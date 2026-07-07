// Command worker is the Temporal worker binary — registers workflows and
// activities and processes tasks from the found-footy task queue. See §5
// orchestration + §16.5 Phase O for the workflows this binary hosts.
//
// Phase S2.4: opens the pg pool at startup, blocks until SIGINT/SIGTERM,
// closes the pool cleanly on shutdown. Workflow/activity registration
// lands in Phase O when the domain layer is ready to be plugged in.
package main

import (
	"context"

	"go.temporal.io/sdk/worker"

	"github.com/vedantadhobley/found-footy/internal/bootstrap"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
	"github.com/vedantadhobley/found-footy/internal/infra/nats"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/infra/s3"
	"github.com/vedantadhobley/found-footy/internal/infra/temporal"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11
// deploy tracking. Empty defaults for direct `go run` invocations.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("worker", gitSHA, builtAt, func(ctx context.Context, deps *bootstrap.Deps) error {
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
		_ = s3c // consumed by the video pipeline in Phase O
		// s3 client has no explicit Close (no persistent connection); no
		// closer needed — leaving it out is intentional and symmetric
		// with the aws-sdk-go-v2 client's lifecycle.

		llmIns := llm.RegisterMetrics(deps.Metrics, deps.Log)
		llmClient, err := llm.NewClient(ctx, deps.Cfg.LLM, llmIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("llm", func(_ context.Context) error {
			llmClient.Close()
			return nil
		})
		_ = llmClient // consumed by vision + RAG activities in Phase O

		tempIns := temporal.RegisterMetrics(deps.Metrics, deps.Log)
		tempClient, err := temporal.NewClient(ctx, deps.Cfg.Temporal, tempIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("temporal-client", func(_ context.Context) error {
			tempClient.Close()
			return nil
		})

		w := temporal.NewWorker(tempClient, tempIns, worker.Options{})
		// Phase O will register workflows + activities on `w` here.
		if err := w.Start(ctx); err != nil {
			return err
		}
		// Worker shutdown MUST run before its downstream deps close so
		// draining activities can still use pg/nats/s3. LIFO order
		// (temporal-worker registered last → drained first) gives us this.
		deps.RegisterCloser("temporal-worker", func(_ context.Context) error {
			w.Stop()
			return nil
		})

		// Domain workflows land here in Phase O. For now: hold the
		// adapters open until the signal-handled context cancels.
		<-ctx.Done()
		return nil
	})
}
