// PGConfig — env-driven settings for the Postgres adapter (DSN + pool sizing).
package config

import "time"

// PGConfig covers the Postgres substrate every binary that touches the
// database uses. Single DSN is authoritative — split-field form gets
// added if a secret manager forces it, but a DSN keeps local dev +
// tests simple.
//
// PG_DSN is not tagged required at the env layer because Phase S1
// binaries don't need Postgres yet; the pg package's constructor
// returns a descriptive error when a binary that DOES need it starts
// without the env var set.
type PGConfig struct {
	// DSN is a libpq / pgx connection string. Example:
	//   postgres://ffuser:ffpass@postgres:5432/found_footy?sslmode=disable
	// Env name is PG_DSN to match the naming convention scaffolded in
	// .env / .env.example.
	DSN string `env:"PG_DSN"`

	// MaxConns caps the pool per binary. 10 × 8 worker replicas + api
	// stays under Postgres's default max_connections=100. Retune
	// after Phase O has real load data.
	MaxConns int32 `env:"PG_MAX_CONNS" envDefault:"10"`

	// MinConns keeps a warm connection floor to absorb burst without
	// paying handshake cost every time.
	MinConns int32 `env:"PG_MIN_CONNS" envDefault:"2"`

	// ConnectTimeout bounds the initial connect handshake. Independent of
	// per-query context timeouts.
	ConnectTimeout time.Duration `env:"PG_CONNECT_TIMEOUT" envDefault:"10s"`
}
