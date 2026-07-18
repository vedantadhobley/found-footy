# found-footy Go rebuild — roadmap to La Liga (2026-08-15)

**Target:** La Liga season starts **Friday 2026-08-15**. Go rebuild must be at feature-parity with the Python system, running in prod, replacing Python, by first La Liga match kickoff.

**Runway:** 4 weeks (2026-07-18 through 2026-08-14). Buffer weekend before season start intentionally kept clear for late-breaking fixes + cutover verification.

**Philosophy:** feature parity means the user experience matches or exceeds Python's — same clip discovery quality, same or better latency, same or better rank ordering. Internally the rewrite is dramatically cleaner (typed enums, structured concurrency, dual-write observability, atomic spawn semantics) but that's not user-visible; parity is measured by output.

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
  - VNC container (Xvfb + x11vnc + websockify + raw Firefox)
  - `cookies.sqlite` watcher that captures `auth_token` on login + writes shared `/config/twitter_cookies.json`
  - Cookie expiry monitor loop + NATS `twitter.auth_expired` emission on expiry
  - Cookie reload on NATS `twitter.reauthed` subscription (headless fleet)
  - Docker compose: dev + prod stacks pick up the split (VNC + N headless)
- **T/c — Full search + scrape** [~2 days]
  - Port `scrape.py`'s DOM extraction helpers (tweet URL, age, video duration, promoted-tweet skip, snowflake sanity check)
  - Time-based scroll termination (`max_age_minutes` boundary)
  - `max_scrolls = 10` safety cap (from Python's `_do_search`)
  - Empty-page stop after ≥1 scroll
  - **Consecutive-already-seen early-stop (3 default) — improvement over Python** (per video-dedup Q2)
  - `exclude_urls` filter honored per-tweet
  - Baseline stealth #2 (timing jitter ±20–40 s on Discovery's search cadence)
  - Baseline stealth #4 (random 0.5–3 s scroll pauses)
- **T/d — Rate-limit detection + backoff** [~half day]
  - 429 detection + "Are you a robot?" interstitial detection
  - Per-instance `backoff_until` written to `twitter_instances` pg table
  - `/status` surfaces backoff state; Discovery-side router skips backed-off instances
- **T/e — Instance registry + heartbeat** [~half day]
  - `twitter_instances` pg schema (see twitter-port.md Q5)
  - 10 s heartbeat goroutine
  - `ORDER BY RANDOM()` router in Discovery (per twitter-port.md Q6)

Buffer work if week runs ahead:

- T/g browser-crash watchdog (nice-to-have; can slip to week 2)
- T/h adapter update to match new API (mostly done; small polish)
- Baseline stealth #3 header rotation (defer if pressed)

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
  - Structured JSON output: 5 fields (soccer, screen, clock, added, stoppage_clock)
  - Dual-checkpoint verification at 25 % and 75 % frame positions
  - Rubric tightened per video-dedup Q3 (celebrations narrowed to "on-field player celebrations following a goal"; screen expanded to catch software screen recording)
  - `verified` bool derived + wrong-clock hard-reject
- **V/c — Perceptual hash + batch perceptual dedup** [~1.5 days]
  - dHash (9x8 grayscale + histogram equalization) at 0.25 s frame intervals — preserve Python's algorithm verbatim
  - Storage format: `"dense:0.25:t1=h1,..."` for cutover compat with Python's on-disk hash strings
  - `_dense_hashes_match` sliding-window offset-tolerant matcher (`max_hamming=10`, `min_consecutive=3`)
  - Batch dedup + S3 perceptual lookup (all-pairs against corpus; indexing deferred)
  - Category-scoped (verified pool vs unverified pool only)

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
  - Signal-with-start pattern, deterministic workflow ID per event
  - `add_batch` signal from Video workflows
  - **Queue-drain completion**: expected N batches seen AND queue empty → exit (fixes Python's 5-min tail waste)
  - Hard-cap timeout (~30 min) as safety net for crashed DownloadWorkflows
- **Downstream spawn rule for Discovery → Video → Asset chain** [~1 day]
  - Discovery activity spawns Video via Temporal client + inserts Video's `event_downstream_workflows` row atomically
  - Video's finalize activity spawns Asset via signal-with-start
  - Every stage owns its row + successor spawn per the 2026-07-16 decision
- **V/g — Upload + video_assets + video_shares + replace-and-absorb** [~2 days]
  - Optimistic `INSERT ... ON CONFLICT (content_hash) DO NOTHING RETURNING id` for cross-event race safety
  - S3 upload only on race winner
  - `UPDATE video_shares SET video_asset_id` for replace + absorb; delete old asset + S3 object
  - Popularity via `COUNT(video_shares)` — no counter to maintain
- **V/f — Vision call #2 quality comparison** [~half day]
  - One representative frame per clip (from ~50 % timestamp)
  - Multi-image call to Qwen3-VL-8B; ranked JSON output with per-clip score + reasoning
  - Fires only when 2+ candidates survive perceptual clustering to Stage 8
- **T/f + T/g — CDN split + browser-crash watchdog** [~1 day]
  - Split `/download_video` into `/extract_cdn_url` (used by V/a) + external downloader
  - Watchdog: `page.Ping` every N seconds, on failure relaunch context + reload cookies + one retry

Buffer:

- Alias table seed (team_aliases rows for tracked leagues) — half day
- `event.rank_recalculated` emit from Asset's ranking recompute — small
- Backfill script skeleton (V/h) if time permits

**Milestone gate:** on Sunday match day (Aug 3 in the middle of Week 3), rehearse: point Go worker at active fixtures, observe end-to-end pipeline, compare video clips output against Python's prod output. Any regressions logged as Week 4 fix tasks.

**Risks:**

- **AssetWorkflow signal ordering under load** — Temporal signals are FIFO but race conditions between signal-with-start and heartbeat interleaving can produce out-of-order effects. Test hard with multiple simultaneous fixtures.
- **Replace + absorb race with concurrent Discovery** — two events discovering the same clip simultaneously must both land at the same asset row. `ON CONFLICT DO NOTHING` handles it but validate under real match-day load.

### Week 4 — Consumer surface + cutover (2026-08-08 → 2026-08-14)

**Goal by Friday:** Go worker in prod, Python worker off, vedanta-systems consuming from Go's surface. Friday is buffer + cutover verification; Saturday is last-chance-Sunday-morning of the season eve.

Critical path:

- **`cmd/api` SSE bridge + REST surface** [~2 days]
  - HTTP surface exposing `/api/v1/fixtures`, `/api/v1/events`, `/api/v1/events/{id}/shares`, SSE at `/api/v1/stream`
  - Bearer token auth checked at Caddy edge
  - SSE bridge subscribes to NATS `event.>` + `fixture.>` subjects, forwards to browser clients
  - SSE reconnect uses `event_log_id` cursor for backfill from pg
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

## Deferred to post-2026-08-15 (post-season-start)

Explicitly named so we don't second-guess these during the 4 weeks:

1. Full V/h Python-era corpus backfill (perceptual + verified for every historical clip). Doing enough during Week 4 to avoid immediate duplicate S3 explosion; full backfill can happen during Oct international break.
2. Structured error taxonomy refinement (Twitter service ships with 503/500 catchall like Python's today).
3. Grafana dashboards for the Go pipeline (Python-era dashboards keep working; Go writes to same Loki + Prometheus).
4. Scenario YAML additions for O3/b's 5 emissions.
5. Enum-casing scenario test failures.
6. LSH-style indexing for perceptual corpus lookup (all-pairs is fine at our size for now).
7. Perceptual hash storage format upgrade from text to binary (Python-format text still works; binary is a post-cutover perf tweak).
8. Baseline stealth #3 header rotation (User-Agent + Accept-Language variation).
9. Deeper stealth options #5–8 from video-dedup catalog (per-container fingerprints, mobile.twitter.com path, residential proxy pool, behavior simulation).
10. Multiple Twitter accounts (multi-account fraud detection risk >> isolation benefit).
11. Alias RAG replacement — simple table lookup permanently replaces the Python-era Wikidata + LLM pipeline.

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

Last updated: 2026-07-18 (initial draft, agents currently producing detailed Python behavior specs to be merged into python-functional-spec.md).
