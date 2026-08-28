// schema_migration_contract_test.go keeps the newest ordered migration target
// synchronized with the authoritative fresh-install schema.
package test_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var migrationSchemaHash = regexp.MustCompile(`(?m)^-- schema-hash: ([0-9a-f]{64})$`)

func TestNewestMigrationTargetsEmbeddedSchemaHash(t *testing.T) {
	root := repositoryRoot(t)
	schema := readToolingFile(t, root, "internal/infra/pg/schema.sql")
	entries, err := os.ReadDir(filepath.Join(root, "migrations"))
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		t.Fatal("no ordered migrations found")
	}
	sort.Strings(names)
	migration := readToolingFile(t, root, filepath.Join("migrations", names[len(names)-1]))

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
