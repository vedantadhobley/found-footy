// fleet_test.go — tests for the per-event Firefox fleet (#160).
//
// Two tiers:
//   - Pure-function tests (InstanceName/InstanceAddr) run always. They pin
//     the registry-free contract: the name/address are a deterministic
//     function of the event ID, so the monitor (provision at count=1) and
//     the EventWorkflow (derive address at count=3) compute the same value.
//   - Socket-backed tests (New + find/count/Release wiring) run only with a
//     real Docker daemon and are skipped under -short. They exercise the
//     Docker API plumbing — not-found → success, label counting, idempotent
//     release-on-missing — WITHOUT spinning a real Firefox container. The
//     full Provision→healthy→Release round-trip stays a live-verification
//     step (Firefox startup is ~30s and flaky; it does not belong in unit CI).
package firefoxfleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/config"
)

// fixed event IDs so the determinism assertions don't depend on randomness.
var (
	evA = uuid.MustParse("11111111-2222-3333-4444-555555555555")
	evB = uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
)

// TestInstanceName_Deterministic pins the no-registry contract: the name is
// a stable, pure function of the event ID (same in → same out, different in
// → different out) and carries the human-scannable ff-firefox-ev- prefix +
// the 8-char event-ID head.
func TestInstanceName_Deterministic(t *testing.T) {
	got1 := InstanceName(evA)
	got2 := InstanceName(evA)
	if got1 != got2 {
		t.Fatalf("InstanceName not deterministic: %q != %q", got1, got2)
	}
	if !strings.HasPrefix(got1, "ff-firefox-ev-") {
		t.Errorf("InstanceName %q missing ff-firefox-ev- prefix", got1)
	}
	// 8-hex head of the event ID, appended to the prefix.
	wantHead := evA.String()[:8]
	if !strings.HasSuffix(got1, wantHead) {
		t.Errorf("InstanceName %q should end in event-ID head %q", got1, wantHead)
	}
	if same := InstanceName(evB); same == got1 {
		t.Errorf("distinct events produced identical names: %q", same)
	}
}

// TestInstanceAddr pins the address shape the worker dials over the shared
// network (container-name DNS + fixed :8888), derivable without provisioning.
func TestInstanceAddr(t *testing.T) {
	addr := InstanceAddr(evA)
	wantName := InstanceName(evA)
	want := "http://" + wantName + ":8888"
	if addr != want {
		t.Errorf("InstanceAddr = %q, want %q", addr, want)
	}
	// Same purity guarantee as the name.
	if InstanceAddr(evA) != InstanceAddr(evA) {
		t.Error("InstanceAddr not deterministic")
	}
}

// testFleet builds a Fleet against the ambient Docker daemon, skipping when
// unavailable (no socket / -short). Uses a dedicated label value so its
// counting/listing can never collide with a real fleet member.
func testFleet(t *testing.T) *Fleet {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping socket-backed fleet test in -short mode")
	}
	f, err := New(config.FirefoxFleetConfig{
		Image:            "found-footy-dev-twitter:latest",
		Network:          "found-footy-dev_found-footy-dev",
		CookieHostPath:   "/home/vedanta/.config/found-footy/twitter_cookies.json",
		InstanceMemLimit: 2 << 30,
		MaxInstances:     16,
		HealthTimeout:    45 * time.Second,
	})
	if err != nil {
		t.Skipf("no docker daemon available: %v", err)
	}
	// Probe the daemon; skip (don't fail) when the socket isn't mounted so
	// the suite stays green on hosts without Docker.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.count(ctx); err != nil {
		_ = f.Close()
		t.Skipf("docker daemon not reachable: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestFleet_FindMissing verifies the not-found path is success, not error —
// the invariant every idempotent lifecycle op leans on.
func TestFleet_FindMissing(t *testing.T) {
	f := testFleet(t)
	ctx := context.Background()
	_, ok, err := f.find(ctx, InstanceName(uuid.New()))
	if err != nil {
		t.Fatalf("find on missing container errored: %v", err)
	}
	if ok {
		t.Error("find reported a never-provisioned container as existing")
	}
}

// TestFleet_ReleaseIdempotent verifies releasing a never-provisioned event
// is a no-op success — the happy-path, VAR cancel-cleanup, and monitor-decay
// release paths all rely on double-release being harmless.
func TestFleet_ReleaseIdempotent(t *testing.T) {
	f := testFleet(t)
	ctx := context.Background()
	ev := uuid.New()
	if err := f.Release(ctx, ev); err != nil {
		t.Fatalf("Release on missing container should be nil, got %v", err)
	}
	// Second release — still nil.
	if err := f.Release(ctx, ev); err != nil {
		t.Fatalf("double Release should be nil, got %v", err)
	}
}

// TestFleet_CountNonNegative sanity-checks the label-filtered count wiring
// (the cap's source of truth) returns a usable number against a real daemon.
func TestFleet_CountNonNegative(t *testing.T) {
	f := testFleet(t)
	n, err := f.count(context.Background())
	if err != nil {
		t.Fatalf("count errored: %v", err)
	}
	if n < 0 {
		t.Errorf("count returned negative: %d", n)
	}
}
