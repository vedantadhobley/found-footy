# found-footy Go rebuild — roadmap to La Liga (2026-08-15)

**Target:** La Liga season starts **Friday 2026-08-15**. Go rebuild must be at feature-parity with the Python system, running in prod, replacing Python, by first La Liga match kickoff.

**Runway:** 4 weeks (2026-07-18 through 2026-08-14). Buffer weekend before season start intentionally kept clear for late-breaking fixes + cutover verification.

**Philosophy:** feature parity means the user experience matches or exceeds Python's — same clip discovery quality, same or better latency, same or better rank ordering. Internally the rewrite is dramatically cleaner (typed enums, structured concurrency, dual-write observability, atomic spawn semantics) but that's not user-visible; parity is measured by output.

**Scope discipline:** user's call 2026-07-18 — **no shortcuts, full scope**. Everything in this document is in-flight; nothing is cut for expedience. If a week runs long, we push into weekend/evening rather than skip work. The doc is aggressive on purpose.

**Ground truth:** the detailed Python behavior spec (`docs/rebuild/python-functional-spec.md` addendum, updated 2026-07-18) describes what each subsystem does today with WHY-annotations for load-bearing behaviors. Every week's checklist below is grounded in that spec; when in doubt during implementation, consult it first.

---

## Where we are on 2026-07-18

### Shipped and stable

- **F, S1–S7** — scaffold + observability substrate + all infra adapters (pg, NATS, S3, LLM, Temporal, apifootball, syndication, wikidata, twitter HTTP client)
- **O1** — IngestWorkflow (daily 00:05 UTC schedule, tracked-team roster refresh, fixture staging + smart lookahead)
- **O2** — ActivePollWorkflow (30 s) + StagingPollWorkflow (15 min cron)
- **O3/a** — event composer (dual-write pg `event_log` + NATS publish)
- **O3/b** — Monitor emits 5 event kinds + atomically inserts `event_downstream_workflows` row + spawns Discovery
- **O3/c** — DiscoveryWorkflow stub + `MarkDownstreamComplete` activity
- **T/a** — Twitter service scaffold: Playwright-Go + Firefox launches, stealth patches applied, cookies load, session verify passes, minimal `/search` endpoint returns real tweets

### Not shipped, blocking parity

Twitter service full (T/b–T/h), full video pipeline (V/a–V/i), AssetWorkflow, Discovery → Video → Asset spawn chain, consumer-facing API surface, migration + cutover plan.

### Non-blocking cleanup deferrable to post-season

- Scenario YAML additions for O3/b's 5 emissions
- Pre-existing enum-casing scenario test failures
- Grafana dashboards + Loki queries update
- Full V/h backfill of the Python-era corpus's perceptual hashes (accept some early dup S3 objects; backfill during international break in October)

---

## Weekly breakdown

Each week has a **critical path** (must ship to hit next week) and **buffer work** (helpful but bumpable). Dates are UTC; day boundaries are aspirational, not strict.

### Week 1 — Twitter service usable (2026-07-18 → 2026-07-25)

**Goal by Friday:** Go Twitter service can auth via VNC + auto-refresh cookies + serve real search results including the 3-stop scroll termination + consecutive-already-seen early-stop.

Critical path:

- **T/b — Auth + cookie lifecycle** [~2 days]
  - ✅ **T/b.1 — Cookie backup file management** (shipped 2026-07-21, commit 6234123): `internal/twitter/cookies_backup.go` — `Fingerprint()` (order-independent sha256 of name+value+domain), `WriteBackup()` (atomic via `os.CreateTemp` + `os.Rename`, per-writer unique tmp names for correct concurrent-writer semantics), `ReadBackup()`, `BackupFileMtime()`. `auth_token` presence guard on BOTH write and read paths (broken sessions never persisted / trusted). Domain filter (x.com/twitter.com only). 10 unit tests including 5×20×50 concurrent writer/reader stress.
  - ✅ **T/b.2 — Auth flow + state machine + HTTP surface** (shipped 2026-07-21): `internal/twitter/auth.go` — `EnsureAuthenticated(ctx)` runs mtime-check → warm-path → verify sequence. `sessionBrowser` interface (`internal/twitter/browser_iface.go`) extracted so auth is testable without Playwright. `Browser.GetCookies()` + `Browser.ReplaceCookies()` added to complete the round-trip. State machine gained `StateLoading` (transient during reload); auth flow transitions to `StateHealthy` / `StateUnauthenticated` / `StateFailed` as appropriate. Typed error `ErrUnauthenticated` for `errors.Is` at call sites. `authMu` serializes concurrent callers (5 concurrent callers dedupe to 1 verify via warm-path). `busy` flag on `/status`. Endpoints: `GET /authenticate` (state + operator-facing reauth instructions with `TWITTER_VNC_URL` + `TWITTER_VNC_START_CMD` env vars surfaced in response), `POST /auth/verify` (VNC container hits this after manual login to force headless fleet to reload + verify). `BackupCookies(ctx)` method with fingerprint dedupe (unchanged cookies skip the write entirely — no spurious mtime bump). 16 unit tests including concurrent-callers, external-reload-during-warm-path, browser-failure-escalates-to-StateFailed.
  - ⏳ **T/b.3 — VNC container** — Xvfb + x11vnc + websockify + raw (non-headless) Firefox. Opt-in via `docker compose --profile vnc up` — VNC container runs only when operator needs to log in (per decisions.md 2026-07-21).
  - ⏳ **T/b.4 — Docker compose splits** — dev + prod stacks pick up VNC + N headless split; `twitter-vnc` and `twitter-N` share the cookie-file volume; VNC gets its own persistent profile-dir volume separate from headless.
  - ⏳ **T/b.5 — Ancillary hardening** — instance-scoped profile dir via hostname hash (multi-instance profile corruption); idle-CPU containment Firefox prefs (autoplay off, GIF animation off, ~20% CPU savings, twitter-spec §10); structured `auth_expired` log event for operator alerting; hook `BackupCookies` invocation into the search success path (lands with T/c).
- **T/c — Full search + scrape** [~2 days]
  - Port `scrape.py`'s DOM extraction helpers (tweet URL, age, video duration, promoted-tweet skip, snowflake sanity check per `MIN_SNOWFLAKE_LEN = 18`)
  - **Preserve typed error taxonomy** for downloads: `VideoNotAvailableError` (404), `VideoGeoRestrictedError` (403), `TwitterRateLimitedError` (429), `VideoCDNTimeoutError`, `VideoDownloadError`, `VideoMalformedURLError` with `failure_mode=truncated_snowflake` (video-spec §2, load-bearing for Grafana observability)
  - Time-based scroll termination (`max_age_minutes` boundary)
  - `max_scrolls = 10` safety cap (from Python's `_do_search`)
  - Empty-page stop after ≥1 scroll
  - **Consecutive-already-seen early-stop (3 default) — improvement over Python** (per video-dedup Q2)
  - `exclude_urls` normalized to tweet-ID SET so callers can pass either `/user/status/…` or `/i/status/…` shapes (twitter-spec §3, load-bearing)
  - Baseline stealth #2 (timing jitter ±20–40 s on Discovery's search cadence)
  - Baseline stealth #4 (random 0.5–3 s scroll pauses)
  - Baseline stealth #3 (User-Agent + Accept-Language rotation)
  - `Referer: https://x.com/` and `Origin: https://x.com` headers on CDN download requests (twitter-spec §6, load-bearing — CDN rejects without them)
  - Per-tweet exception swallowing to avoid log flood (twitter-spec §4, deliberate)
- **T/d — Rate-limit detection + backoff** [~half day]
  - 429 detection + "Are you a robot?" interstitial detection
  - Per-instance `backoff_until` written to `twitter_instances` pg table
  - `/status` surfaces backoff state; Discovery-side router skips backed-off instances
- **T/e — Instance registry + heartbeat + scaler** [~1 day]
  - `twitter_instances` pg schema (see twitter-port.md Q5)
  - 10 s heartbeat goroutine
  - 30 s eviction cutoff on staleness (passive eviction — do not mutate stale rows, they fall out of the filter and reappear on next heartbeat, per scaler-spec §A3)
  - `ORDER BY RANDOM()` router in Discovery (per twitter-port.md Q6)
  - **Single fallback URL path** — collapse Python's three redundant fallbacks (scaler-spec §A6, drop the stale `TWITTER_SESSION_URL` default)
  - **Registry routing wired end-to-end** — either drop `busy` state entirely or make the router actually consult it (scaler-spec §A4 flags Python's dead-code hook; rebuild picks one)
  - Scaler service: separate binary watching two independent signals — worker workflow count (Temporal `ListWorkflowExecutions`) + twitter active goal count (pg query on `events` where downstream not complete); each signal scales its own service pool, min=2/max=8 per Python's tuning; 30 s check cadence + 60 s cooldown
- **T/f — CDN URL extract + external download split** [~1 day]
  - `/extract_cdn_url` endpoint returns bundle `{cdn_url, cookies, user_agent, referer, origin}`
  - Discovery-side Go HTTP downloader attaches the bundle exactly, streams to disk
  - Cookies (`auth_token`, `ct0`, `twid`, `guest_id`) attached to CDN request (video-spec §2, load-bearing for `amplify_video` variants)
  - Typed error propagation preserved through the boundary
- **T/g — Session recovery watchdog** [~half day]
  - `page.Ping()` heartbeat every N seconds
  - On failure: relaunch context + reload cookies + one retry
  - Emit `twitter.browser_recovered` NATS event
- **T/h — Twitter HTTP client adapter update** [~half day]
  - Match the new `/extract_cdn_url` + typed error shape
  - Update S7 adapter tests to cover the new endpoint

**Milestone gate before Week 2:** Discovery can search Twitter end-to-end from Go worker → Twitter service → real tweets returned → `event_tweets` populated. Manual match-day rehearsal against a live weekend match validates.

**Risks:**

- **Cookies expire mid-week** — mitigation: T/b lands Monday–Tuesday so VNC re-auth is possible by Wednesday.
- **DOM extraction misses fields** — mitigation: scrape.py has ~15 years of X-side rendering quirks encoded; port carefully, don't reinvent selectors.

### Week 2 — Video pipeline core (2026-07-25 → 2026-08-01)

**Goal by Friday:** Given a candidate tweet URL, the pipeline downloads bytes, hard-filters on metadata, computes content hash, batch-dedupes, checks S3 corpus, runs vision #1, computes perceptual hash, batch-dedupes perceptual, checks S3 perceptual. No upload yet.

Critical path:

- **V/a — Download + metadata hard-filter + content hash** [~2 days]
  - `/extract_cdn_url` endpoint on the Twitter service returns `{cdn_url, cookies, user_agent, referer, origin}` bundle
  - Go HTTP downloader attaches all of them, streams to disk
  - ffprobe metadata extract, hard-filter with short-circuit (duration 3–90 s, aspect 1.75–1.80, short-edge ≥ 600 px, framerate ≥ 20 fps)
  - SHA256 content hash, batch dedup within event, S3 content-hash lookup, popularity via `COUNT(video_shares)`
- **V/b — Vision call #1 (combined analyze)** [~1.5 days]
  - Structured JSON output: 5 fields (soccer, screen, clock, added, stoppage_clock) — preserve the shape (vision-spec §3)
  - Dual-checkpoint verification at 25 % and 75 % frame positions
  - **50 % tiebreaker fires ONLY on soccer/screen disagreement**, not for timestamp validation (vision-spec §2, load-bearing — extending tiebreaker to timestamp would silently loosen acceptance)
  - Rubric tightened per video-dedup Q3 (celebrations narrowed to "on-field player celebrations following a goal"; screen expanded to catch software screen recording — vision-spec §5 flags this as a known gap in Python)
  - **Three-state timestamp classification: verified / unverified / rejected** (vision-spec §7-§8, load-bearing distinction). Rejected = discard; unverified = keep in corpus but rank lower; verified = full confidence
  - `is_valid = is_soccer AND NOT is_screen_recording AND timestamp_status != "rejected"` (vision-spec §8)
  - LLM concurrency semaphore pinned to 2 per worker (matches joi's parallel-cap)
  - Typed error taxonomy: `LLMUnavailableError` / `LLMTimeoutError` / `LLMValidationError` (vision-spec §9)
  - `added` field captured but not summed into absolute minute; `stoppage_clock` IS summed (vision-spec §6, preserve both fields for future OCR-quality improvements)
- **V/c — Perceptual hash + batch perceptual dedup** [~1.5 days]
  - dHash (9x8 grayscale + histogram equalization) at 0.25 s frame intervals — preserve Python's algorithm verbatim (video-spec §5)
  - Storage format: `"dense:0.25:t1=h1,..."` for cutover compat with Python's on-disk hash strings; parse once into typed `[]struct{ts float32; hash uint64}` in memory for 10× faster matching (video-spec §5 REMARKS)
  - **Single ffmpeg invocation with select filter to stream all frames**, not one subprocess per frame (video-spec §5 REMARKS, ~100× subprocess overhead reduction over Python)
  - Heartbeat before every ffmpeg call (video-spec §5 + §8, load-bearing under contention)
  - `_dense_hashes_match` sliding-window offset-tolerant matcher (`max_hamming=10`, `min_consecutive=3`, timestamp tolerance = interval/2)
  - **Index `frames_b` by rounded timestamp for O(1) tolerance lookup** (video-spec §6 REMARKS, drops worst-case O(N²·M²))
  - Empty-perceptual-hash on either side treated as no-signal, pair NOT collapsed (video-spec §7)
  - Batch dedup + S3 perceptual lookup (all-pairs against corpus)
  - **Category-scoped: verified pool vs unverified pool ONLY** (upload-spec §3, load-bearing — prevents verified-goal replacement by unverified-clip-of-different-moment)
  - `HASH_VERSION` field stored per-video for future algorithm swap (video-spec §9)

Buffer:

- Vision call #2 quality comparison (V/f) can start end-of-week if V/a–V/c ship ahead

**Milestone gate:** given a tweet URL, run end-to-end through Stage 8 (all pipeline stages except final upload). Discovery emits `event.stable`, spawns Video, Video emits its checklist row → this is verifiable via pg + NATS observation.

**Risks:**

- **AI vision throughput on joi** — Qwen3-VL-8B is 2-concurrent-cap; dual-checkpoint doubles calls. Might need to accept slower per-clip validation OR bump concurrency + trust it not to OOM.
- **Perceptual match false positives on cutover** — Python's threshold is empirically tuned against Python-encoded clips; Go-side ffmpeg + dHash implementation may produce slightly different signatures. Real corpus calibration during V/h backfill week.

### Week 3 — Asset pipeline + downstream chain + prod-ready (2026-08-01 → 2026-08-08)

**Goal by Friday:** end-to-end from tweet → video_shares row visible in pg. Prod-shaped Go worker running against real match-day data in dev.

Critical path:

- **AssetWorkflow (per-event signal-based FIFO)** [~1.5 days]
  - Signal-with-start pattern, deterministic workflow ID `asset-{event_id}` per event
  - `add_batch` signal from Video workflows
  - **Queue-drain completion**: expected N batches seen AND queue empty → exit (fixes Python's 5-min tail waste per upload-spec §10)
  - **`ALLOW_DUPLICATE` reuse policy** (upload-spec §1, load-bearing) so late-arriving signals after a Completed workflow start a fresh instance rather than silently drop
  - Empty-batch signals suppressed at the sending activity (upload-spec §1)
  - Fresh S3 state fetched INSIDE the serialized workflow per-batch, not per-workflow (upload-spec §2, load-bearing for race-free dedup)
  - Fixed per-batch order: fetch state → MD5 dedup → popularity bump → split MD5 replacements from perceptual → verified/unverified perceptual dedup in parallel → popularity bumps → uploads → share inserts → frontend notify → cleanup INDIVIDUAL files (NOT the temp dir, per upload-spec §11)
  - **`dedup-failure-skips-batch` invariant** (upload-spec §11) — perceptual dedup failure zeros both upload/replace lists; do NOT fall back to "upload everything" (regresses duplicate-upload bug)
  - **VAR event-removal hard-terminate** (upload-spec §9) — `fetch_event_data` returns `event_not_found` → workflow completes immediately, discards remaining queued batches
  - Hard-cap timeout (~30 min) as safety net for crashed downstream workflows
  - **No rank recalc activity** — ranks are derived at read time per [decisions.md 2026-07-18](../decisions.md). AssetWorkflow does NOT store a rank column or fire `event.rank_recalculated` during normal flow.
  - **Parallel batch processing via `workflow.Go`** (2026-07-18 design discussion) — within-batch dedup + S3 upload can run concurrently across signal-received batches. pg's `INSERT ... ON CONFLICT (content_hash) DO NOTHING RETURNING id` provides the atomic dedup+upload primitive; popularity via `COUNT(video_shares)` handles multi-source correctly regardless of ordering. Python's per-batch serialization was a Mongo-forced constraint that pg lifts.
  - **Completion condition via pg queue query** (2026-07-18 design discussion) — Discovery pre-inserts `event_download_workflows` rows for all N expected downloads. AssetWorkflow polls: when `count(completed_at IS NOT NULL) == expected AND queue empty AND no in-flight goroutines`, drain done. Deterministic + failure-resilient + no idle-timeout waste.
- **Downstream spawn rule for Discovery → Video → Asset chain** [~1 day]
  - Discovery activity spawns Video via Temporal client + inserts Video's `event_downstream_workflows` row atomically
  - Video's finalize activity spawns Asset via signal-with-start
  - Every stage owns its row + successor spawn per the 2026-07-16 decision
- **V/g — Upload + video_assets + video_shares + replace-and-absorb** [~2 days]
  - Optimistic `INSERT ... ON CONFLICT (content_hash) DO NOTHING RETURNING id` for cross-event race safety
  - S3 upload only on race winner
  - **Same S3 key reused on replacement** (upload-spec §7) — URL stability preserves shared consumer links; MongoDB→pg positional in-place update, NEVER remove-then-add
  - **Popularity carried into replacement candidate BEFORE the S3 PUT** (upload-spec §6, load-bearing — remove-then-add would zero the counter)
  - `UPDATE video_shares SET video_asset_id` for share migration; delete old asset row + S3 object
  - Popularity via `COUNT(video_shares)` — no counter to maintain
  - **Duration-vs-resolution winner picking**: 15 % `DURATION_SIMILARITY_THRESHOLD` — within 15 % → prefer higher resolution; more than 15 % → prefer longer (upload-spec §4)
  - **Should-replace-S3 same 15 % rule** with reason string threaded into logs for Grafana debugging (upload-spec §5, load-bearing)
  - `bump_video_popularity` idempotent write path
- **V/f — Vision call #2 quality comparison** [~half day]
  - One representative frame per clip (from ~50 % timestamp)
  - Multi-image call to Qwen3-VL-8B; ranked JSON output with per-clip score + reasoning
  - Fires only when 2+ candidates survive perceptual clustering to Stage 8
- **T/f + T/g — CDN split + browser-crash watchdog** [~1 day]
  - Split `/download_video` into `/extract_cdn_url` (used by V/a) + external downloader
  - Watchdog: `page.Ping` every N seconds, on failure relaunch context + reload cookies + one retry

Buffer:

- Alias table seed (team_aliases rows for tracked leagues) — half day
- ~~`event.rank_recalculated` emit from Asset's ranking recompute~~ — dropped per [decisions.md 2026-07-18 derived-at-read ranking](../decisions.md)
- Backfill script skeleton (V/h) if time permits

**Milestone gate:** on Sunday match day (Aug 3 in the middle of Week 3), rehearse: point Go worker at active fixtures, observe end-to-end pipeline, compare video clips output against Python's prod output. Any regressions logged as Week 4 fix tasks.

**Risks:**

- **AssetWorkflow signal ordering under load** — Temporal signals are FIFO but race conditions between signal-with-start and heartbeat interleaving can produce out-of-order effects. Test hard with multiple simultaneous fixtures.
- **Replace + absorb race with concurrent Discovery** — two events discovering the same clip simultaneously must both land at the same asset row. `ON CONFLICT DO NOTHING` handles it but validate under real match-day load.

### Week 4 — Consumer surface + cutover (2026-08-08 → 2026-08-14)

**Goal by Friday:** Go worker in prod, Python worker off, vedanta-systems consuming from Go's surface. Friday is buffer + cutover verification; Saturday is last-chance-Sunday-morning of the season eve.

Critical path:

- **`cmd/api` SSE bridge + REST surface** [~2 days]
  - HTTP surface exposing `/api/v1/fixtures`, `/api/v1/events`, `/api/v1/events/{id}/shares`, `/api/v1/dates`, `/api/v1/search`, SSE at `/api/v1/stream`
  - **Preserve exact SSE envelope schema** — `{type, id, ts, data}` with types `connected`, `invalidate`, `heartbeat`, `health` (scaler-spec §B13, copy-paste contract with vedanta-systems)
  - `redirect_slashes=False` (scaler-spec §Preserve)
  - Bearer token auth checked at Caddy edge; `X-Internal-Token` for `/api/v1/internal/notify`
  - SSE bridge subscribes to NATS `event.>` + `fixture.>` subjects, forwards to browser clients via `EventSource` envelopes
  - **`X-Accel-Buffering: no` header on the SSE response** (scaler-spec §B12, load-bearing hint to Caddy)
  - Bounded queue per connection (`maxsize=100`), slow-client policy = drop connection, EventSource reconnects and catches up via REST
  - Idle heartbeat every 30 s + `ping=15` TCP keepalive comments
  - `Last-Event-ID` cursor for reconnect backfill from `event_log` (improvement over Python which returns REST-only replay)
  - Video URL rewrite at boundary: stored `/video/<bucket>/<key>` → public `/api/v1/videos/<bucket>/<key>` (scaler-spec §B10)
  - Server-side status derivation for `/events/{id}` (watching / extracting / complete / validating)
  - **Ranks derived at query time via `ROW_NUMBER() OVER (PARTITION BY event_id ORDER BY verified DESC, popularity DESC, quality_score DESC NULLS LAST)`** (per [decisions.md 2026-07-18](../decisions.md)). No stored rank column, no rank recalc anywhere. Every SSE-triggered client re-fetch sees fresh ordering.
  - CORS explicit origin (not `*`)
- **`notify_frontend_refresh` sites — 5 workflows** [in-line during workflow work above]
  - Ingest, Monitor, Discovery, Video, Asset workflows each fire on state-mutation
  - **Dual-publish during migration**: (a) legacy vedanta-systems Express `POST /api/found-footy/refresh` coarse trigger, (b) new FastAPI-equivalent `POST /api/v1/internal/notify` typed envelope (scaler-spec §B13, load-bearing during migration)
- **Webhook delivery worker** [~1 day]
  - JetStream durable consumer subscribing to same subjects
  - Registered endpoints get HMAC-signed POSTs with retry + dead-letter
- **vedanta-systems integration** [~half day; runs parallel]
  - Point vedanta-systems' Express BFF at the new Go `/api/v1/*` surface
  - Test the goal-clip surfacing UI against real Go output
- **Backfill script V/h (streamlined)** [~1 day]
  - Enumerate current Python-era S3 assets
  - Compute SHA256 + perceptual hash + `verified` flag via existing vision path
  - Insert `video_assets` row per unique content hash
  - Run in dev against dev's shared S3 corpus first, then prod during cutover window
- **Dual-run + cutover dry-run** [~1 day, Wednesday–Thursday]
  - Run Go worker + Python worker simultaneously in dev, verify no fixture double-processing
  - Diff output: same fixtures selected, same events detected, same clips surfaced within ±10 % (some fuzz expected)
  - Rollback plan documented: revert docker-compose.prod.yml to Python image, redeploy, resume — must be < 5 min
- **Cutover — Friday** [~half day]
  - Stop Python worker
  - Start Go worker
  - Observe first hour for regressions
  - Rollback trigger: any of (a) prod fixtures ingested < 90 % of expected, (b) event detection drops > 20 % from Python baseline, (c) any un-recoverable error class exceeds 5 % of workflow runs

Buffer / late-breaking:

- Documentation updates (living docs kept current-ish; final polish here)
- Grafana panels for the Go pipeline (Python-era ones keep working through cutover)
- Handle any late-breaking Twitter DOM changes (X ships updates)

**Milestone gate:** Aug 14 end of day — Go worker in prod, Python stopped, vedanta-systems live against new surface, first-hour observability shows no regressions. If gate fails, roll back Python by 10 PM UTC Aug 14.

**Risks:**

- **Cookie freshness at cutover** — Python has been keeping cookies warm via active scraping; if Go's cookie refresh loop has bugs, we could hit auth expiry mid-season with no VNC handy.
- **vedanta-systems integration surprises** — depends on what the Express BFF actually expects. If the shape doesn't match, we shim in the SSE bridge.

---

## Cross-cutting workstreams (in parallel with weekly work)

### Team alias seeding

- Populate `team_aliases` for ~100 tracked-league teams (top-5 European + FIFA international squads we care about)
- Manual curation: primary name + 3–5 aliases per team (English + native + common nicknames)
- Half-day of work, can happen any week; ideally end of Week 2 so Discovery has aliases when video pipeline goes live
- **NOT reintroducing the Python-era Wikidata + LLM RAG** — user's call, docs/rebuild/proposals/video-dedup.md notes this

### Ongoing observability

- Every new phase gets Prometheus metrics via the shared substrate (already the pattern)
- Loki queries via `docker logs found-footy-{env}-worker-*` remain the primary debugging surface
- Grafana panel work formally deferred to post-cutover

### Living docs

- Every phase update includes `docs/rebuild/<topic>.md` update in the same commit (per AGENTS.md working discipline)
- `docs/decisions.md` gets an entry when architecture changes
- Proposal docs (discovery.md, twitter-port.md, video-dedup.md) get "shipped" annotations as phases land

---

## Rollback + safety

- **Git branch:** all work stays on `rebuild/go`. `main` still runs Python.
- **Docker image tagging:** every prod-bound image gets tagged with `rebuild-go-<sha>` so rollback is one-liner `docker compose -f docker-compose.prod.yml pull && docker compose up -d`.
- **Data separation:** Go writes to pg (fresh schema); Python still uses MongoDB. Cutover is docker-compose swap, not a data migration. Backfill (V/h) is one-shot at cutover; if it fails midway, `.env` swap back to Python is 60 sec.
- **DNS / Caddy:** Caddy routes `vedanta.systems` traffic to whichever worker is currently up. Cutover is `docker-compose.prod.yml` service swap; Caddy detects via Docker network.
- **Cookies:** shared `/config/twitter_cookies.json` bind-mount survives worker swap. Cookies stay warm because Twitter service auto-refreshes on active scraping.

---

## Out of scope for Aug 15 (genuinely deferred — user-approved)

Only genuinely-out-of-scope items go here. Every "could-cut-if-we-slip" item from an earlier draft was pulled back into the weekly plan per the 2026-07-18 no-shortcuts directive.

1. **LSH-style indexing** for perceptual corpus lookup — all-pairs is fine at our current size (~thousands of assets); revisit when the corpus grows past ~10 k.
2. **Perceptual hash storage format upgrade from text to binary** — Python-format text works and is what the migration bridges against; binary is a post-cutover perf tweak.
3. **Deeper stealth options #5–8** from video-dedup catalog (per-container fingerprints, mobile.twitter.com path, residential proxy pool, full behavior simulation) — baseline #1–4 ship pre-cutover; escalate to #5–8 only if empirical detection surfaces post-cutover.
4. **Multiple Twitter accounts** — multi-account fraud detection risk >> isolation benefit for a read-only service (per twitter-port.md Q4).
5. **Wikidata + LLM alias RAG** — permanently replaced by a curated `team_aliases` table + Twitter advanced-search OR-syntax (user's 2026-07-17 call). Not "deferred" — dead.
6. **Scenario YAML additions for O3/b's 5 emissions** — code is correct + inert-safe; scenario harness update fits into V/i cross-cutting week if there's time, else post-cutover cleanup.
7. **Pre-existing enum-casing scenario test failures** — unrelated to the rebuild; independent bug ticket.
8. **Full Grafana dashboard build-out for the Go pipeline** — Prometheus + Loki writes land during weeks 1–4; dashboard authoring is post-cutover polish. Python-era dashboards keep working through cutover because Go writes to the same substrates.

In-scope items called out explicitly (no cuts):

- **V/h full Python-era corpus backfill** — was previously flagged as "maybe October." Now in Week 4 as full scope. Runs in one shot during cutover window; we do NOT ship with a half-backfilled corpus.
- **Vision call #2 quality comparison** — was previously flagged as "post-season addition." Now in Week 3 as full scope.
- **Structured error taxonomy** — was previously flagged as "ship with 503/500 catchall like Python." Now in scope everywhere the Python spec cites a typed error (Twitter downloads, LLM validation, workflow retries).
- **Baseline stealth #1–4 all four** — Playwright defaults + timing jitter + header rotation + scroll pauses. All ship pre-cutover.
- **`notify_frontend_refresh` dual-publish** — legacy Express + new FastAPI both hit until vedanta-systems Express migration is verified in prod.

---

## Success criteria for Aug 15

Match-day (Aug 15 evening kickoffs):

1. Go worker processing all La Liga fixtures within the tracked-team roster.
2. Fixture staging → active transition observed for every match.
3. Goals detected via Monitor, `event.stable` emitted, Discovery workflows spawned.
4. Twitter searches return non-zero tweet results within 60 s of goal detection.
5. Videos downloaded, validated, hashed, deduped, uploaded.
6. `video_shares` visible in pg with proper ranks + popularity counts.
7. vedanta-systems surfaces surviving clips to the frontend within 5 min of the goal.
8. No error class exceeds 5 % of workflow runs.
9. No regression in clip surfacing rate compared to Python baseline (± 10 % noise floor acceptable).
10. No user complaints during the first match window.

If (1) fails: total blocker, rollback immediately.
If (2)–(4) fail intermittently: investigate + patch; not necessarily rollback.
If (5)–(7) fail systematically: rollback to Python, debug post-mortem.
If (8) fires: page + iterate.
If (9) shows > 10 % regression: rollback, investigate.

---

## Living document — updated as we go

Check in weekly, update the "where we are" section, cross off shipped items, add newly-discovered work to the deferred list.

Last updated: 2026-07-18 (initial draft + spec integration + no-shortcuts scope stance).

Detailed Python behavior specs merged into `python-functional-spec.md` on 2026-07-18 as an addendum; every week's checklist above is now grounded in those specs. Line-number references in each week's bullets point at Python source; the addendum has PURPOSE + BEHAVIOR + REMARKS per subsystem.
