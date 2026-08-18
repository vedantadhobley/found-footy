// schema_migration_contract_test.go keeps the pending in-place migration's
// VerifySchema stamp synchronized with the authoritative flat schema.
package test_test

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"
)

var migrationSchemaHash = regexp.MustCompile(`schema_hash = '([0-9a-f]{64})'`)

func TestPendingMigrationStampsEmbeddedSchemaHash(t *testing.T) {
	root := repositoryRoot(t)
	schema := readToolingFile(t, root, "internal/infra/pg/schema.sql")
	migration := readToolingFile(t, root, "migrations/20260817_01_add_video_asset_hash_version.sql")

	match := migrationSchemaHash.FindStringSubmatch(migration)
	if len(match) != 2 {
		t.Fatal("pending migration does not contain one exact schema hash")
	}
	sum := sha256.Sum256([]byte(schema))
	want := hex.EncodeToString(sum[:])
	if match[1] != want {
		t.Fatalf("migration schema hash = %s, want %s", match[1], want)
	}
}
