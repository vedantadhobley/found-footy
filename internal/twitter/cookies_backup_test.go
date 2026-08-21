// Unit tests for the cookie backup file management. Pure Go, no
// browser or Playwright involved.
package twitter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Baseline cookie set with auth_token — used across most tests.
func validCookies() []Cookie {
	return []Cookie{
		{Name: "auth_token", Value: "abc123", Domain: ".x.com", Path: "/", Secure: true, HTTPOnly: true},
		{Name: "ct0", Value: "csrf_xyz", Domain: ".x.com", Path: "/", Secure: true},
		{Name: "twid", Value: "u=12345", Domain: ".x.com", Path: "/", Secure: true},
	}
}

// TestFingerprint_Stable — identical cookie sets in different orders
// produce identical fingerprints.
func TestFingerprint_Stable(t *testing.T) {
	c1 := validCookies()
	c2 := []Cookie{c1[2], c1[0], c1[1]} // reversed order
	if Fingerprint(c1) != Fingerprint(c2) {
		t.Errorf("Fingerprint not order-independent")
	}
}

// TestFingerprint_ChangesOnValueRotation — Twitter refreshing csrf
// (rotating ct0's value) produces a different fingerprint. This is
// the discriminator that lets the write-if-changed path fire.
func TestFingerprint_ChangesOnValueRotation(t *testing.T) {
	c1 := validCookies()
	c2 := validCookies()
	c2[1].Value = "csrf_ROTATED" // ct0 gets a new value
	if Fingerprint(c1) == Fingerprint(c2) {
		t.Errorf("Fingerprint didn't change on value rotation — dedupe would skip a legitimate refresh")
	}
}

func TestFingerprint_ChangesOnExpiryRefresh(t *testing.T) {
	c1 := validCookies()
	c1[0].Expires = 1_800_000_000
	c2 := validCookies()
	c2[0].Expires = 1_900_000_000
	if Fingerprint(c1) == Fingerprint(c2) {
		t.Fatal("expiry-only refresh did not change fingerprint")
	}
}

// TestFingerprint_EmptyIsZero — empty cookie set gives a distinct
// zero-value fingerprint; used to bootstrap the in-memory
// last-fingerprint tracker.
func TestFingerprint_EmptyIsZero(t *testing.T) {
	var zero CookieFingerprint
	if Fingerprint(nil) != zero {
		t.Errorf("empty cookies should hash to zero-value fingerprint")
	}
	if Fingerprint([]Cookie{}) != zero {
		t.Errorf("empty slice should hash to zero-value fingerprint")
	}
}

// TestWriteBackup_HappyPath — atomic write produces a readable file
// with the correct envelope + cookies.
func TestWriteBackup_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twitter_cookies.json")

	at := time.Date(2026, 8, 15, 14, 23, 15, 0, time.UTC)
	if err := WriteBackup(path, validCookies(), at); err != nil {
		t.Fatalf("WriteBackup: %v", err)
	}

	// File exists, parses as envelope, has the auth_token.
	got, exportedAt, err := ReadBackup(path)
	if err != nil {
		t.Fatalf("ReadBackup: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("cookies read = %d, want 3", len(got))
	}
	if exportedAt != "2026-08-15T14:23:15Z" {
		t.Errorf("exported_at = %q, want RFC3339 UTC", exportedAt)
	}
	if !hasAuthToken(got) {
		t.Errorf("read cookies missing auth_token")
	}
}

// TestWriteBackup_RefusesWithoutAuthToken — the load-bearing guard.
// Never persist a cookie set that would break other instances on
// restore.
func TestWriteBackup_RefusesWithoutAuthToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twitter_cookies.json")

	broken := []Cookie{
		{Name: "ct0", Value: "csrf_xyz", Domain: ".x.com"},
		{Name: "twid", Value: "u=12345", Domain: ".x.com"},
		// no auth_token — Twitter session is broken
	}
	err := WriteBackup(path, broken, time.Now())
	if err == nil {
		t.Fatal("expected refusal to write without auth_token, got no error")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Errorf("file was written despite auth_token missing — guard failed")
	}
}

// TestWriteBackup_RefusesEmptyAuthToken — a present-but-empty
// auth_token is broken same as missing. Twitter's server won't
// accept it; persisting it would poison the shared file.
func TestWriteBackup_RefusesEmptyAuthToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twitter_cookies.json")

	broken := []Cookie{
		{Name: "auth_token", Value: "", Domain: ".x.com"}, // empty!
		{Name: "ct0", Value: "csrf_xyz", Domain: ".x.com"},
	}
	if err := WriteBackup(path, broken, time.Now()); err == nil {
		t.Fatal("expected refusal to write with empty auth_token")
	}
}

// TestWriteBackup_FiltersToTwitterDomain — cookies from unrelated
// domains that snuck into the browser context (e.g., from an
// off-Twitter redirect) don't get persisted.
func TestWriteBackup_FiltersToTwitterDomain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twitter_cookies.json")

	mixed := []Cookie{
		{Name: "auth_token", Value: "abc", Domain: ".x.com"},
		{Name: "google_thing", Value: "xyz", Domain: ".google.com"},
		{Name: "ct0", Value: "csrf", Domain: ".x.com"},
	}
	if err := WriteBackup(path, mixed, time.Now()); err != nil {
		t.Fatalf("WriteBackup: %v", err)
	}
	got, _, err := ReadBackup(path)
	if err != nil {
		t.Fatalf("ReadBackup: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("filtered cookie count = %d, want 2 (x.com only)", len(got))
	}
	for _, c := range got {
		if c.Domain == ".google.com" {
			t.Errorf("google.com cookie leaked through domain filter")
		}
	}
}

func TestWriteBackup_RejectsSubstringLookalikeDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twitter_cookies.json")
	cookies := []Cookie{
		{Name: "auth_token", Value: "attacker", Domain: ".notx.com"},
		{Name: "ct0", Value: "csrf", Domain: ".reallytwitter.com"},
	}
	if err := WriteBackup(path, cookies, time.Now()); err == nil {
		t.Fatal("lookalike domains were accepted")
	}
}

// TestReadBackup_RejectsMissingAuthToken — symmetric with the write
// guard. A corrupted file (missing auth_token) should NOT be trusted.
func TestReadBackup_RejectsMissingAuthToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twitter_cookies.json")

	// Hand-write a file without auth_token (bypass WriteBackup guard).
	envelope := CookieBackup{
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Cookies: []Cookie{
			{Name: "ct0", Value: "csrf", Domain: ".x.com"},
		},
	}
	data, _ := json.Marshal(envelope)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, _, err := ReadBackup(path)
	if err == nil {
		t.Fatal("expected ReadBackup to reject file without auth_token")
	}
}

// TestWriteBackup_AtomicNoPartialReads — concurrent writers +
// readers never produce a torn JSON. Uses os.Rename which is
// atomic on the same filesystem; the tmp file is renamed into place
// in one syscall.
func TestWriteBackup_AtomicNoPartialReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twitter_cookies.json")

	// Seed a valid file so readers have something to read from.
	if err := WriteBackup(path, validCookies(), time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Spin up N writers rapidly overwriting the file + M readers
	// hammering ReadBackup. If any read got a partial file, ReadBackup
	// would return a JSON parse error.
	const writers = 5
	const readers = 20
	const iterations = 50

	var wg sync.WaitGroup
	stopCh := make(chan struct{})

	// Writers — each rotates the ct0 value on every write so
	// Fingerprint changes and something actually gets serialized each
	// time (write path exercised).
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				select {
				case <-stopCh:
					return
				default:
				}
				c := validCookies()
				c[1].Value = "csrf_" + string(rune('a'+id)) + "_" + string(rune('0'+i%10))
				_ = WriteBackup(path, c, time.Now())
			}
		}(w)
	}

	// Readers — every read must succeed (no torn JSON parse errors).
	readErrs := make(chan error, readers*iterations)
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				select {
				case <-stopCh:
					return
				default:
				}
				if _, _, err := ReadBackup(path); err != nil {
					readErrs <- err
				}
			}
		}()
	}

	wg.Wait()
	close(readErrs)
	for err := range readErrs {
		t.Errorf("torn read (atomic-rename violation): %v", err)
	}
}

// TestBackupFileMtime_ReflectsRewrite — the mtime coordination
// signal actually works: rewriting the file advances mtime, so
// other instances' mtime check will trigger reload.
func TestBackupFileMtime_ReflectsRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twitter_cookies.json")

	if err := WriteBackup(path, validCookies(), time.Now()); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	t1, err := BackupFileMtime(path)
	if err != nil {
		t.Fatalf("mtime: %v", err)
	}

	// Delay ensures the second write lands in a distinguishable
	// timestamp bucket even on filesystems with second-granularity
	// mtime (most modern ones are ns-granular but be safe).
	time.Sleep(1100 * time.Millisecond)

	c2 := validCookies()
	c2[1].Value = "csrf_rotated"
	if err := WriteBackup(path, c2, time.Now()); err != nil {
		t.Fatalf("second write: %v", err)
	}
	t2, err := BackupFileMtime(path)
	if err != nil {
		t.Fatalf("mtime after rewrite: %v", err)
	}
	if !t2.After(t1) {
		t.Errorf("mtime did not advance after atomic rewrite: t1=%v t2=%v", t1, t2)
	}
}

// TestBackupFileMtime_MissingFile — reader gets a clean error when
// no backup exists; caller uses this as "no file to reload from,
// keep whatever we already had loaded."
func TestBackupFileMtime_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does_not_exist.json")
	if _, err := BackupFileMtime(path); err == nil {
		t.Fatal("expected error for missing file")
	}
}
