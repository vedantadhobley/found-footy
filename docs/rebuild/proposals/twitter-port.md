# Twitter service port — design proposal (Phase T)

**Status:** design-first draft. Do not implement anything from this
doc until it's reviewed + signed off.

**Revision log:**
- 2026-07-21 (T/b partially shipped) — cookie backup file management
  + auth flow (mtime → warm-path → verify) + service state machine +
  `/authenticate` + `/auth/verify` endpoints all shipped and unit-
  tested (26 tests). See [`../roadmap.md`](../roadmap.md) T/b.1 +
  T/b.2 entries for what's in the tree. Remaining T/b work: VNC
  container image, docker-compose splits, instance-scoped profile
  dir, idle-CPU Firefox prefs, `BackupCookies` invocation from
  search's success path (lands with T/c). Design settled during
  implementation: VNC container is opt-in (profiles: ["vnc"]), not
  always-running, per decisions.md 2026-07-21 — `/authenticate`
  response surfaces `TWITTER_VNC_URL` + `TWITTER_VNC_START_CMD` env
  vars so operator gets copy-paste instructions.
- 2026-07-16 (first pass) — initial proposal. Phase T is sequenced
  right after O3, before O4, per [`discovery.md`](./discovery.md)
  Q3 sign-off — Twitter is the pipeline's most load-bearing external
  dependency and deserves its own dedicated design + review runway.
- 2026-07-17 (T/a PoC first attempt) — hit a driver-CDN wall on
  `playwright-community/playwright-go` v0.4700-v0.5700: all three
  mirror URLs 404 because Playwright upstream moved to
  `cdn.playwright.dev` and that fork stopped receiving CDN URL
  updates. Committed a note pivoting to Selenium fallback — the
  wrong call, as the next day's dig showed.
- 2026-07-18 (T/a PoC PASSED) — root cause was the module fork.
  The `playwright-community/playwright-go` GitHub repo redirects to
  `mxschmitt/playwright-go` (the original author's personal repo);
  releases v0.5900+ landed under the mxschmitt module path. v0.6100.0
  uses `cdn.playwright.dev` cleanly. Swapped the import to
  `github.com/mxschmitt/playwright-go@v0.6100.0`, matched to
  Playwright base image v1.61.1-noble, added a Dockerfile step to
  copy the pre-installed driver from `/root/.cache/ms-playwright-go`
  in the builder stage to `/home/pwuser/.cache/ms-playwright-go` in
  the runtime stage (playwright.Run() looks at `$HOME/.cache/...`
  and the base image switches user to pwuser). Standalone
  `docker run` produces the intended gate-pass output: `/health`
  returns 503 with `state=unauthenticated` and reason "no cookies"
  — Firefox launched, driver initialized, state machine
  transitioned, HTTP endpoints served. Selenium fallback commitment
  from 2026-07-17 rescinded; **T/a resolves as Playwright-Go
  v0.6100.0 (mxschmitt path) + Firefox as originally planned.**
- 2026-07-16 (second pass, this doc) — walkthrough with user corrected
  a misframing: **dual-mode auth is an OPERATIONAL pattern (VNC as
  login terminal, save cookies to shared disk, headless fleet loads),
  NOT an anti-detection workaround** as I originally implied. The
  operational split is preserved and formalized: VNC container runs
  raw Firefox only (no automation library, no scraping), headless
  fleet runs Playwright-Go for scraping only (never login). Browser
  library locked to Playwright-Go + Firefox with the T/a PoC gate as
  a fallback trigger to port Python's Selenium implementation if
  Firefox-in-Docker-with-Playwright-Go proves fragile.

**Cross-refs:**
- Plan intent — [`../../rebuild-plan.md`](../../rebuild-plan.md) §9 external adapters, §5 W3 Discovery
- Prior decisions — [`../../decisions.md`](../../decisions.md):
  - 2026-07-16 Downstream workflow spawn via Temporal-direct + register-on-flip
  - 2026-07-11 Fixture completion contract
- Upstream — [`./discovery.md`](./discovery.md) Q3 sign-off (stub in O3, port in dedicated T phase)
- Downstream — [`./video-dedup.md`](./video-dedup.md) V/a (Video pipeline consumes T's search output + calls T's download endpoint)
- Python reference:
  - `archive/twitter/` — service directory (app.py, session.py, scrape.py, config.py, README.md)
  - `archive/twitter/README.md` — end-user + integration doc, worth reading verbatim
- Existing Go client (S7 shipped 2026-07-06) — [`internal/infra/twitter/client.go`](../../../internal/infra/twitter/client.go) — HTTP wrapper only; talks to the current Python service. Adapter needs minor extensions (auth endpoints, download endpoint) once the Go service ships and honors the same API contract.

## Purpose

Port the Twitter scraping service from Python (`archive/twitter/`) to Go, preserving the behavior that works (dual-mode auth, cookie persistence, time-based scrolling, URL exclusion, OR-syntax search) while raising the code quality bar and fixing known weaknesses (rate limit handling, session recovery, error taxonomy, search-string tuning).

Twitter is the pipeline's most critical external dependency: no Twitter → no video candidates → no goals surfaced. It deserves its own dedicated port phase rather than being bundled into O3.

## What Python does (behavior reference)

Read `archive/twitter/README.md` for the end-user view. Summary of the behavior we need to preserve:

- **HTTP service on :8888** with endpoints: `/health`, `/search`, `/authenticate`, `/auth/verify`, `/auth/launch-browser`, `/download_video`, `/status`, `/`.
- **Dual-mode browser lifecycle:** Firefox launched WITHOUT Selenium for manual login (avoids Twitter's bot detection); once cookies are captured, Selenium takes over the same profile for scraping.
- **Persistent Firefox profile + cookie backup** at `/config/twitter_cookies.json`, host-side `~/.config/found-footy/twitter_cookies.json`, shared across all instances via bind mount.
- **VNC access** on `twitter-vnc` container for manual re-auth when cookies expire (headless instances share the re-authed cookies via the backup file).
- **Search:** POST `/search` with `{search_query, exclude_urls[], max_age_minutes}`. Returns ALL videos found within the time window, no limit.
- **Time-based scroll termination:** scroll through "Latest" results until a tweet is older than `max_age_minutes`, then stop.
- **URL exclusion:** Discovery passes previously-seen `exclude_urls`; the service skips those during scroll — supports the 10-attempt / 1-minute-apart pattern by not re-surfacing already-processed tweets.
- **OR-syntax search:** query supports `player (team1 OR team2 OR team_alias)` — team alias resolution happens upstream (in Discovery via Wikidata + LLM RAG).
- **Direct video download** via authenticated browser session — navigates to the tweet, extracts the CDN URL from network activity, downloads with cookies attached. Bypasses yt-dlp's 403 rate-limit issues by riding the authenticated session.
- **Instance registry + heartbeat:** the scaler tracks multiple twitter containers by ID + heartbeats them every 10s. Enables horizontal scaling of the headless fleet.
- **Deployment split:** one `twitter-vnc` container (Xvfb + x11vnc + websockify + Firefox) for auth + debugging; N `twitter-headless` containers for scale-out (no Xvfb, faster startup, no port conflicts).

## What Python does well — preserve

- **Dual-mode auth pattern.** Manual Firefox for login is the most reliable defense against Twitter's evolving bot detection. Should carry over regardless of automation library.
- **Cookie backup file shared across instances.** One re-auth event unlocks the whole fleet. Right move.
- **URL exclusion in the search API.** Lets Discovery drive dedup at the search level, not just at the download level. Preserves.
- **Time-based scroll termination.** Simpler than a fixed count + more useful when a match generates many videos or few. Preserves.
- **Instance registry pattern.** Scaler can scale up/down without static config. Preserves.
- **Direct-download-through-browser-session** to bypass yt-dlp rate limits. Non-obvious win, preserves.

## What Python does poorly — improve

- **Rate-limit handling is non-existent.** No detection of 429 responses, no exponential backoff, no explicit throttle. When Twitter rate-limits us, the service just returns errors and the next attempt hits the same wall. Redesign: detect rate-limit signals (429, "Are you a robot?" interstitials, empty result pages), track per-instance backoff state, expose it via `/status`, and let Discovery route to a non-throttled instance.
- **Session recovery is silent.** When Firefox crashes mid-search (real: happens under load), the service returns generic 500. Redesign: watchdog process; on browser crash, auto-relaunch + reload cookies + retry the search once before returning error.
- **Error taxonomy is thin.** 503 = not authenticated, 500 = everything else. Discovery can't distinguish rate-limit-back-off vs authentication-required vs transient-network. Redesign: HTTP status + structured error body with `error_class` enum (`auth_expired`, `rate_limited`, `no_results`, `browser_crashed`, `network_error`, `internal_error`) + `retry_after_seconds` where meaningful.
- **Search-string tuning has no rubric.** Discovery constructs `player (team1 OR team2)` from Wikidata + LLM aliases; the service just searches it as-is. Improvements to search-string design (more aliases, hashtag combinations, negative filters) live in Discovery's RAG, not here — but this proposal notes that improvements to the Discovery/RAG side directly affect T's yield.
- **No metrics.** Python service has logs; no per-instance counters for search rate / success rate / rate-limit encounters / auth-expiry events / avg tweets scanned per search / etc. Redesign: instrument the service the same way every other Go adapter is instrumented — Prometheus counters + histograms via the shared observability substrate.
- **Direct video download coupling.** Python's `/download_video` uses the authenticated browser session to extract CDN URL + download bytes in one round trip. Works but couples download timing to search timing on the same browser instance. Redesign: split into `/extract_cdn_url` (returns the CDN URL for a given tweet, cheap) + separate downloader that hits the CDN URL from any HTTP client with the shared session cookies attached. Enables parallel downloads from a search's results without serializing on the browser.

## Browser automation library — Playwright-Go + Firefox

**Locked (2026-07-16):** Playwright-Go with Firefox for the scraping fleet. Raw Firefox in the VNC container (no automation library at all — that container's job is manual login only, per the operational reframing above).

Reasoning:

- **Firefox** matches Python's proven-good choice against Twitter's detection surface (Chromium is more heavily targeted). Firefox-vs-Chromium is orthogonal to automation-library choice — we're keeping Firefox regardless of library.
- **Playwright-Go over Selenium Go bindings** for code quality:
  - Modern async API with typed responses, auto-waits, and built-in retries. Selenium's API leans on manual polling loops that make Go code noisy.
  - Active development, larger community, Microsoft-backed.
- **NOT the reasons — detection isn't the driver.** Grepping `session.py:123-141` for Python's Firefox config confirms it has **zero anti-detection setup** — no User-Agent spoofing, no `navigator.webdriver` patching, no stealth mode. The only Firefox preferences set are idle-CPU optimizations (autoplay off, animations off, background video suspended). Post-login scraping runs with `navigator.webdriver = true` and Twitter tolerates it. So the anti-detection edge Playwright-Go has by default (`navigator.webdriver = false`, juggler-patched Firefox) is a marginal improvement over Python's current status quo, not a decisive one. **The library choice is a code-quality win, not a detection win.** Dual-mode is preserved for operational reasons (VNC-as-login-terminal), not to hide from Twitter's login-time detection (which is what I originally wrote in the first pass — corrected here).
- **Chromium-only options rejected** (Rod, Chromedp, Playwright-Chromium): dropping Firefox increases detection risk against Twitter specifically. Not worth it for pure-Go convenience.

### Trade-offs we're accepting

- **Node runtime in the container** — Playwright-Go bundles a Node driver (~50MB overhead). Fine.
- **Firefox-in-Docker with Playwright-Go is less battle-tested than Selenium+geckodriver in Docker.** Known Playwright-Go GitHub issues around Firefox startup in containers. The T/a PoC gate is exactly this test — if it works, ship. If it doesn't, fall back.

### Fallback strategy

**If T/a's PoC gate fails** (Playwright-Go + Firefox in Docker proves unreliable against Twitter):
- Port Python's `session.py` directly using `tebeka/selenium` Go bindings + geckodriver + Firefox.
- Mechanical translation from Python — the Selenium API shape is nearly identical across languages.
- We lose the modern-API code-quality argument but preserve Firefox-for-detection.
- No redesign of T's other work — the fallback is contained to the browser wrapper module.

Fallback commitment: if the PoC gate fails, DON'T pivot to Chromium in some pure-Go option. Firefox stays. The pivot is Playwright-Go → Selenium Go bindings, that's it.

## Authentication + cookie lifecycle

Preserving Python's operational pattern (VNC-as-login-terminal, headless-fleet-as-scrapers, cookies shared via disk). Explicit container split:

### VNC container — login terminal only

- **Runs raw Firefox in Xvfb, exposed via x11vnc + websockify.** No Playwright, no automation library — the whole point is a real Firefox that Twitter's detection sees as an actual user.
- **On startup:** launch Firefox pointed at `x.com/login`. Idle until a human connects via VNC.
- **Login watcher:** a Go process monitors the Firefox profile's `cookies.sqlite` for the `auth_token` cookie appearing.
- **Cookie capture:** once `auth_token` is present + non-expired, extract cookies from `cookies.sqlite` → write to `/config/twitter_cookies.json` (JSON format, compatible with Python's existing format for migration).
- **No explicit signaling** — after cookie capture writes the shared file via atomic temp+rename, other instances discover the update naturally on their next `EnsureAuthenticated` call via a file-mtime check. (Updated 2026-07-21 twice: was NATS event → briefly pg NOTIFY → landed on filesystem mtime as the coordination medium. No pub/sub needed; the filesystem IS the coordination signal. Matches Python's own `auto_verify_loop` pattern at `session.py:739` which uses mtime to detect VNC login completion.)
- **Post-capture behavior:** container goes idle. Firefox stays running so a human can re-open VNC and re-auth on future expiry. Not a scraping instance.
- **HTTP endpoints:** just `/health` + `/status` (report last-capture timestamp, cookie expiry) — no `/search`, no `/download_video`. The scaler routes search traffic away from this container.

### Headless fleet — scraping only, never login

- **Runs Playwright-Go with Firefox.** Persistent context pointed at `/data/firefox-profile/` (per-container profile; NOT shared, because Playwright's persistent context isn't multi-writer-safe).
- **Cookie loading:** on startup, read `/config/twitter_cookies.json` and load cookies into the Playwright context. Probe `x.com/home` to verify the session is live. Success → serve traffic. Failure → mark `/health` 503, log for scaler visibility.
- **Cookie expiry detection:** each `EnsureAuthenticated` check against `x.com/home` returns login-redirect → mark this instance unhealthy, log loud (structured `auth_expired` event via observability, not any pub/sub). No explicit fleet notification — since all instances share the same session, they'll all discover the same failure on their own next check. Operator sees `auth_expired` events across the fleet in Loki, does VNC re-auth. (Updated 2026-07-21 — was NATS event → briefly pg NOTIFY → no signaling needed; all instances fail together because they share the same cookies for the same account.)
- **Cookie reload — no subscription needed:** each `EnsureAuthenticated` first stat's the cookie backup file; if mtime is newer than the last-loaded time, reload cookies from file BEFORE the warm-path or full-verify step. Same mechanism whether the file was updated by another headless instance's successful search (Twitter refresh tokens) or by VNC container writing after manual login. Filesystem is the coordination medium. (Updated 2026-07-21 — see above.)
- **NEVER attempts login itself.** If cookies are invalid, the correct behavior is to go unhealthy and wait for a human-driven VNC re-auth. Automating login in a headless container defeats the whole operational split (defensive Firefox for login, automation for scraping).

### Cookie format

JSON file at `/config/twitter_cookies.json` (host-side `~/.config/found-footy/twitter_cookies.json`), bind-mounted into every twitter container. Same top-level shape as Python's format (array of cookie objects) plus optional metadata fields for observability. See Q4 below for open question on the metadata schema.

## Search + scrape strategy

- **Request shape** (unchanged from Python API contract):
  ```json
  POST /search
  {
    "search_query": "Salah (Liverpool OR LFC OR Reds) goal",
    "exclude_urls": ["https://x.com/i/status/12345", "..."],
    "max_age_minutes": 5
  }
  ```
- **Scroll loop** (Go re-implementation of Python's DOM-walking logic in `scrape.py`):
  - Navigate to `https://x.com/search?q=<url_encoded_query>&f=live` (Latest tab).
  - Extract tweets via `data-testid="tweet"` selector.
  - For each tweet:
    - Extract the `/status/` href → tweet URL.
    - Skip if URL in `exclude_urls`.
    - Extract `time[datetime]` → parse age; if older than `max_age_minutes` → set "stop after this batch" flag.
    - Check for video element via multi-selector strategy (`video`, `[data-testid='videoPlayer']`, `[data-testid='videoComponent']`) — Python's `extract_video_duration` maps directly.
    - Skip if promoted (`Promoted` / `Ad` indicator).
    - Skip if truncated snowflake (< 18 digits) — Python's `MIN_SNOWFLAKE_LEN` guard for deleted/edge-case tweets.
    - Extract tweet text (`[data-testid='tweetText']`), truncate to 200 chars.
    - Extract username from URL path.
    - Append to results list.
  - Scroll down (page-down + await network idle) → repeat until stop-flag set or no new tweets after 3 consecutive scrolls.
- **Response shape** (unchanged):
  ```json
  {"status": "success", "videos": [{"tweet_url":"...","video_page_url":"...","tweet_text":"...","tweet_id":"...","duration_seconds":15.0,"discovered_at":"..."}], "count": 1}
  ```

## Rate limiting + error taxonomy

Structured error responses replacing Python's `503 = auth, 500 = everything`:

```json
{
  "status": "error",
  "error_class": "rate_limited",
  "message": "Twitter returned 429 on last request",
  "retry_after_seconds": 60,
  "instance_id": "found-footy-twitter-3"
}
```

Error classes:

| `error_class` | HTTP | When | Retry hint |
|---|---|---|---|
| `auth_expired` | 503 | Cookies expired / login required | Never auto-retry; user action required |
| `rate_limited` | 429 | Twitter 429 or "Are you a robot?" interstitial detected | `retry_after_seconds` (30-300 depending on signal) |
| `browser_crashed` | 502 | Playwright context lost, watchdog respawning | 5s |
| `no_results` | 200 | Zero videos found within window (not an error but distinct signal) | N/A — Discovery retries as next attempt |
| `network_error` | 502 | Playwright request failure / connection reset | 5s |
| `internal_error` | 500 | Unexpected exception; log + rethrow | Retry once with backoff |

Rate-limit detection heuristics:
- Direct 429 from twitter.com.
- Search results page contains "Something went wrong" / "Try again" text.
- Search returns 0 tweets when the query is known to have hits (delta vs expected? maybe not detectable, but empty-result-after-scroll is a signal).
- Continuous "Rate limit exceeded" API responses in network trace.

Instance-level backoff state exposed via `/status` — Discovery can route new searches to non-throttled instances.

## Deployment shape

Preserving Python's fleet pattern with Go-native process management:

- **`found-footy-{dev,prod}-twitter-vnc`** — one instance, Xvfb + x11vnc + websockify + Firefox + Go service. Exposes VNC on 4103/3203 (dev/prod). Used for manual re-auth + debugging.
- **`found-footy-{dev,prod}-twitter-N`** — headless instances, no Xvfb / VNC. Go service + Playwright launching Firefox headless. Scale count via docker compose replicas.
- **Instance registry:** each instance heartbeats to a shared registry (pg table `twitter_instances(id, url, healthy, busy, last_heartbeat, backoff_until)`) every 10s. Discovery reads this to route search requests.
- **Cookie backup shared** via bind-mount `/config/twitter_cookies.json` — all instances read the same file. Fleet reload happens naturally via mtime check on each `EnsureAuthenticated` (no pub/sub — filesystem is the coordination signal). Atomic writes via temp+rename prevent torn reads. Fingerprint-based dedupe skips writes when cookies haven't changed since last backup. (Updated 2026-07-21 — was NATS event → briefly pg NOTIFY → filesystem mtime; see decisions.md 2026-07-21.)

Improvements over Python:
- Instance registry moves from scaler-owned in-memory state to pg table. Observable via SQL, survives scaler restarts.
- Backoff state persistence — an instance that hit rate limit stays backed off across container restarts.

## HTTP API contract

Preserving Python's endpoint set, updating semantics:

| Endpoint | Method | Purpose | Change from Python |
|---|---|---|---|
| `/health` | GET | Health check — auth status + backoff state | Adds `backoff_until` field |
| `/status` | GET | Detailed status — instance id, busy flag, backoff, cookie expiry | Adds `cookie_expires_at`, `error_class_last` |
| `/search` | POST | Search for videos, return all within window | Response adds `error_class` + `retry_after_seconds` on error |
| `/authenticate` | POST | Force re-auth (kicks manual mode) | Same |
| `/auth/verify` | POST | Verify manual login succeeded → switch to Playwright | Same intent, Playwright not Selenium |
| `/auth/launch-browser` | POST | Relaunch manual Firefox | Same |
| `/extract_cdn_url` | POST | **NEW** — return CDN URL for a given tweet, no download | Splits Python's `/download_video` into extract + download |
| `/download_video` | POST | Download bytes to a path, using authenticated session cookies | Now delegates to `/extract_cdn_url` + HTTP client; browser only used for extract |

Existing S7 Go client at `internal/infra/twitter/client.go` needs additive changes to match — new methods for `ExtractCDNUrl` + auth endpoints. Existing `SearchRequest` / `SearchResponse` types stay compatible.

## Sequenced sub-commits

### T/a — Skeleton service + Playwright-Go PoC + baseline stealth

- `cmd/twitter/main.go` — HTTP server (net/http or chi router)
- `internal/twitter/service.go` — service wrapper (state machine: `starting`, `authenticated`, `unauthenticated`, `backoff`)
- `internal/twitter/browser.go` — Playwright-Go wrapper: launch Firefox in persistent context, load cookies, verify session
- `internal/twitter/stealth.go` — baseline anti-detection: stealth init script applied to the Playwright context on every session (spoofs `navigator.webdriver`, chrome-runtime, plugin arrays, WebGL vendor/renderer, ~15 other WebDriver telltales — see § Stealth improvements). Header rotation config (User-Agent pool, Accept-Language variants).
- Health + status endpoints only

PoC gate: launch Playwright + Firefox in a dev container, load a cookie fixture, verify session against `x.com/home` with stealth applied. If Firefox support in Playwright-Go proves fragile → fall back to Selenium Go bindings + geckodriver (stealth patches transfer since they're browser-level, not library-level).

~500 lines (grew from ~400 with stealth init script + header rotation config).

### T/b — Auth + cookie lifecycle

- Dual-mode Firefox launch (Playwright persistent context OR raw subprocess for manual mode — pending T/a resolution).
- Cookie backup file read/write (JSON, same shape as Python).
- Manual-login detection loop.
- VNC infrastructure (Xvfb + x11vnc + websockify) in `docker/twitter-vnc/Dockerfile`.
- Headless variant in `docker/twitter-headless/Dockerfile`.
- Endpoints: `/authenticate`, `/auth/verify`, `/auth/launch-browser`.

~600 lines (includes docker/ files).

### T/c — Search + scrape + behavior jitter + consecutive-already-seen early stop

- `internal/twitter/search.go` — scroll loop, DOM extraction (mirrors Python `scrape.py` helpers).
- Endpoint: `/search` — full contract with `exclude_urls`, `max_age_minutes`, structured response.
- **Four scroll-stop conditions** (Python has 3; we add the 4th):
  1. Tweet age > `max_age_minutes` → stop (preserved from Python).
  2. `scroll_count >= max_scrolls` (default 10) → stop (preserved from Python).
  3. Empty page after ≥1 scroll → stop (preserved from Python).
  4. **`consecutive_already_seen >= consecutive_stop_threshold` → stop (NEW).** Counter increments on each tweet whose URL is in `exclude_urls`, RESETS on any new-to-us tweet. Default threshold: **3 consecutive**. Env-tunable. Fixes Python's under-utilization of exclude_urls — Python only uses it to skip individual tweets, not to short-circuit scroll. Late-attempt searches (7-10 out of 10) walk through mostly-known tweets in Python; the early-stop cuts that waste. Counter-reset-on-new-tweet handles genuine sparse-new-content interleaving correctly.
- **Behavior jitter (baseline stealth #2 + #4):** random 0.5-3s pause between scroll actions; random ±20-40s jitter added to the "1 minute between attempts" interval that Discovery uses when calling `/search` repeatedly.
- Instrumentation: Prometheus counters + histograms via shared observability substrate. Track which stop condition fired per search (age/max-scrolls/empty/consecutive-seen) — feeds threshold tuning during T shakeout.
- Structured error responses with `error_class` taxonomy.

~800 lines including tests (grew from ~750 with the consecutive-already-seen counter + stop-condition telemetry).

### T/d — Rate-limit detection + backoff state

- `internal/twitter/backoff.go` — rate-limit detection (429 / interstitial / heuristics).
- Backoff state per instance, persisted via instance registry (pg table).
- `/status` endpoint exposes current backoff.
- Search endpoint refuses new requests during backoff → returns `error_class: rate_limited` with `retry_after_seconds`.

~400 lines.

### T/e — Instance registry + heartbeat

- pg table `twitter_instances` — schema in `schema.sql`.
- Heartbeat goroutine — updates `last_heartbeat` every 10s.
- Registry queries for healthy + non-backed-off instances.
- Discovery-side helper — round-robin router with backoff awareness (lands in Discovery, not here).

~300 lines.

### T/f — CDN URL extraction + download split

- `/extract_cdn_url` endpoint — navigates to tweet in Playwright, monitors network for video CDN URL, returns it.
- `/download_video` endpoint — either delegates to `/extract_cdn_url` + HTTP client with session cookies OR keeps browser-driven download (open question).
- Cookie export helper — pulls current session cookies for use by non-browser HTTP client.

~400 lines.

### T/g — Session recovery watchdog

- Browser crash detection (Playwright context dead / no response to ping).
- Auto-relaunch + cookie reload + one search retry.
- NATS emit `twitter.browser_recovered` for observability.

~300 lines.

### T/h — Adapter update (Go client)

- Update `internal/infra/twitter/client.go` for new endpoints (extract_cdn_url, structured errors, /status fields).
- Preserve backward compat during migration (Discovery uses updated adapter, Python service coexists until cutover).

~200 lines.

## What's decided going in

| Decision | Source |
|---|---|
| T is a dedicated phase sequenced right after O3, before O4. | discovery.md Q3 sign-off |
| Preserve Python's API contract (`/search`, `/health`, `/authenticate`, `/auth/verify`, `/download_video`) so existing S7 client works with minimal changes. | Minimize cutover risk |
| Dual-mode auth pattern preserved AS PRIMARY DESIGN, not fallback — reason is operational (VNC as login terminal, save cookies to disk, headless fleet loads), not anti-detection as originally miswritten. VNC container runs raw Firefox only, headless fleet runs Playwright-Go only. Container roles are formally split. | 2026-07-16 walkthrough |
| Browser library locked: **Playwright-Go + Firefox** for the headless scraping fleet. Reasons: modern async API + typed responses + auto-waits + built-in retries make cleaner Go code than Selenium's polling-loop shape. Firefox retained regardless of library because it has less Twitter-detection surface than Chromium. Chromium-only options rejected. | 2026-07-16 walkthrough |
| Fallback strategy on T/a PoC failure: port Python's `session.py` directly using `tebeka/selenium` Go bindings + geckodriver + Firefox. Mechanical translation. Firefox stays. No pivot to Chromium under any option. | 2026-07-16 walkthrough |
| Preserve Python's cookie backup file at `/config/twitter_cookies.json` shared via bind mount. Same shape as Python for migration compat. | Minimize cutover risk |
| Preserve Python's dual-container deployment (twitter-vnc + N twitter-headless). Scale N via docker compose replicas. | Proven pattern |
| Preserve Python's URL exclusion + time-based scroll termination + OR-syntax search — the search API contract from Discovery's perspective is unchanged. | Discovery integration compat |
| Split `/download_video` into `/extract_cdn_url` + downloader — enables parallel downloads without serializing on the browser. | Improvement over Python |
| Structured error taxonomy replacing 503/500 catch-all. `error_class` enum + `retry_after_seconds`. | Improvement over Python |
| Rate-limit detection with per-instance backoff state; Discovery routes around throttled instances. | Improvement over Python |
| Session recovery watchdog for browser crashes with auto-relaunch + one retry. | Improvement over Python |
| Instance registry moves from scaler-owned in-memory state to pg table `twitter_instances` — observable via SQL, survives restarts. | Improvement over Python |
| Prometheus instrumentation via shared observability substrate. | Standard for all Go adapters |
| **Baseline stealth in default scope** — Playwright stealth patches (spoof `navigator.webdriver` + related WebDriver telltales), timing jitter on searches, header rotation across sessions, random scroll pauses. All #1-4 of the § Stealth improvements list ship in T/a and T/c by default. Python has ZERO stealth config, so this is a step-change improvement. | 2026-07-16 walkthrough |
| **Deeper stealth options (#5-8) documented for empirical evaluation, not baseline scope.** Per-container fingerprint differentiation, mobile.twitter.com alt path, residential proxy pool, full behavior simulation. Tracked in § Stealth improvements with escalation triggers. Do not build upfront; add if T/a/T/c dev testing shows detection signals. | 2026-07-16 walkthrough |
| **Instance load-balancing: even distribution.** Random selection from healthy-and-not-backed-off pool via `ORDER BY RANDOM() LIMIT 1`. Statistically even over many selections, zero coordination cost across Discovery workers. Python's in-memory round-robin counter doesn't compose across worker processes. If metrics show uneven distribution, upgrade to pg-backed round-robin. | 2026-07-16 walkthrough |
| **Scroll-stop early exit on consecutive already-seen tweets** — Python has THREE stop conditions in `_do_search` (age > max_age_minutes, scroll_count ≥ 10, empty page); we add a FOURTH: consecutive_already_seen ≥ 3 → stop. Counter resets on new tweets so genuine interleaving doesn't trigger false halts. Fixes Python's under-utilization of exclude_urls — Python uses it only to per-tweet skip, not to short-circuit scroll. Real speed improvement for late attempts (7-10 out of 10) which currently walk mostly-known tweets. | 2026-07-16 walkthrough |

## Resolved during 2026-07-16 walkthrough

- **Q1 — Browser library** — Playwright-Go + Firefox for the scraping fleet. Fallback if the T/a PoC gate fails: `tebeka/selenium` + geckodriver + Firefox (mechanical port of Python's `session.py`). Firefox stays regardless.
- **Q2 — Dual-mode auth** — preserved AS PRIMARY DESIGN, not fallback. Framed as operational (VNC = login terminal, headless fleet = scrapers, cookies shared via disk), not anti-detection. VNC container is login-only (no automation library); headless containers are scraping-only (Playwright-Go, never login). Cookie reload via filesystem mtime check on each `EnsureAuthenticated`. (Updated 2026-07-21 — was NATS event → briefly pg NOTIFY → filesystem mtime is the actual right answer; see decisions.md 2026-07-21.)
- **Q3 — `/download_video` split** — split into `/extract_cdn_url` + external HTTP download. Enables parallel downloads while the browser stays free for search requests. **Twitter's CDN validates the full request context, not just the signed URL** — Python's `download_video_direct` attaches all browser cookies + User-Agent + Referer + Origin (`session.py:874-885`). That's what bypasses yt-dlp's rate limits. So `/extract_cdn_url` returns a bundle: `{cdn_url, cookies, user_agent, referer, origin}`. External HTTP client attaches all of it. If URL expiry proves flaky, retry via re-extract. Do NOT build a fallback to browser-driven download preemptively — add it only if we see download failures we can't handle otherwise.
- **Q4 — Cookie backup format** — collapsed. First-pass proposal was based on wrong information: Python's format isn't a bare array, it's already an object: `{exported_at: "...", cookies: [...]}` (see `session.py:298-304`). Python's restore reads `backup_data.get('cookies', [])` and ignores unknown top-level keys. So we preserve Python's existing shape and add fields (`captured_by_instance`, `auth_token_expires_at`, `twitter_username`) as we find them useful. Zero migration friction — Python silently ignores the extras. **Cookies stay shared between dev + prod** — the multi-account fraud detection risk from creating a second Twitter account outweighs the isolation benefits of separate accounts, especially given our READ-only usage stays well under Twitter's rate limits regardless.
- **Q5 — Instance registry table** — new dedicated pg table `twitter_instances`, mirroring the shape of Python's MongoDB collection (grepped `src/scaler/registry.py`) plus fields for our improvements. Schema:
  ```sql
  CREATE TABLE twitter_instances (
      instance_id      TEXT PRIMARY KEY,
      url              TEXT NOT NULL,
      status           TEXT NOT NULL CHECK (status IN ('available', 'busy', 'unavailable')),
      last_heartbeat   TIMESTAMPTZ NOT NULL,
      registered_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
      backoff_until    TIMESTAMPTZ,   -- rate-limit awareness, new in Go
      last_error_class TEXT,           -- observability, new in Go
      last_error_at    TIMESTAMPTZ
  );
  CREATE INDEX twitter_instances_available
      ON twitter_instances (status, last_heartbeat)
      WHERE status = 'available';
  ```
- **Q6 — Routing strategy** — **even load-balancing across the healthy fleet**. The whole point of running N instances is to spread load; "prefer freshest heartbeat" (my earlier lean) concentrates work on whichever instance most recently checked in, defeating the scale-out. Python uses in-memory round-robin via `TwitterRegistry._round_robin_index` (a singleton counter in the scaler process) — works fine within one worker but doesn't compose across workers. Go port: **random selection from the healthy-and-not-backed-off set**. Statistically even over many selections, no coordination needed across Discovery workers. If metrics show uneven distribution in practice, upgrade to a pg-backed round-robin counter. Sample query:
  ```sql
  SELECT instance_id, url
  FROM twitter_instances
  WHERE status = 'available'
    AND (backoff_until IS NULL OR backoff_until < NOW())
    AND last_heartbeat > NOW() - INTERVAL '30 seconds'
  ORDER BY RANDOM()
  LIMIT 1;
  ```
  Fallback: same query without the `status = 'available'` filter (include busy). Final fallback: default URL.
- **Q7 — Search query construction** — Discovery assembles the OR-syntax query via Wikidata + LLM team-alias RAG. T stays stateless w.r.t. alias resolution. Confirmed from Python's `TwitterWorkflow` + `archive/twitter/README.md`. Preserve exactly.
- **Q8 — Playwright-Go Firefox in Docker** — not a design question, an empirical PoC gate for T/a. If it works we ship Playwright-Go; if it doesn't we fall back to Selenium Go bindings (both fully specified above). Removed from the open-question list — it's an execution risk tracked in T/a's PoC gate criteria.

## Open questions

None blocking sign-off. T/a is unblocked (after O3/a-c ship).

## Stealth improvements — captured for T/a-and-beyond empirical testing

Python's current setup uses plain Firefox + Selenium with **zero** anti-detection config (confirmed from `session.py:123-141` — only idle-CPU preferences set, no fingerprint spoofing). Twitter tolerates this for our current load, but detection surfaces evolve. The following stealth options are documented for empirical evaluation during T's implementation phase — not all required by default, but captured here so future-us (or an agent picking up T's implementation) doesn't have to re-derive the list. What we have works, but it's not the best solution for stealth Twitter scraping.

Any of these can move up in scope if T/a's PoC gate or T/c's initial dev testing surfaces detection signals (429s, "Are you a robot?" pages, empty search results on known-good queries, unexpected session invalidation).

### Cheap, high-impact — bake into T/a and T/c by default

1. **Playwright stealth patches.** Spoof `navigator.webdriver`, `chrome-runtime`, plugin arrays, WebGL vendor/renderer, and ~15 other WebDriver telltales. Python has none of this. Small implementation cost — a single init script applied to the Playwright context. Ship in T/a alongside the browser wrapper.
2. **Timing jitter on searches.** Python's "10 attempts, 1 minute apart" is a regular pattern — perfect for detection heuristics that key on request regularity. Add ±20-40s uniform jitter within the window. Trivial code change in T/c's scroll orchestration.
3. **Header rotation.** Vary User-Agent (within a small pool of realistic current Firefox strings), Accept-Language, and related fingerprint headers across sessions or per-request. Python uses whatever Firefox ships by default. Small config addition in T/a.
4. **Random scroll pauses.** Add jittered 0.5-3s pauses between scroll actions during search. Python probably scrolls at consistent intervals. Small code addition in T/c.

### Medium-effort — evaluate during T implementation

5. **Per-container fingerprint differentiation.** Each headless instance runs with its own randomized WebGL renderer, Canvas signature, timezone. Real complexity — requires per-instance Playwright context config. Do this if concentrated same-fingerprint traffic from our IP starts drawing attention.
6. **`mobile.twitter.com` / `m.twitter.com` scrape path.** Different detection surface, sometimes weaker. Requires an alt DOM extraction path (scrape.go logic changes for a different DOM shape). Diverges from x.com's UI. Fallback if x.com detection gets tighter.

### High-effort, high-impact — evaluate if detection tightens

7. **Residential proxy pool.** Rotates IP per request via a residential proxy service (Bright Data, Oxylabs, similar). Real monthly cost, real integration work. Biggest impact on IP-based detection but only necessary if we start seeing detection specifically tied to our IP. Bloody expensive.
8. **Full behavior simulation** — mouse movements, keyboard events, natural navigation patterns. Diminishing returns beyond basic timing jitter (#2) and scroll pauses (#4). Do only if simpler measures leak.

### Explicit non-improvements

- **Multi-account rotation** — multi-account fraud detection risk outweighs the benefit for our READ-only use case. Losing the working account is worse than any isolation benefit.
- **Aggressive request pace** — we're not rate-limit constrained and going faster is a detection signal.

### Empirical evaluation triggers

The T/a-through-T/h implementation and dev testing should track these signals to know when to escalate up the list:

- 429 rate-limit responses per instance per day
- "Are you a robot?" interstitials encountered
- Empty search results on queries known to have matches (post-hoc check via `/search?f=live` in a browser)
- Session invalidations forcing re-auth via VNC
- Search response time drift (detection sometimes shows as latency injection before hard-blocking)

If any exceed a threshold, escalate from the currently-shipped set (#1-4) to #5-6, and eventually #7 if the pattern is IP-fingerprint-based.

## Deferred / not this proposal's scope

- **Search-string RAG improvements** — team alias tuning, hashtag combinations, negative filters. Lives in Discovery, not here.
- **Twitter API (official)** — no dependency on Twitter's paid API tiers. If we ever add one, it lands as a separate adapter under `internal/infra/twitter_api/`, not merged with this service.
- **Video validation / hashing / dedup** — happens in Video + Asset workflows post-download. See [`./video-dedup.md`](./video-dedup.md).
- **Cross-instance cookie refresh atomicity** — if two instances re-auth simultaneously via VNC (unlikely but possible), one overwrites the other's cookies. Follow-up phase; document but don't solve now.
- **Metrics dashboard** — Grafana panels for twitter service. Lands with the monitor/observability roadmap when we build project-specific dashboards.
