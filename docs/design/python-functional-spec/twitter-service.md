# Twitter service — Python behavior spec

Frozen WHAT-and-WHY detail from the
[Python functional-spec index](./README.md).

## Twitter service — Python behavior spec (WHAT + WHY)

Files referenced: `archive/twitter/{session.py,scrape.py,app.py,config.py,README.md}`.

### 1. Startup + dual-mode auth lifecycle

- **PURPOSE**: Boot the FastAPI surface immediately, then asynchronously establish an authenticated Firefox profile via a two-mode ladder (Selenium headless for scraping, non-Selenium Firefox for login) so Twitter's bot detection never sees Selenium during credential entry.
- **BEHAVIOR**:
  - FastAPI `lifespan` (`app.py:75-90`) launches `background_startup` on a daemon thread so `/health` responds even while auth is pending.
  - `startup()` (`session.py:705`) calls `ensure_authenticated()`, which tries: reuse live session → cookie backup restore → boot headless Firefox against the persisted profile → give up.
  - On give-up, `startup()` sends a one-shot email (`_send_login_notification`), launches non-Selenium Firefox via `subprocess.Popen` on `DISPLAY=:99` (`session.py:313-336`), and spawns a poll thread (`auto_verify_loop`, `session.py:732-756`) that watches `cookies.sqlite` mtime.
  - When the loop detects fresh mtime, it kills manual Firefox, re-boots Selenium headless, verifies logged-in, and backs cookies up.
  - `ensure_authenticated()` has a 60-second warm-path shortcut: if `last_activity` is recent, only a cheap `driver.current_url` check runs (`session.py:426-437`).
- **REMARKS**:
  - The singleton pattern (`__new__` with lock, `session.py:52-58`) means all endpoints/threads share one browser — no per-request isolation.
  - Manual Firefox and Selenium Firefox share the same `profile_dir`; they never coexist, but the killing (`pkill -9 firefox`, `session.py:316,341`) is process-wide and would nuke unrelated Firefoxes on the same host — safe only inside a dedicated container.
  - `authenticate()` is referenced by the `/authenticate` endpoint (`app.py:233`) but does not exist on `TwitterSessionManager`. That endpoint is dead code that raises `AttributeError` at call time — a rebuild should not port it.

### 2. Cookie persistence during active scraping

- **PURPOSE**: Keep the JSON backup file fresh so container restarts (or a sibling scaled instance) can resume without a manual re-login round-trip.
- **BEHAVIOR**:
  - Backup is event-driven, not timer-driven. It happens: after every successful `_do_search` (`session.py:691`), after cookie-restore succeeds (`session.py:470`), after Firefox-profile fallback succeeds (`session.py:480`), after post-manual-login verify succeeds (`session.py:749`, `769`).
  - `_backup_cookies_to_host` filters to `x.com` domain cookies, refuses to write if `auth_token` is missing (`session.py:293-296`), writes JSON `{exported_at, cookies}` atomically over the backup path.
  - The **only** mtime tracking anywhere is in `auto_verify_loop`, watching `profile_dir/cookies.sqlite` (`session.py:737-740`) — that's the trigger for detecting completion of manual login, not for scheduling backups.
  - No throttling, no debounce — a busy instance backs up on every search, which for the 30 s cycle means once per goal search per instance.
- **REMARKS**:
  - Backup path defaults to `~/.config/found-footy/twitter_cookies.json` but is overridden via `TWITTER_COOKIE_BACKUP_PATH` (`session.py:88-89`); scaled instances share one path via the host mount — race-safe only because writes are last-writer-wins, all writers hold identical auth cookies.
  - The `auth_token` guard is load-bearing: without it a partial cookie set could overwrite a good backup and brick every instance on next restart. Silent regression risk if a Go port drops this check.

### 3. Search API contract

- **PURPOSE**: One HTTP call to fan out a "find recent videos matching this query" browser scrape, returning a normalized list to the Temporal activity.
- **BEHAVIOR**:
  - `POST /search` (`app.py:173-216`) body: `{search_query: str, exclude_urls: list[str]=[], max_age_minutes: int=5}`.
  - Success response: `{status: "success", videos: [...], count: N}`. Each video dict carries `search_term, tweet_url, tweet_id, tweet_text[:200], username, timestamp, discovered_at, search_index (always 0), video_index, source="browser_automation", requires_ytdlp=True, video_page_url, duration_seconds, tweet_age_minutes` (`session.py:649-664`).
  - Errors: `503` with body `{error: "authentication_required", vnc_url, ...}` on `TwitterAuthError`; `500` bare `detail=str(e)` on anything else.
- **REMARKS**:
  - `requires_ytdlp=True` is a lie in the current stack — downloads go through `/download_video`, not yt-dlp. Rebuild should drop or repurpose.
  - `search_index=0` is a vestigial field; nothing in the response distinguishes attempt N from attempt 1 — the caller tracks that.
  - `exclude_urls` is normalized to a *tweet-ID set* (`session.py:526-530`), so callers can pass either `/user/status/…` or `/i/status/…` shapes interchangeably. Under-documented, load-bearing for cross-attempt dedup.

### 4. Scroll termination

- **PURPOSE**: Bound work so a search over a quiet query doesn't scroll forever, and end early when we've walked past the "recent enough" horizon.
- **BEHAVIOR**: Four stop conditions in `_do_search` (`session.py:590-684`):
  1. Any tweet's `tweet_age_minutes > max_age_minutes` sets `found_old_tweet=True` and breaks (`session.py:612-616`). Primary path.
  2. `scroll_count >= max_scrolls` (hardcoded 10) exits the while loop (`session.py:590`).
  3. `len(tweet_elements) == 0 and scroll_count >= 1` — empty results page after first scroll (`session.py:675-677`).
  4. Any exception on `driver.get` (page-load timeout of 30 s) raises `RuntimeError` all the way out (`session.py:557-572`).
- **REMARKS**:
  - The age check requires a parseable `<time datetime>` — if that element is missing or malformed, `tweet_age_minutes` is `None` and the "too old" branch never fires for that tweet.
  - No "found N videos, stop" cap. Callers rely on `max_age_minutes` alone to bound cost.
  - Inside the per-tweet loop, exceptions are swallowed with `continue` (`session.py:669-670`), losing the tweet silently. Load-bearing forgiveness — a rebuild that surfaces those errors would drown in noise.

### 5. DOM extraction fields (per tweet)

- **PURPOSE**: Reduce a tweet `article` element to the minimal record needed downstream, tolerating X's shifting markup by trying multiple selectors.
- **BEHAVIOR** (all in `scrape.py`):
  - `extract_status_link` — first `a[href*='/status/']` (`scrape.py:94-105`). Also the source of `tweet_url`.
  - `extract_tweet_age_minutes` — `time[datetime]` parsed ISO-8601, delta vs UTC now (`scrape.py:108-120`).
  - `is_promoted_tweet` — XPath text-contains for "Promoted"/"Ad" (`scrape.py:123-133`).
  - `extract_tweet_text` — `[data-testid='tweetText']`, defaults to `"Text not found"`, truncated to 200 chars (`scrape.py:136-145`).
  - `extract_video_duration` — walks selectors `video → [data-testid='videoPlayer'] → [data-testid='videoComponent']` (`scrape.py:175`), then for duration tries `video.duration` attribute first, then four overlay-text selectors (`scrape.py:192-197`) parsed as `M:SS`.
  - Post-processing in `_do_search`: `extract_tweet_id_from_url` splits `/status/` (`scrape.py:44-53`), `extract_username_from_url` from URL path (`scrape.py:56-72`) — returns `"Unknown"` for `/i/status/…`, `video_page_url` is built as `https://x.com/i/status/{tweet_id}` (`session.py:661`).
- **REMARKS**:
  - `is_truncated_snowflake` (`scrape.py:75-86`, `MIN_SNOWFLAKE_LEN=18`) filters IDs that look like X's upstream rendering quirks for deleted/quoted tweets — these won't syndicate to a downloadable video. Same guard exists in the downloader; if the Go rewrite drops it, ghost tweets pass through to the video pipeline and fail later.
  - The XPath `Promoted`/`Ad` match is over the whole subtree — false-positive risk if a user tweets those words. No sentinel test.
  - Duration extraction is best-effort; a `None` duration still gets emitted and the caller sorts by "top 5 longest" downstream, so `None` videos rank last but aren't excluded.

### 6. Download flow — `/download_video`

- **PURPOSE**: Bypass yt-dlp rate limits by piggybacking on the already-authenticated browser session's CDN cookies.
- **BEHAVIOR** (`session.py:778-916`, `app.py:291-333`):
  - Authorization gate: `authenticated and driver` truthy, else raises `TwitterAuthError` → HTTP 401.
  - `driver.get(tweet_url)` with 20 s page-load timeout, 2 s wait for player init.
  - Post-navigation re-checks for `login`/`flow` in URL (`session.py:809-811`); logs out state → 401.
  - Three URL-extraction methods in order: (1) `video[src]` containing `video.twimg.com` (`session.py:816-822`); (2) regex `https://video\.twimg\.com/…` over `page_source`, MP4 preferred (`session.py:829-841`); (3) JS walk over `<video>` and nested `<source>` elements (`session.py:845-862`). First match wins.
  - Downloads with `requests.get(video_url, cookies=<all-browser-cookies>, headers={UserAgent from driver, Referer: https://x.com/, Origin: https://x.com}, stream=True, timeout=30)` (`session.py:874-885`), streams 8 KB chunks to `output_path`.
- **REMARKS**:
  - The `Referer`/`Origin` on `x.com` is load-bearing — the CDN rejects raw requests without them. Silent regression if a Go port omits either.
  - Cookies are forwarded en masse; no filtering to `x.com` domain here (unlike backup). Works because `requests` sends them to `video.twimg.com` and Twitter's CDN authenticates the same session token.
  - Method 2's page-source regex will happily pick up a poster-image thumb URL if no MP4 exists — the `mp4_urls` filter mitigates but doesn't eliminate this.

### 7. Instance identity + registration lifecycle

- **PURPOSE**: Let the scaler / worker pool discover which twitter containers are up and route work to them, without hardcoded topology.
- **BEHAVIOR**:
  - `INSTANCE_ID = TWITTER_INSTANCE_ID or socket.gethostname()` (`app.py:29`). `CONTAINER_NAME = $HOSTNAME` (`app.py:31`). URL = `http://{CONTAINER_NAME}:{SERVICE_PORT}` (`app.py:34-37`).
  - After `session.startup()` returns (blocking or not), `register_with_registry()` fires (`app.py:40-50`), calling `src.scaler.registry.registry.register(INSTANCE_ID, url)`. Failure is logged non-fatal.
  - Heartbeat thread (`app.py:53-62`) calls `registry.heartbeat(INSTANCE_ID)` every 10 seconds; exceptions swallowed silently.
  - Lifespan shutdown calls `registry.unregister(INSTANCE_ID)` (`app.py:85-89`) then `session.cleanup()`.
- **REMARKS**:
  - Registry is imported lazily inside each function so twitter doesn't hard-depend on the scaler module. If the import fails, twitter runs fine but is invisible to the scaler — a "healthy in isolation, silently orphaned in prod" failure mode.
  - Registration happens *only after* auth completes; a container stuck at manual-login never registers.
  - `INSTANCE_ID` falling back to `socket.gethostname()` collides with `CONTAINER_NAME` under Docker Compose scaling. The `profile_dir` disambiguation uses a hash of `$HOSTNAME`, so multiple instances get distinct profiles even under the same instance-ID fallback path — but the *registry* row for two instances with the same hostname would collide. Real deployments always set `TWITTER_INSTANCE_ID` per compose service.

### 8. Error taxonomy

- **PURPOSE** (inferred): Signal auth-vs-generic failures so the caller can decide "retry later" (503/401) vs "propagate" (500).
- **BEHAVIOR**:
  - `/search`: 503 on `TwitterAuthError` (with a structured `detail` dict); 500 with `detail=str(e)` on anything else (`app.py:203-216`).
  - `/download_video`: 401 on `TwitterAuthError` or not-authenticated; 500 with `{status: "error", error: ...}` for extraction failures or `RuntimeError`. Extraction failures inside `download_video_direct` return `{status:"error", error:...}` normally but `/download_video` wraps them in a `JSONResponse(status_code=500,...)` — so an extract-fail HTTP-wise looks identical to a browser crash.
  - `/health`: 200 healthy or 503 unhealthy.
  - Auth endpoints: 500 on any exception. `/authenticate` is broken (see §1).
- **REMARKS**:
  - "500 for everything else" is indistinct on purpose — the workflow layer catches broadly and reschedules — but it loses signal on transient (network flap) vs permanent (extraction impossible) failures. The Go rebuild has room for a real error taxonomy here.

### 9. VNC container vs headless container

- **PURPOSE**: Separate the login-only surface (needs a visible desktop) from the scraping fleet (no desktop needed, fewer resources, no port conflicts).
- **BEHAVIOR** (per `README.md:132-146`, `session.py:106-149`):
  - `twitter-vnc` runs Xvfb + x11vnc + websockify on `DISPLAY=:99`, exposing VNC on host port 3203 (prod) / 4203 (dev). API on `:8888` internal.
  - Scaled `twitter-N` containers run headless — Selenium `--headless` flag, no Xvfb, no VNC.
  - Both mount the same host directory holding `twitter_cookies.json`. VNC container is where a human logs in; every scaled headless container restores from the shared JSON on next startup.
  - Manual Firefox in VNC container spawns with `DISPLAY=:99` explicitly (`session.py:320`) so the user sees it through noVNC.
  - Selenium headless mode is chosen by `TwitterConfig.headless` env-var (`config.py:20`) — same code path, `_setup_browser` reads the flag (`session.py:112-114`).
- **REMARKS**:
  - Both container roles share the *same singleton* `TwitterSessionManager` code — behavior identical except for the display. A rebuild could split them but doesn't need to.
  - Cookie flow is unidirectional: VNC container writes → shared file → headless containers read on next auth cycle. If a headless container's session goes stale mid-day, it hits `TwitterAuthError` and cannot self-heal until a human touches the VNC container. Silent operational gotcha.

### 10. Nuances a rebuild would silently regress on

- **60 s warm-path fast-check**: `ensure_authenticated` skips a full x.com/home GET if `last_activity` is <60 s ago. Saves ~3-4 s per search — critical during a goal burst where multiple queries fire back-to-back.
- **`busy` flag** (`session.py:70,509,515`): drives the scaler's scale-down safety; True only during `_do_search`. Read via `/status` (`app.py:158-170`). Rebuild must preserve this semantic exactly or the scaler will terminate mid-search.
- **`auth_token` presence guard** on both cookie backup (`session.py:293-296`) and restore (`session.py:216-218`): silently drops the operation. Load-bearing safety valve.
- **Instance-scoped profile dir** via hostname hash (`session.py:76-84`): parallel instances would corrupt each other's Firefox profile without this.
- **Idle-CPU containment Firefox prefs** (`session.py:139-141`): disables autoplay, GIF animation, backgrounded video decode. Without these, the warm-idle x.com tab bleeds ~20 % CPU per container.
- **Truncated snowflake rejection** (`session.py:632-638`, `scrape.py:75-86`): skips <18-digit tweet IDs before enqueue, avoiding downstream download failures.
- **`exclude_urls` → tweet-ID set** normalization (`session.py:526-530`): accepts both `/user/status/` and `/i/status/` shapes. Rebuild must keep both — callers pass a mix.
- **One-shot notification file** `/data/.login_notification_sent` (`session.py:98-104`, `399-404`): prevents email spam and clears itself once auth succeeds.
- **Cookie-backup file overwritten every successful search**: no consistency-of-content check. If a cosmic-ray flip corrupted `auth_token` mid-session, backup rewrites the broken cookies.
- **`_do_search` swallows per-tweet exceptions** silently (`session.py:669-670`): under-tested but intentional. Rebuild that surfaces these will need a rate-limited log.
- **Dead `/authenticate` endpoint** (`app.py:219-241` → nonexistent method): can be dropped in the rewrite.
- **`search_timeout=5`** in `config.py:31` is a *post-load sleep*, not a network timeout; renaming it in the rewrite would be less confusing.

---
