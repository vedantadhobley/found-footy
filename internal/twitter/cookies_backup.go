// cookies_backup.go — cookie backup file management for T/b.
//
// The shared cookie backup file at /config/twitter_cookies.json is
// the fleet's coordination medium. All headless instances read from
// it, all can write to it. Filesystem mtime doubles as the "cookies
// changed since you last saw them" signal — no pub/sub needed.
//
// Design guards (settled in decisions.md 2026-07-21):
//   - auth_token presence check on BOTH read and write — never persist
//     or trust a session that's missing the load-bearing cookie.
//   - Atomic writes via temp + rename — concurrent readers see either
//     the full old file or the full new one, never a torn partial JSON.
//   - Fingerprint dedupe — sha256 of the complete sorted cookie shape; skip
//     the write entirely when the cookie set hasn't changed since the
//     last-in-memory fingerprint. Kills the 60-80% of Python's
//     redundant writes when Twitter didn't rotate a token.
//
// Load direction (LoadCookies) lives in browser.go alongside the
// Playwright context that receives the cookies.
package twitter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// authTokenName is the load-bearing Twitter cookie whose presence
// certifies "we have a session." Absent → the cookie set is broken
// and we refuse to persist it (or trust it on restore).
const authTokenName = "auth_token"

// CookieFingerprint is a stable hash of a cookie set — sha256 over
// the complete sorted cookie shape. Instances keep the fingerprint of
// what they last wrote in memory; if the current browser fingerprint
// matches, the write is skipped.
type CookieFingerprint [sha256.Size]byte

// Hex renders the fingerprint as a short hex string for log fields.
func (f CookieFingerprint) Hex() string { return hex.EncodeToString(f[:]) }

// Fingerprint computes the deterministic hash of a cookie set.
// Order-independent — sorts by every persisted field before hashing so the
// same logical set always produces the same fingerprint regardless
// of the order Playwright returned them.
//
// Every persisted attribute contributes to the hash. Twitter can extend a
// cookie's expiry without rotating its value; ignoring that change would leave
// the shared backup with the old expiry and undo the refresh on the next cold
// browser. The complete persisted shape is the simplest definition of "did
// the cookie snapshot change?".
func Fingerprint(cookies []Cookie) CookieFingerprint {
	if len(cookies) == 0 {
		return CookieFingerprint{}
	}
	sorted := make([]Cookie, len(cookies))
	copy(sorted, cookies)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		if sorted[i].Domain != sorted[j].Domain {
			return sorted[i].Domain < sorted[j].Domain
		}
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		if sorted[i].Value != sorted[j].Value {
			return sorted[i].Value < sorted[j].Value
		}
		if sorted[i].Expires != sorted[j].Expires {
			return sorted[i].Expires < sorted[j].Expires
		}
		if sorted[i].HTTPOnly != sorted[j].HTTPOnly {
			return !sorted[i].HTTPOnly
		}
		if sorted[i].Secure != sorted[j].Secure {
			return !sorted[i].Secure
		}
		return sorted[i].SameSite < sorted[j].SameSite
	})
	data, _ := json.Marshal(sorted) // Cookie contains only JSON-safe scalar fields.
	return sha256.Sum256(data)
}

// hasAuthToken reports whether the cookie set contains a non-empty
// auth_token entry. Load-bearing guard for both write (refuse to
// persist a broken set) and read-side validation (log + treat file
// as untrusted if this fails on restore).
func hasAuthToken(cookies []Cookie) bool {
	for _, c := range cookies {
		if c.Name == authTokenName && c.Value != "" {
			return true
		}
	}
	return false
}

// filterToTwitterDomain returns only exact X/Twitter domains or their
// subdomains. A substring check would also accept attacker-controlled domains
// such as notx.com.
func filterToTwitterDomain(cookies []Cookie) []Cookie {
	out := make([]Cookie, 0, len(cookies))
	for _, c := range cookies {
		if isTwitterDomain(c.Domain) {
			out = append(out, c)
		}
	}
	return out
}

func isTwitterDomain(domain string) bool {
	domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	return domain == "x.com" || strings.HasSuffix(domain, ".x.com") ||
		domain == "twitter.com" || strings.HasSuffix(domain, ".twitter.com")
}

// WriteBackup writes a cookie set to the backup file at path.
//
// Guards (any failure returns error, file NOT written):
//   - cookies is nil/empty → refuse (nothing to persist)
//   - auth_token missing or empty → refuse (broken session; would
//     poison other instances that restore from this file)
//
// Write is atomic — data goes to path.tmp then os.Rename to path,
// which is atomic on the same filesystem (POSIX guarantee). Readers
// either see the full old file or the full new one, never torn.
// Concurrent writers race safely — last-rename-wins, no corruption.
//
// exportedAt is embedded in the envelope; passed in so tests can
// pin it deterministically. Prod callers pass time.Now().UTC().
func WriteBackup(path string, cookies []Cookie, exportedAt time.Time) error {
	filtered := filterToTwitterDomain(cookies)
	if len(filtered) == 0 {
		return fmt.Errorf("twitter.WriteBackup: no x.com/twitter.com cookies to persist")
	}
	if !hasAuthToken(filtered) {
		return fmt.Errorf("twitter.WriteBackup: refusing to write cookie set without auth_token (would break other instances on restore)")
	}

	envelope := CookieBackup{
		ExportedAt: exportedAt.UTC().Format(time.RFC3339),
		Cookies:    filtered,
	}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("twitter.WriteBackup: marshal: %w", err)
	}

	// Atomic write via unique temp file + rename. Using os.CreateTemp
	// for a per-writer-unique tmp filename — a shared "path.tmp" name
	// would let concurrent writers clobber each other's tmp file
	// mid-write, and a rename would then move a partial file into
	// place. Unique names + rename gives correct concurrent-writer
	// semantics: last rename wins, no partial files ever land at path.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("twitter.WriteBackup: mkdir parent: %w", err)
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("twitter.WriteBackup: create temp: %w", err)
	}
	tmpPath := tmpFile.Name()
	// If anything below fails, remove the temp file we created.
	// Rename-success path also removes it via the rename itself.
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("twitter.WriteBackup: write temp: %w", err)
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("twitter.WriteBackup: chmod temp: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("twitter.WriteBackup: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("twitter.WriteBackup: rename to %s: %w", path, err)
	}
	cleanupTmp = false // rename moved the file; nothing to remove
	return nil
}

// ReadBackup reads and parses the backup file at path. Returns the
// cookies + the envelope's exported_at timestamp. Errors on missing
// file, malformed JSON, or a cookie set that fails the auth_token
// guard.
//
// The auth_token check here is symmetric with WriteBackup — if a
// file somehow ended up on disk without auth_token (corrupted mid-
// write on a system without atomic rename, or hand-edited), we
// refuse to trust it. Callers can decide whether to treat this as
// "no cookies, need manual login" vs a hard error.
func ReadBackup(path string) ([]Cookie, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("twitter.ReadBackup: read %s: %w", path, err)
	}
	var envelope CookieBackup
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, "", fmt.Errorf("twitter.ReadBackup: parse %s: %w", path, err)
	}
	if len(envelope.Cookies) == 0 {
		return nil, envelope.ExportedAt, fmt.Errorf("twitter.ReadBackup: %s: empty cookie set", path)
	}
	if !hasAuthToken(envelope.Cookies) {
		return nil, envelope.ExportedAt, fmt.Errorf("twitter.ReadBackup: %s: cookie set missing auth_token (untrusted)", path)
	}
	return envelope.Cookies, envelope.ExportedAt, nil
}

// BackupFileMtime returns the file's modification time or an error
// (typically "not exists"). Used by EnsureAuthenticated to decide
// whether to reload cookies before an auth check — if mtime is
// newer than what we last loaded, another instance or the VNC
// container wrote fresh cookies and we should pick them up.
func BackupFileMtime(path string) (time.Time, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return stat.ModTime(), nil
}
