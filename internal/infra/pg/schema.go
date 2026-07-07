package pg

import _ "embed"

// Schema is the initial-schema SQL, embedded at build time from
// schema.sql. Consumers use it two ways today:
//
//  1. Tests apply it via tcpostgres.WithInitScripts("schema.sql")
//     (which reads the file directly — no need for this string).
//  2. Future migration tooling / dev bootstrap can execute it via
//     pool.Exec(ctx, pg.Schema) without shipping the .sql file
//     separately.
//
// The file at internal/infra/pg/schema.sql is authoritative. If a
// consumer wants an ordered set of migrations later (goose etc.),
// this constant gets promoted into an embed.FS of migration files.
//
//go:embed schema.sql
var Schema string
