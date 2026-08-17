// scripts/smoke_fleet/main.go — dev-only live smoke for the #160 per-event
// Firefox fleet.
//
// Proves a real per-event Firefox container comes up on the dev network and
// serves: constructs a firefoxfleet.Fleet, provisions ONE instance for a
// synthetic event ID, waits for the twitter service inside it to reach
// StateHealthy (polling /status so a stuck 'failed'/'unauthenticated' fails
// fast instead of waiting the full timeout), optionally fires one /search,
// then releases the instance. Release is deferred so a failed run still
// cleans up after itself.
//
// This is the check deliberately kept OUT of unit CI — Firefox startup is
// ~30-60s and flaky, which is fine for a manual smoke but poison for CI. It
// does NOT flip FIREFOXFLEET_ENABLED and does NOT touch the running worker;
// it drives the Docker API directly, exactly as the worker would once the
// flag is on.
//
// Run — the throwaway container needs BOTH the docker socket (to provision)
// AND the dev network (to reach the instance by container-name DNS):
//
//	docker run --rm \
//	  -v /home/vedanta/workspace/dev/found-footy:/src \
//	  -v /var/run/docker.sock:/var/run/docker.sock \
//	  --network=found-footy-dev_found-footy-dev \
//	  -w /src golang:1.25-bookworm go run ./scripts/smoke_fleet
//
// Opt into the live search step (fires real activity on the shared Twitter
// account — off by default to avoid concurrent-session pressure during a
// match) with SMOKE_FLEET_SEARCH=1.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/firefoxfleet"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ SMOKE FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n✅ SMOKE PASSED — instance provisioned, reached healthy, released cleanly")
}

func run() error {
	// Generous overall bound: create + Firefox warmup + optional search.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Fleet config from env — FIREFOXFLEET_* overrides + the same envDefaults
	// the worker uses, so this smoke provisions with production-identical spec.
	var cfg config.FirefoxFleetConfig
	if err := env.Parse(&cfg); err != nil {
		return fmt.Errorf("parse fleet config: %w", err)
	}
	fmt.Printf("config:\n  image=%s\n  network=%s\n  cookie=%s\n  cap=%d mem=%dMiB\n",
		cfg.Image, cfg.Network, cfg.CookieHostPath, cfg.MaxInstances, cfg.InstanceMemLimit>>20)

	fleet, err := firefoxfleet.New(cfg)
	if err != nil {
		return fmt.Errorf("fleet.New (is /var/run/docker.sock mounted?): %w", err)
	}
	defer func() { _ = fleet.Close() }()

	// Synthetic event — a random UUID so this never collides with a real
	// fleet member during a live match.
	eventID := uuid.New()
	name := firefoxfleet.InstanceName(cfg.Network, eventID)
	addr := firefoxfleet.InstanceAddr(eventID)
	fmt.Printf("\nsynthetic event=%s\n  name=%s\n  addr=%s\n", eventID, name, addr)

	// Always release — even if provision/warmup fails partway. Uses a fresh
	// context so cleanup still runs if the main ctx has expired.
	defer func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rcancel()
		fmt.Printf("\n[release] stopping + removing %s ...\n", name)
		if err := fleet.Release(rctx, eventID); err != nil {
			fmt.Fprintf(os.Stderr, "[release] WARN: %v\n", err)
			return
		}
		fmt.Println("[release] done")
	}()

	// 1. Provision — create+start only, must return fast (~1-2s). A slow
	//    Provision would mean it's blocking on health, which would stall the
	//    real 30s poll cycle.
	fmt.Println("\n[provision] create+start (should return in ~1-2s)...")
	start := time.Now()
	if _, err := fleet.Provision(ctx, eventID); err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	provDur := time.Since(start)
	fmt.Printf("[provision] started in %s — warming in background\n", provDur.Round(time.Millisecond))
	if provDur > 10*time.Second {
		fmt.Printf("[provision] ⚠ took >10s — expected create+start only, not a blocking health wait\n")
	}

	// 2. Warmup — poll /status until healthy. This is the window that, in the
	//    real system, hides behind debounce (count 1→3 ≈ 90s).
	fmt.Println("\n[warmup] polling /status until state=healthy ...")
	if err := waitHealthy(ctx, addr, 2*time.Minute); err != nil {
		return err
	}
	fmt.Printf("[warmup] healthy after %s total (provision → serving)\n", time.Since(start).Round(time.Second))

	// 3. Optional live search — opt-in, since it puts real load on the shared
	//    account. Soft: a failure here is a cookie/auth signal, not a fleet
	//    defect, so it warns rather than fails the smoke.
	if os.Getenv("SMOKE_FLEET_SEARCH") == "1" {
		fmt.Println("\n[search] SMOKE_FLEET_SEARCH=1 — firing one live search...")
		if err := trySearch(ctx, addr); err != nil {
			fmt.Printf("[search] ⚠ WARN (non-fatal): %v\n", err)
		}
	} else {
		fmt.Println("\n[search] skipped (set SMOKE_FLEET_SEARCH=1 to fire one live search)")
	}

	return nil
}

// statusResp is the subset of the twitter service's /status we act on.
type statusResp struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// waitHealthy polls <addr>/status until state==healthy, failing fast on the
// terminal states ('failed', 'unauthenticated') rather than burning the full
// timeout on a container that will never recover on its own.
func waitHealthy(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	hc := &http.Client{Timeout: 5 * time.Second}
	last := ""
	report := func(line string) {
		if line != last {
			fmt.Printf("  %s\n", line)
			last = line
		}
	}
	for {
		st, err := fetchStatus(ctx, hc, addr)
		if err != nil {
			// Container not listening yet (still booting) — normal early on.
			report("state=connecting (service not listening yet)")
		} else {
			report(fmt.Sprintf("state=%s%s", st.State, reasonSuffix(st.Reason)))
			switch st.State {
			case "healthy":
				return nil
			case "failed":
				return fmt.Errorf("instance reached terminal state 'failed': %s", st.Reason)
			case "unauthenticated":
				return fmt.Errorf("instance is 'unauthenticated' — the shared cookies need re-auth via the VNC container: %s", st.Reason)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not healthy within %s (last state=%q)", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func fetchStatus(ctx context.Context, hc *http.Client, addr string) (*statusResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out statusResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func reasonSuffix(r string) string {
	if r == "" {
		return ""
	}
	return " (" + r + ")"
}

// trySearch fires one POST /search at the instance — raw HTTP hitting the
// same endpoint the #160 addr path targets. Permissive query + wide age
// window so a healthy session returns something.
func trySearch(ctx context.Context, addr string) error {
	body, _ := json.Marshal(map[string]any{
		"query":           "goal",
		"max_age_minutes": 10080, // 7 days — recency shouldn't zero the result
	})
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, addr+"/search", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("search %d: %s", resp.StatusCode, raw)
	}
	var sr struct {
		Count      int    `json:"count"`
		StopReason string `json:"stop_reason"`
	}
	_ = json.Unmarshal(raw, &sr)
	fmt.Printf("[search] ok — count=%d stop_reason=%q\n", sr.Count, sr.StopReason)
	return nil
}
