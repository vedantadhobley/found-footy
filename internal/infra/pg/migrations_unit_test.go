// Unit tests for migration-chain parsing and transaction-safety guards.
package pg

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadMigrationsRejectsTransactionControl(t *testing.T) {
	data := "-- schema-hash: " + strings.Repeat("a", 64) + "\nBEGIN;\nSELECT 1;\nCOMMIT;\n"
	_, err := loadMigrations(fstest.MapFS{
		"20260828_01_bad.sql": {Data: []byte(data)},
	})
	if err == nil {
		t.Fatal("transaction control was accepted")
	}
}

func TestLoadMigrationsSortsAndChecksums(t *testing.T) {
	header := "-- schema-hash: " + strings.Repeat("b", 64) + "\n"
	got, err := loadMigrations(fstest.MapFS{
		"20260828_02_second.sql": {Data: []byte(header + "SELECT 2;\n")},
		"20260828_01_first.sql":  {Data: []byte(header + "SELECT 1;\n")},
	})
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(got) != 2 || got[0].version != "20260828_01_first" || len(got[0].checksum) != 64 {
		t.Fatalf("loaded chain = %+v", got)
	}
}
