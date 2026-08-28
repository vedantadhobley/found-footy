// Embedded fresh-install schema and its release fingerprint.
package pg

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// Schema is the authoritative fresh-install snapshot. Existing databases move
// between snapshots through the ordered embedded migration chain.
//
//go:embed schema.sql
var Schema string

// SchemaHash is the exact SHA-256 release identity of schema.sql.
func SchemaHash() string {
	sum := sha256.Sum256([]byte(Schema))
	return hex.EncodeToString(sum[:])
}
