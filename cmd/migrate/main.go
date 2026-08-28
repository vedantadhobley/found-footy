// Command migrate applies Found Footy's embedded Postgres migration chain.
package main

import (
	"context"

	"github.com/vedantadhobley/found-footy/migrations"

	"github.com/vedantadhobley/found-footy/internal/bootstrap"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("migrate", gitSHA, builtAt, func(ctx context.Context, deps *bootstrap.Deps) error {
		ins := pg.RegisterMetrics(deps.Metrics, deps.Log)
		pool, err := pg.New(ctx, deps.Cfg.Postgres, ins)
		if err != nil {
			return err
		}
		deps.RegisterCloser("pg", func(_ context.Context) error {
			pool.Close()
			return nil
		})
		return pool.Migrate(ctx, migrations.FS)
	})
}
