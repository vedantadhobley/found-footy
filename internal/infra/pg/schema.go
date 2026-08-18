// schema.go — schema.sql embedded at build time + the boot-time drift guard.
//
// schema.sql is the flat, authoritative schema. It is applied only on a fresh
// volume (dev initdb) or an ephemeral testcontainer (WithInitScripts), which
// means an edit to schema.sql silently no-ops against an already-provisioned
// DB. A one-time operational migration may bridge an existing deployment to a
// new flat schema; it is not an application-side migration runner. VerifySchema
// converts silent divergence into a loud startup refusal: the live DB records
// the schema.sql fingerprint it was built from, and a binary whose embedded
// schema.sql differs will not run against it.
package pg

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Schema is the flat, authoritative schema SQL, embedded at build time from
// schema.sql. Applied on a fresh volume (dev initdb) or testcontainer
// (WithInitScripts); VerifySchema hashes it to fingerprint the live DB.
// In-place changes to an existing deployment use a reviewed operational SQL
// file under migrations/ and re-stamp this exact fingerprint. The application
// never discovers or runs migrations at startup; completed migration files are
// folded back into this flat schema after every environment has converged.
//
//go:embed schema.sql
var Schema string

// schemaVerifyLockKey is an arbitrary fixed key for a transaction-scoped
// advisory lock, so a concurrent worker+api startup stamps the baseline
// exactly once. Session-level locks don't survive pgxpool's per-statement
// connection hand-off, so VerifySchema runs its whole check in one tx and
// uses pg_advisory_xact_lock (auto-released at commit/rollback).
const schemaVerifyLockKey int64 = 0x_666f_6f74_795f_5343 // "footy_SC"

// SchemaHash is the hex SHA-256 of the embedded schema.sql — the fingerprint
// VerifySchema stamps and checks. Stable across builds (same bytes → same
// hash); go:embed embeds the file verbatim, so it equals `sha256sum
// schema.sql`.
func SchemaHash() string {
	sum := sha256.Sum256([]byte(Schema))
	return hex.EncodeToString(sum[:])
}

// VerifySchema guards against silent schema drift (audit P0-3). On the first
// run against a DB it records the embedded schema.sql fingerprint in a
// singleton schema_version row (the baseline stamp). On every later run it
// compares: a match proceeds; a mismatch returns an error so the caller
// refuses to boot rather than run against a DB that never received a
// schema.sql change. Idempotent + concurrency-safe via a transaction-scoped
// advisory lock.
//
// Re-stamp workflow: a fresh provision auto-stamps the new hash; an
// intentional in-place migration must `UPDATE schema_version SET schema_hash`
// to the new value once applied. The guard is fingerprint-level — it catches
// "schema.sql edited, DB not re-provisioned", not manual DB tampering.
func (p *Pool) VerifySchema(ctx context.Context) error {
	want := SchemaHash()

	tx, err := p.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg.VerifySchema: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize concurrent startups; auto-released when this tx ends.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", schemaVerifyLockKey); err != nil {
		return fmt.Errorf("pg.VerifySchema: lock: %w", err)
	}

	// The guard owns its bookkeeping table (not in schema.sql), so it works
	// against any DB, including one provisioned before the guard existed.
	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_version (
			id          int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			schema_hash text NOT NULL,
			applied_at  timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("pg.VerifySchema: ensure schema_version: %w", err)
	}

	var got string
	err = tx.QueryRow(ctx, "SELECT schema_hash FROM schema_version WHERE id = 1").Scan(&got)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_version (id, schema_hash) VALUES (1, $1)", want); err != nil {
			return fmt.Errorf("pg.VerifySchema: stamp baseline: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("pg.VerifySchema: commit stamp: %w", err)
		}
		p.ins.log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleInfraPG, vocabulary.ActionSchemaStamped,
			"schema baseline stamped", logging.String("schema_hash", shortHash(want)))
		return nil
	case err != nil:
		return fmt.Errorf("pg.VerifySchema: read schema_version: %w", err)
	}

	if got != want {
		// Read-only path — rollback (deferred) releases the lock.
		p.ins.log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionSchemaDrift,
			"schema drift: live DB does not match the embedded schema.sql",
			logging.String("db_hash", shortHash(got)),
			logging.String("binary_hash", shortHash(want)))
		return fmt.Errorf("pg.VerifySchema: schema drift — DB built from schema.sql %s, binary embeds %s; "+
			"migrate the DB (then UPDATE schema_version) or re-provision the volume",
			shortHash(got), shortHash(want))
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg.VerifySchema: commit: %w", err)
	}
	p.ins.log.Emit(ctx, logging.LevelDebug, vocabulary.ModuleInfraPG, vocabulary.ActionSchemaVerified,
		"schema verified", logging.String("schema_hash", shortHash(want)))
	return nil
}

// shortHash trims a hex fingerprint to a log-friendly prefix.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
