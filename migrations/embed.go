// Package migrations embeds Found Footy's ordered Postgres migration chain.
package migrations

import "embed"

// FS contains every immutable ordered SQL migration. The Postgres adapter
// validates names, checksums, and target schema hashes before applying it.
//
//go:embed *.sql
var FS embed.FS
