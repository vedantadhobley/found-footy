// PGConfig — env-driven settings for the Postgres adapter (DSN + pool sizing).
package config

import "time"

// PGConfig covers the Postgres substrate every binary that touches the
// database uses. Single DSN is authoritative — split-field form gets
// added if a secret manager forces it, but a DSN keeps local dev +
// tests simple.
//
// PG_DSN is not tagged required at the env layer; the adapter constructor
// returns a descriptive error when a consuming binary starts without it.
type PGConfig struct {
	// DSN is a libpq / pgx connection string. Example:
	//   postgres://ffuser:ffpass@postgres:5432/found_footy?sslmode=disable
	// Env name is PG_DSN to match the naming convention scaffolded in
	// .env / .env.example.
	DSN string `env:"PG_DSN"`

	// MaxConns caps the pool per binary. Reconcile this with the deployed
	// replica count and Postgres connection budget before raising it.
	MaxConns int32 `env:"PG_MAX_CONNS" envDefault:"10"`

	// MinConns keeps a warm connection floor to absorb burst without
	// paying handshake cost every time.
	MinConns int32 `env:"PG_MIN_CONNS" envDefault:"2"`

	// ConnectTimeout bounds the initial connect handshake. Independent of
	// per-query context timeouts.
	ConnectTimeout time.Duration `env:"PG_CONNECT_TIMEOUT" envDefault:"10s"`
}
