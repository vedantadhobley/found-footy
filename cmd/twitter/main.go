// Command twitter is the Firefox+Playwright-Go browser-automation
// service that scrapes Twitter for goal video links. It owns browser launch,
// stealth patches, authentication/cookie lifecycle, status endpoints, and the
// scrolling search surface. See docs/twitter-service.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/vedantadhobley/found-footy/internal/twitter"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11.
var (
	gitSHA           = "dev"
	builtAt          = "unknown"
	errBrowserExited = errors.New("twitter browser process exited")
)

// idleCPUFirefoxPrefs — Firefox preferences that reduce idle CPU
// consumption + speed up cold start. Applied to the persistent-
// context launch so every subsequent page inherits them.
//
// The value types matter — Playwright expects native Go types matching
// the pref's Firefox-side type (int / bool / string). Passing the
// wrong type surfaces as a silent no-op with no error, which is why
// this map is documented in-line rather than derived from a config
// surface.
var idleCPUFirefoxPrefs = map[string]any{
	// ── Idle-CPU savings ────────────────────────────────────
	// Video autoplay — 5 = "blocked by default". Combined with the
	// filter:videos search results (which contain video elements),
	// this stops thousands of tweet videos from autoplaying in the
	// background feed and burning CPU on hidden pages.
	"media.autoplay.default": 5,
	// GIF animation — completely disabled. Static frame only. Tweet
	// feeds have many GIFs; each one is a background CPU drain.
	"image.animation_mode": "none",
	// Suspend video in backgrounded/hidden tabs — releases decoded-frame
	// buffers instead of letting off-screen tweet videos keep decoding.
	// The third of Python's 2026-06-30 media-suppression prefs (archive
	// docs/todo.md "Firefox idle-CPU bleed"); the Go rewrite had ported
	// only the first two, which is a prime suspect for the dev browser
	// ballooning to ~41 GB RSS over 13 days while prod (all three) held
	// at 2-3 GB.
	"media.suspend-bkgnd-video.enabled": true,
	// Tab switching / notification animations — pure visual, no
	// scrape value.
	"browser.tabs.animate":                  false,
	"browser.download.animateNotifications": false,
	"browser.fullscreen.animate":            false,
	"toolkit.cosmeticAnimations.enabled":    false,
	// Hardware-accelerated H.264 decoding — we're not playing videos,
	// only extracting metadata. Skip the acceleration overhead.
	"media.webrtc.hw.h264.enabled": false,

	// ── Cold-start speedup — kills bandwidth-heavy Firefox startup work ──
	// Safe Browsing DB downloads are ~200 MB from Google on first
	// launch. With Python-shape ephemeral profiles, every container
	// restart would re-download unless we disable. We don't need
	// malware/phishing protection for a scraping browser.
	"browser.safebrowsing.malware.enabled":          false,
	"browser.safebrowsing.phishing.enabled":         false,
	"browser.safebrowsing.downloads.enabled":        false,
	"browser.safebrowsing.downloads.remote.enabled": false,
	// Telemetry / experiments / studies — nothing we need, all costs
	// bandwidth + CPU on background pings.
	"toolkit.telemetry.enabled":                  false,
	"toolkit.telemetry.unified":                  false,
	"toolkit.telemetry.archive.enabled":          false,
	"datareporting.healthreport.uploadEnabled":   false,
	"datareporting.policy.dataSubmissionEnabled": false,
	"app.shield.optoutstudies.enabled":           false,
	// Disk cache — persistent-context stores it, we regenerate it on
	// every load anyway. Skip the write overhead.
	"browser.cache.disk.enable": false,
	// History tracking — we don't navigate back, don't need places.sqlite
	// updates on every page load.
	"places.history.enabled": false,

	// ── Memory-cache bounds (leak fix, 2026-08-06) ──────────────
	// Disk cache is OFF (above), so EVERY cache falls back to RAM. Without
	// explicit caps, Firefox smart-sizes them against the RAM it detects —
	// the container's full 125 GB before a mem_limit exists — so the
	// network + decoded-image caches grow unbounded across a long-lived
	// scraping session. Measured 2026-08-06: a 7-hour dev instance reached
	// 3.8 GB RSS, 95 % anonymous private-dirty heap (i.e. these caches),
	// vs a ~400 MB fresh floor. Bound them explicitly. (The container
	// mem_limit is the hard backstop, and #160's short-lived per-event
	// instances are the structural fix — they die before accumulating —
	// but a bounded long-lived instance is correct regardless.)
	//
	// Values are generous for a scraper that re-fetches distinct search
	// pages: it gains little from a large cache, so capping costs ~nothing.
	"browser.cache.memory.capacity":       51200,  // network memory cache: 50 MB (KB)
	"image.mem.surface_cache.max_size_kb": 102400, // decoded-image cache: 100 MB
	"media.cache_size":                    32768,  // media cache: 32 MB (autoplay is blocked, so tiny)
	// bfcache keeps whole rendered pages in memory for back/forward. We
	// open→scrape→close each search page and never navigate back, so this
	// is pure retained-page bloat. Disable it.
	"browser.sessionhistory.max_total_viewers": 0,
}

func main() {
	// Env-driven config with sensible defaults.
	cookieFile := envOrDefault("TWITTER_COOKIE_FILE", "/config/twitter_cookies.json")
	listenAddr := envOrDefault("TWITTER_SERVICE_ADDR", ":8888")
	headless := envOrDefault("TWITTER_HEADLESS", "true") == "true"

	// Firefox profile lives in the container writable layer for headless
	// (matches Python-shape — each container gets its own private
	// /data/firefox-profile/ via Docker's copy-on-write, no shared
	// volume, no cross-instance SQLite locking). VNC container has its
	// own dedicated `twitter-vnc-profile` volume for operator session
	// persistence — that's a different profile from headless. See
	// decisions.md 2026-07-23 (ephemeral vs subdirs) for the rationale.
	profileDir := envOrDefault("TWITTER_PROFILE_DIR", "/data/firefox-profile")

	// Launch browser first — if this fails the service can't do
	// anything useful, exit non-zero so the orchestrator restarts us.
	browser, err := twitter.NewBrowser(twitter.NewBrowserOptions{
		ProfileDir:       profileDir,
		Headless:         headless,
		FirefoxUserPrefs: idleCPUFirefoxPrefs,
	})
	if err != nil {
		printJSON("startup_error", map[string]string{
			"stage": "browser_launch",
			"err":   err.Error(),
		})
		os.Exit(1)
	}
	defer func() { _ = browser.Close() }()

	svc := twitter.NewService(browser, twitter.ServiceOptions{
		CookieFile: cookieFile,
		Build: twitter.BuildInfo{
			GitSHA:   gitSHA,
			BuiltAt:  builtAt,
			ImageTag: os.Getenv("IMAGE_TAG"),
		},
		// AuditEmit — structured auth-expiry and browser-failure transitions
		// for Grafana/Loki alerting.
		AuditEmit: emitAudit,
	})

	// TWITTER_VNC_MODE=true only in the twitter-vnc container (set by
	// docker-compose). Drives the "open a browser tab so the operator
	// sees Twitter" behavior below — no effect on headless.
	vncMode := os.Getenv("TWITTER_VNC_MODE") == "true"

	// Kick off first auth check. EnsureAuthenticated handles the full
	// sequence: mtime check → reload from shared file if newer →
	// verify. Best-effort in the background — /health reports the
	// outcome so the orchestrator sees it. Missing cookie
	// file → StateUnauthenticated, VNC operator needs to log in.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := svc.EnsureAuthenticated(ctx); err != nil {
			// State is already set by EnsureAuthenticated (unauth /
			// failed / etc.). Log for visibility; process stays up so
			// the VNC re-auth path can recover it without a restart.
			printJSON("initial_auth_failed", map[string]string{"err": err.Error()})
		}

		// VNC nicety: keep a Twitter tab open in the visible browser
		// so the operator lands somewhere useful. If authed → x.com
		// serves /home. If not → Twitter redirects to /login. Either
		// way the operator sees Twitter, not Firefox's default new-
		// tab page. Best-effort — a nav failure here doesn't affect
		// service health (headless containers skip this block).
		if vncMode {
			navCtx, navCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer navCancel()
			// Deliberately not closing the returned page — leaving
			// it open is the point. Playwright will close it when
			// the browser context tears down at process exit.
			if _, err := browser.Navigate(navCtx, "https://x.com/", 20*time.Second); err != nil {
				printJSON("vnc_landing_nav_failed", map[string]string{"err": err.Error()})
			}
		}
	}()

	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	printJSON("service_starting", map[string]string{
		"listen":      listenAddr,
		"profile_dir": profileDir,
		"cookie_file": cookieFile,
		"headless":    envOrDefault("TWITTER_HEADLESS", "true"),
		"hostname":    os.Getenv("HOSTNAME"),
		"git_sha":     gitSHA,
		"built_at":    builtAt,
		"image_tag":   os.Getenv("IMAGE_TAG"),
	})

	srv := &http.Server{Addr: listenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.ListenAndServe() }()
	if err := waitForBrowserOrServer(browser.Done(), serverDone, svc.MarkBrowserExited); err != nil {
		action := "server_error"
		if errors.Is(err, errBrowserExited) {
			action = "browser_exited"
		}
		printJSON(action, map[string]string{"err": err.Error()})
		os.Exit(1)
	}
}

// waitForBrowserOrServer makes Firefox a critical child of Go PID 1. The
// service state changes before the caller exits non-zero, allowing health and
// audit observers to see the cause while Docker restarts the container unit.
func waitForBrowserOrServer(
	browserDone <-chan struct{},
	serverDone <-chan error,
	markBrowserFailed func(),
) error {
	select {
	case <-browserDone:
		if markBrowserFailed != nil {
			markBrowserFailed()
		}
		return errBrowserExited
	case err := <-serverDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func envOrDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// printJSON logs a JSON-encoded event to stdout for legacy startup /
// error diagnostics. String-valued only; keeps the parser simple.
func printJSON(action string, fields map[string]string) {
	fields["action"] = action
	fields["ts"] = time.Now().UTC().Format(time.RFC3339)
	buf := []byte("{")
	first := true
	for k, v := range fields {
		if !first {
			buf = append(buf, ',')
		}
		first = false
		buf = append(buf, '"')
		buf = append(buf, k...)
		buf = append(buf, `":"`...)
		for _, r := range v {
			if r == '"' || r == '\\' {
				buf = append(buf, '\\')
			}
			buf = append(buf, byte(r))
		}
		buf = append(buf, '"')
	}
	buf = append(buf, "}\n"...)
	_, _ = os.Stdout.Write(buf)
}

// emitAudit is the Service's audit emitter — richer than printJSON
// because event fields can carry typed values (not just strings). Used
// for structured events consumed by Grafana/Loki alerts. Emits one
// canonical-ordered JSON line per event.
//
// Field key `action` is the primary discriminator (`twitter.auth_expired` or
// `twitter.browser_failed`). Container hostname gets folded in automatically
// so alerts can identify which replica hit the transition.
func emitAudit(action string, fields map[string]any) {
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["action"] = action
	fields["ts"] = time.Now().UTC().Format(time.RFC3339)
	if host := os.Getenv("HOSTNAME"); host != "" && fields["container"] == nil {
		fields["container"] = host
	}
	// Canonical key order for grep-friendly output. json.Marshal on a
	// map has nondeterministic order — sort keys explicitly.
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := []byte("{")
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, _ := json.Marshal(k)
		buf = append(buf, kb...)
		buf = append(buf, ':')
		vb, err := json.Marshal(fields[k])
		if err != nil {
			// Fall back to string form on unusual value types.
			vb, _ = json.Marshal(err.Error())
		}
		buf = append(buf, vb...)
	}
	buf = append(buf, "}\n"...)
	_, _ = os.Stdout.Write(buf)
}
