// Command twitter is the Firefox+Playwright-Go browser-automation
// service that scrapes Twitter for goal video links. See
// docs/rebuild/proposals/twitter-port.md for phase T design + PoC gate.
//
// T/a scope: skeleton service that launches Playwright + Firefox in
// the persistent context, applies stealth patches, exposes /health +
// /status. Search + auth endpoints land in T/b + T/c.
//
// Runtime PoC verification: start the container, hit /health. If the
// service reports StateUnauthenticated cleanly (browser launched,
// cookies not present) → Playwright-Go + Firefox in Docker works.
// If startup logs an error before /health responds → the fallback
// path (Selenium Go bindings + geckodriver) becomes the T/a resolution.
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/vedantadhobley/found-footy/internal/twitter"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	_ = gitSHA
	_ = builtAt

	// Env-driven config with sensible defaults for the T/a PoC.
	profileDir := envOrDefault("TWITTER_PROFILE_DIR", "/data/firefox-profile")
	cookieFile := envOrDefault("TWITTER_COOKIE_FILE", "/config/twitter_cookies.json")
	listenAddr := envOrDefault("TWITTER_SERVICE_ADDR", ":8888")
	headless := envOrDefault("TWITTER_HEADLESS", "true") == "true"

	// Launch browser first — if this fails the service can't do
	// anything useful, exit non-zero so the orchestrator restarts us.
	browser, err := twitter.NewBrowser(twitter.NewBrowserOptions{
		ProfileDir: profileDir,
		Headless:   headless,
	})
	if err != nil {
		printJSON("startup_error", map[string]string{
			"stage": "browser_launch",
			"err":   err.Error(),
		})
		os.Exit(1)
	}
	defer func() { _ = browser.Close() }()

	svc := twitter.NewService(browser)

	// Cookie load + session verify — best-effort; missing cookies
	// means the service comes up unauthenticated (VNC re-auth path
	// kicks in for T/b's manual-login flow).
	go func() {
		if _, err := browser.LoadCookies(cookieFile); err != nil {
			svc.SetState(twitter.StateUnauthenticated, "no cookies: "+err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := browser.VerifySession(ctx, 20*time.Second); err != nil {
			svc.SetState(twitter.StateUnauthenticated, "verify: "+err.Error())
			return
		}
		svc.SetState(twitter.StateHealthy, "cookies loaded, session verified")
	}()

	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	printJSON("service_starting", map[string]string{
		"listen":      listenAddr,
		"profile_dir": profileDir,
		"cookie_file": cookieFile,
		"headless":    envOrDefault("TWITTER_HEADLESS", "true"),
	})

	srv := &http.Server{Addr: listenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		printJSON("server_error", map[string]string{"err": err.Error()})
		os.Exit(1)
	}
}

func envOrDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// printJSON logs a JSON-encoded event to stdout. Minimal replacement
// for the internal logging system since T/a hasn't wired bootstrap
// yet — one-off startup diagnostics only.
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
