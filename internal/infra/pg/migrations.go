// Ordered, checksummed, transactional Postgres schema migration runner.
package pg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

const schemaMigrationLockKey int64 = 0x_666f_6f74_795f_4d47 // "footy_MG"

var (
	migrationNamePattern = regexp.MustCompile(`^\d{8}_\d{2}_[a-z0-9_]+\.sql$`)
	schemaHashPattern    = regexp.MustCompile(`^-- schema-hash: ([0-9a-f]{64})$`)
	transactionPattern   = regexp.MustCompile(`(?im)^\s*(BEGIN|COMMIT|ROLLBACK)\s*;`)
	concurrentIndex      = regexp.MustCompile(`(?i)CREATE\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY`)
)

type migration struct {
	version    string
	name       string
	checksum   string
	schemaHash string
	sql        string
}

// Migrate validates the embedded chain, serializes concurrent service starts,
// applies every pending migration in one transaction, verifies required schema
// objects, and records the current flat-schema compatibility fingerprint.
func (p *Pool) Migrate(ctx context.Context, migrationFS fs.FS) (returnErr error) {
	defer func() {
		if returnErr != nil {
			p.ins.log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionSchemaDrift,
				"database migration or schema verification failed", logging.Err(returnErr))
		}
	}()
	migrations, err := loadMigrations(migrationFS)
	if err != nil {
		return fmt.Errorf("pg.Migrate: load: %w", err)
	}
	if err := validateMigrationTarget(migrations); err != nil {
		return fmt.Errorf("pg.Migrate: %w", err)
	}

	tx, err := p.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg.Migrate: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("pg.Migrate: lock: %w", err)
	}
	if err := ensureMigrationLedger(ctx, tx); err != nil {
		return err
	}
	if err := verifyBaselineSchema(ctx, tx); err != nil {
		return fmt.Errorf("pg.Migrate: baseline: %w", err)
	}

	applied, err := readAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	known := make(map[string]migration, len(migrations))
	for _, m := range migrations {
		known[m.version] = m
		if row, ok := applied[m.version]; ok {
			if row.name != m.name || row.checksum != m.checksum || row.schemaHash != m.schemaHash {
				return fmt.Errorf("pg.Migrate: applied migration %s differs from embedded chain", m.version)
			}
		}
	}
	for version := range applied {
		if _, ok := known[version]; !ok {
			return fmt.Errorf("pg.Migrate: database has unknown newer migration %s", version)
		}
	}
	seenPending := false
	for _, m := range migrations {
		_, ok := applied[m.version]
		if !ok {
			seenPending = true
			continue
		}
		if seenPending {
			return fmt.Errorf("pg.Migrate: migration ledger has a gap before %s", m.version)
		}
	}

	var appliedNow []migration
	for _, m := range migrations {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if _, err := tx.Exec(ctx, m.sql, pgx.QueryExecModeSimpleProtocol); err != nil {
			return fmt.Errorf("pg.Migrate: apply %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO schema_migrations (version, name, checksum, schema_hash)
			VALUES ($1, $2, $3, $4)
		`, m.version, m.name, m.checksum, m.schemaHash); err != nil {
			return fmt.Errorf("pg.Migrate: record %s: %w", m.name, err)
		}
		appliedNow = append(appliedNow, m)
	}

	if err := verifyCurrentSchema(ctx, tx); err != nil {
		return fmt.Errorf("pg.Migrate: final schema: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO schema_version (id, schema_hash, applied_at)
		VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE
		SET schema_hash = EXCLUDED.schema_hash, applied_at = EXCLUDED.applied_at
	`, SchemaHash()); err != nil {
		return fmt.Errorf("pg.Migrate: stamp schema: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg.Migrate: commit: %w", err)
	}
	for _, m := range appliedNow {
		p.ins.log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleInfraPG, vocabulary.ActionMigrationApplied,
			"database migration applied",
			logging.String("migration", m.name),
			logging.String("checksum", shortHash(m.checksum)))
	}
	p.ins.log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleInfraPG, vocabulary.ActionSchemaVerified,
		"database migrations and required schema verified",
		logging.Int("applied_count", len(appliedNow)),
		logging.String("schema_hash", shortHash(SchemaHash())))
	return nil
}

// VerifyMigrations is the application startup gate. It performs no schema
// mutation: the dedicated migrate command must have committed the exact
// embedded chain first. A shared advisory lock waits for an in-progress
// migration instead of racing its ledger transaction.
func (p *Pool) VerifyMigrations(ctx context.Context, migrationFS fs.FS) (returnErr error) {
	defer func() {
		if returnErr != nil {
			p.ins.log.Emit(ctx, logging.LevelError, vocabulary.ModuleInfraPG, vocabulary.ActionSchemaDrift,
				"database migration verification failed", logging.Err(returnErr))
		}
	}()
	migrations, err := loadMigrations(migrationFS)
	if err != nil {
		return fmt.Errorf("pg.VerifyMigrations: load: %w", err)
	}
	if err := validateMigrationTarget(migrations); err != nil {
		return fmt.Errorf("pg.VerifyMigrations: %w", err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg.VerifyMigrations: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("pg.VerifyMigrations: lock: %w", err)
	}
	var ledgerExists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&ledgerExists); err != nil {
		return fmt.Errorf("pg.VerifyMigrations: check ledger: %w", err)
	}
	if !ledgerExists {
		return fmt.Errorf("pg.VerifyMigrations: migration ledger is missing; run the migrate command")
	}
	applied, err := readAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(migrations, applied); err != nil {
		return fmt.Errorf("pg.VerifyMigrations: %w", err)
	}
	if err := verifyCurrentSchema(ctx, tx); err != nil {
		return fmt.Errorf("pg.VerifyMigrations: schema: %w", err)
	}
	var storedHash string
	if err := tx.QueryRow(ctx, `SELECT schema_hash FROM schema_version WHERE id = 1`).Scan(&storedHash); err != nil {
		return fmt.Errorf("pg.VerifyMigrations: read schema stamp: %w", err)
	}
	if storedHash != SchemaHash() {
		return fmt.Errorf("pg.VerifyMigrations: schema stamp %s does not match release %s", shortHash(storedHash), shortHash(SchemaHash()))
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg.VerifyMigrations: commit: %w", err)
	}
	p.ins.log.Emit(ctx, logging.LevelInfo, vocabulary.ModuleInfraPG, vocabulary.ActionSchemaVerified,
		"database migration ledger and required schema verified",
		logging.Int("migration_count", len(migrations)),
		logging.String("schema_hash", shortHash(SchemaHash())))
	return nil
}

func validateMigrationTarget(migrations []migration) error {
	if len(migrations) == 0 {
		return fmt.Errorf("no migrations embedded")
	}
	if got, want := migrations[len(migrations)-1].schemaHash, SchemaHash(); got != want {
		return fmt.Errorf("newest migration targets schema %s, schema.sql is %s", shortHash(got), shortHash(want))
	}
	return nil
}

func validateAppliedMigrations(migrations []migration, applied map[string]appliedMigration) error {
	known := make(map[string]migration, len(migrations))
	for _, m := range migrations {
		known[m.version] = m
		row, ok := applied[m.version]
		if !ok {
			return fmt.Errorf("migration %s is pending; run the migrate command", m.name)
		}
		if row.name != m.name || row.checksum != m.checksum || row.schemaHash != m.schemaHash {
			return fmt.Errorf("applied migration %s differs from embedded chain", m.version)
		}
	}
	for version := range applied {
		if _, ok := known[version]; !ok {
			return fmt.Errorf("database has unknown newer migration %s", version)
		}
	}
	return nil
}

func loadMigrations(migrationFS fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, ".")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if !migrationNamePattern.MatchString(entry.Name()) {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		b, err := fs.ReadFile(migrationFS, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		sql := string(b)
		firstLine, _, _ := strings.Cut(sql, "\n")
		match := schemaHashPattern.FindStringSubmatch(strings.TrimSpace(firstLine))
		if len(match) != 2 {
			return nil, fmt.Errorf("%s: first line must be '-- schema-hash: <sha256>'", entry.Name())
		}
		if transactionPattern.MatchString(sql) || concurrentIndex.MatchString(sql) {
			return nil, fmt.Errorf("%s: migrations run inside one transaction; transaction control and concurrent indexes are forbidden", entry.Name())
		}
		sum := sha256.Sum256(b)
		out = append(out, migration{
			version:    strings.TrimSuffix(entry.Name(), ".sql"),
			name:       entry.Name(),
			checksum:   hex.EncodeToString(sum[:]),
			schemaHash: match[1],
			sql:        sql,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i-1].version >= out[i].version {
			return nil, fmt.Errorf("migration versions are not strictly ordered")
		}
	}
	return out, nil
}

func ensureMigrationLedger(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version text PRIMARY KEY,
			name text NOT NULL UNIQUE,
			checksum text NOT NULL CHECK (length(checksum) = 64),
			schema_hash text NOT NULL CHECK (length(schema_hash) = 64),
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("pg.Migrate: ensure migration ledger: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_version (
			id int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			schema_hash text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("pg.Migrate: ensure schema stamp: %w", err)
	}
	return nil
}

type appliedMigration struct {
	name       string
	checksum   string
	schemaHash string
}

func readAppliedMigrations(ctx context.Context, tx pgx.Tx) (map[string]appliedMigration, error) {
	rows, err := tx.Query(ctx, `SELECT version, name, checksum, schema_hash FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("pg.Migrate: read ledger: %w", err)
	}
	defer rows.Close()
	out := make(map[string]appliedMigration)
	for rows.Next() {
		var version string
		var row appliedMigration
		if err := rows.Scan(&version, &row.name, &row.checksum, &row.schemaHash); err != nil {
			return nil, fmt.Errorf("pg.Migrate: scan ledger: %w", err)
		}
		out[version] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.Migrate: ledger rows: %w", err)
	}
	return out, nil
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
