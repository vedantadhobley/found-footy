# Twitter service port — design proposal (Phase T)

**Status:** design-first draft. Do not implement anything from this
doc until it's reviewed + signed off.

**Revision log:**
- 2026-07-16 (first pass, this doc) — initial proposal. Phase T is
  sequenced right after O3, before O4, per [`discovery.md`](./discovery.md)
  Q3 sign-off — Twitter is the pipeline's most load-bearing external
  dependency and deserves its own dedicated design + review runway.

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

## Browser automation library choice — the biggest single decision

Four candidates:

| Library | Browsers | Pure Go | Anti-detection | Trade-offs |
|---|---|---|---|---|
| **Playwright-Go** | Chromium, Firefox, WebKit | No (Node runtime for driver, ~50MB) | Strong out-of-box | Multi-browser matters — Firefox is Python's proven-good choice against Twitter's detection. Playwright's API is modern, retries + auto-waits built in. Downside: Node runtime bundled with the driver adds container size + a language boundary. |
| **Rod** | Chromium only | Yes | Moderate (stealth plugin available) | Cleanest pure-Go API. But Chromium-only means dropping Firefox → higher bot-detection exposure. |
| **Chromedp** | Chromium only | Yes | Weak (need manual spoofing) | Mature, direct CDP. Same Chromium-only limitation as Rod. Lower-level API. |
| **Selenium Go bindings** (`tebeka/selenium`) | Any WebDriver browser (geckodriver → Firefox, chromedriver → Chrome) | Semi (needs WebDriver process) | Same as Python (weak; relies on browser + profile config) | Directly ports Python's approach — Selenium + Firefox. Zero learning curve. But we inherit Selenium's known anti-detection footprint (`navigator.webdriver = true`), which is exactly the reason Python has the dual-mode workaround. |

**My lean: Playwright-Go.**

Reasoning:
- **Firefox support** — matches Python's proven-good browser choice against Twitter's detection.
- **Modern API** with async/retries/auto-waits + typed responses. Less boilerplate than Selenium bindings.
- **Anti-detection defaults** — Playwright spoofs `navigator.webdriver` and other WebDriver telltales by default. May let us drop Python's dual-mode workaround entirely (test in dev; keep dual-mode as fallback).
- **Container size** — Node runtime + driver adds ~50MB. Not zero but not disqualifying.

Runner-up: Selenium Go bindings + geckodriver. Same-shape port, minimal risk, but inherits Selenium's known detection footprint.

**Open question at the top of the list**: Playwright-Go's Firefox support has historically been less battle-tested than Chromium. Test with a dev container against Twitter search + login flows before committing.

## Authentication + cookie lifecycle

Preserving Python's win, adapting to Go + Playwright:

- **Cookie storage:** JSON file at `/config/twitter_cookies.json` (host-side `~/.config/found-footy/twitter_cookies.json`), same shape as Python for cross-compat during migration. Bind-mounted into every twitter container.
- **Startup sequence:**
  1. Launch Firefox via Playwright in a persistent context (`launchPersistentContext`) pointed at `/data/firefox-profile/`.
  2. On startup, if `/config/twitter_cookies.json` exists AND is not expired → load cookies into the Playwright context.
  3. Probe an auth-required endpoint (`x.com/home`) to verify the session is live.
  4. If probe passes → mark service healthy, expose `/health` 200.
  5. If probe fails → mark service `authenticated: false`, expose `/health` 503, launch manual Firefox via `xdotool` or subprocess for VNC visibility.
- **Manual re-auth flow (VNC):**
  1. User visits `http://<host>:<vnc_port>`, sees Firefox.
  2. User logs into Twitter (real Firefox, no automation library attached — same as Python's dual-mode LEFT half).
  3. Service polls the manual-Firefox profile for the `auth_token` cookie every 5s.
  4. Once detected, service exports cookies from manual-Firefox profile → `/config/twitter_cookies.json`.
  5. Playwright reloads cookies into its context → service transitions to `healthy`.
  6. Broadcast a `twitter.reauthed` NATS event so scaled instances refresh their cookies from the backup file.
- **Cookie expiry monitoring:** Playwright cookie inspection every N minutes; if `auth_token` cookie is missing or expired → mark unhealthy, fire `twitter.auth_expired` NATS event.

**Open question:** does Playwright-Go's persistent context play well with Xvfb + VNC for the manual-login flow, or do we need to keep a subprocess-launched raw Firefox for the manual half? Requires testing.

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
- **Cookie backup shared** via bind-mount `/config/twitter_cookies.json` — all instances read the same file, `twitter.reauthed` NATS event triggers reload.

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

### T/a — Skeleton service + Playwright-Go PoC

- `cmd/twitter/main.go` — HTTP server (net/http or chi router)
- `internal/twitter/service.go` — service wrapper (state machine: `starting`, `authenticated`, `unauthenticated`, `backoff`)
- `internal/twitter/browser.go` — Playwright-Go wrapper: launch Firefox in persistent context, load cookies, verify session
- Health + status endpoints only

PoC gate: launch Playwright + Firefox in a dev container, load a cookie fixture, verify session against `x.com/home`. If Firefox support in Playwright-Go proves fragile → fall back to Selenium Go bindings + geckodriver.

~400 lines.

### T/b — Auth + cookie lifecycle

- Dual-mode Firefox launch (Playwright persistent context OR raw subprocess for manual mode — pending T/a resolution).
- Cookie backup file read/write (JSON, same shape as Python).
- Manual-login detection loop.
- VNC infrastructure (Xvfb + x11vnc + websockify) in `docker/twitter-vnc/Dockerfile`.
- Headless variant in `docker/twitter-headless/Dockerfile`.
- Endpoints: `/authenticate`, `/auth/verify`, `/auth/launch-browser`.

~600 lines (includes docker/ files).

### T/c — Search + scrape

- `internal/twitter/search.go` — scroll loop, DOM extraction (mirrors Python `scrape.py` helpers).
- Endpoint: `/search` — full contract with `exclude_urls`, `max_age_minutes`, structured response.
- Instrumentation: Prometheus counters + histograms via shared observability substrate.
- Structured error responses with `error_class` taxonomy.

~700 lines including tests.

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
| Preserve Python's dual-mode auth pattern (manual Firefox for login, automation library for scrape) as fallback. Test whether Playwright-Go's anti-detection is enough to skip manual half in normal operation. | 2026-07-16 walkthrough |
| Preserve Python's cookie backup file at `/config/twitter_cookies.json` shared via bind mount. Same shape as Python for migration compat. | Minimize cutover risk |
| Preserve Python's dual-container deployment (twitter-vnc + N twitter-headless). Scale N via docker compose replicas. | Proven pattern |
| Preserve Python's URL exclusion + time-based scroll termination + OR-syntax search — the search API contract from Discovery's perspective is unchanged. | Discovery integration compat |
| Split `/download_video` into `/extract_cdn_url` + downloader — enables parallel downloads without serializing on the browser. | Improvement over Python |
| Structured error taxonomy replacing 503/500 catch-all. `error_class` enum + `retry_after_seconds`. | Improvement over Python |
| Rate-limit detection with per-instance backoff state; Discovery routes around throttled instances. | Improvement over Python |
| Session recovery watchdog for browser crashes with auto-relaunch + one retry. | Improvement over Python |
| Instance registry moves from scaler-owned in-memory state to pg table `twitter_instances` — observable via SQL, survives restarts. | Improvement over Python |
| Prometheus instrumentation via shared observability substrate. | Standard for all Go adapters |

## Open questions

1. **Browser library — Playwright-Go with Firefox, or fall back to Selenium Go bindings + geckodriver?** My lean: Playwright-Go, with a T/a PoC gate that tests Firefox support against Twitter. Fall back to Selenium bindings if Firefox in Playwright-Go proves fragile.

2. **Can we drop the dual-mode manual-Firefox-for-login pattern with Playwright's anti-detection?** Would simplify the service. Test in dev: try Playwright-driven login flow with anti-detection defaults; keep dual-mode as fallback if bot detection catches it.

3. **`/download_video` — split into extract + external download, or preserve Python's browser-driven download?** My lean: split. But the browser-driven download bypasses yt-dlp rate limits by riding session cookies — if Twitter's CDN URLs require cookies attached to the request, we still need to attach them from the HTTP client after extraction. Confirm CDN URL auth requirements before committing.

4. **Cookie backup format — same JSON as Python, or move to a richer format?** Python's format is minimal (list of cookie dicts). Would gain from adding metadata like `captured_at`, `expires_at`, `captured_by_instance` for observability. My lean: extend the JSON with metadata, keep the core cookies-array-shape backward-compatible for migration.

5. **Instance registry table — new dedicated table or extend an existing one?** No existing table fits. New table `twitter_instances(id TEXT PK, url TEXT, healthy BOOL, busy BOOL, backoff_until TIMESTAMPTZ, last_heartbeat TIMESTAMPTZ, cookie_expires_at TIMESTAMPTZ)`. My lean: new dedicated table.

6. **Discovery's routing strategy across instances** — round-robin, least-busy, hash-by-event-id? My lean: least-busy from the registry (query `WHERE healthy AND (backoff_until IS NULL OR backoff_until < NOW()) AND NOT busy ORDER BY last_heartbeat DESC LIMIT 1`). Falls back to round-robin if all instances are busy.

7. **Search query construction — does Discovery already assemble the OR-syntax string, or does T do it?** Python has Discovery assemble it (from Wikidata + LLM team alias lookup). Preserve. T stays stateless w.r.t. alias resolution. Confirm.

8. **Playwright-Go Firefox in Docker — proven to work with headless + persistent context under Docker?** Some Playwright-Go issues in GitHub report Firefox startup problems in containers. T/a PoC needs to confirm.

Sign off on the 8 above (or mark as "confirm in T/a PoC") and T/a starts.

## Deferred / not this proposal's scope

- **Search-string RAG improvements** — team alias tuning, hashtag combinations, negative filters. Lives in Discovery, not here.
- **Twitter API (official)** — no dependency on Twitter's paid API tiers. If we ever add one, it lands as a separate adapter under `internal/infra/twitter_api/`, not merged with this service.
- **Video validation / hashing / dedup** — happens in Video + Asset workflows post-download. See [`./video-dedup.md`](./video-dedup.md).
- **Cross-instance cookie refresh atomicity** — if two instances re-auth simultaneously via VNC (unlikely but possible), one overwrites the other's cookies. Follow-up phase; document but don't solve now.
- **Metrics dashboard** — Grafana panels for twitter service. Lands with the monitor/observability roadmap when we build project-specific dashboards.
