# Scaler, registry, and consumer surface — Python behavior spec

Frozen WHAT-and-WHY detail from the
[Python functional-spec index](./README.md).

## Scaler / Registry + Consumer surface — Python behavior spec (WHAT + WHY)

Files referenced: `archive/src/scaler/{registry.py, scaler_service.py}`; `archive/api/`; `archive/deploy/INFRA-NOTES.md`.

### SECTION A — Scaler + Twitter Instance Registry

### A1. TwitterRegistry singleton (Mongo-backed)

**PURPOSE**: Give Twitter scraper containers a way to advertise "I exist and I'm healthy" into a shared Mongo collection so other services can discover them dynamically.

**BEHAVIOR**
- `TwitterRegistry` is a thread-locked singleton (`registry.py:29-39`); one process gets one instance.
- Backing store is `db.twitter_instances` in MongoDB, lazy-loaded via `_get_store()` (`registry.py:55-60`).
- `register(instance_id, url)` upserts `{instance_id, url, status:"available", last_heartbeat, registered_at}` (`registry.py:62-86`).
- `heartbeat(instance_id)` bumps `last_heartbeat` only; sent every 10 s from a daemon thread inside each Twitter container (`twitter/app.py:53-62`).
- `unregister(instance_id)` flips `status="unavailable"` on shutdown; the row is not deleted (`registry.py:88-100`).

**REMARKS**: Instance identity is `TWITTER_INSTANCE_ID` env, falling back to `socket.gethostname()`. The URL each instance publishes is `http://$HOSTNAME:$TWITTER_SERVICE_PORT` — container name, port 8888. Perfect fit for a pg-backed row-per-instance table in the rebuild.

### A2. Load-balancing strategies exposed

**PURPOSE**: Let callers pick one healthy Twitter URL per request.

**BEHAVIOR**
- `get_instance_url(strategy)` supports `round_robin` (default), `random`, `first` (`registry.py:155-181`).
- Round-robin increments `_round_robin_index` under the class lock — safe across threads within one process, but each process has its own counter (no global fairness across worker containers).
- Single-instance short-circuit at `registry.py:170` returns the sole URL without consulting `strategy`.
- Public convenience wrapper `get_twitter_url()` calls the singleton with the default strategy (`registry.py:232-234`).

**REMARKS**: Round-robin was chosen because Twitter searches are near-uniform in cost and a random distribution was measurably lumpier under low instance counts. Preserve the "single-instance short-circuit" — it's the dev-mode ergonomics knob.

### A3. Staleness cutoff (30 s default)

**PURPOSE**: Automatically remove a Twitter instance from the routing pool if it stopped heart-beating.

**BEHAVIOR**
- `get_available_instances(max_age_seconds=30)` filters on `status="available" AND last_heartbeat >= now - 30s` (`registry.py:115-153`).
- Stale rows are never returned but are NOT deleted or status-changed by the reader — they just fall out of the filter until they heartbeat again.
- On any Mongo error the last cached list is returned; if none exists, the singleton falls back to `TWITTER_SESSION_URL` (default `http://found-footy-prod-twitter:8888`).
- Heartbeat cadence is 10 s, so the cutoff gives ~3 missed heartbeats before eviction.

**REMARKS**: The rebuild should preserve the "eviction is passive" property — writing to a stale row on read would create contention.

### A4. mark_instance_busy / mark_instance_available — an UNUSED hook

**PURPOSE**: Nominally, let callers signal a Twitter instance is currently occupied so routing skips it.

**BEHAVIOR**
- `mark_instance_busy(instance_id)` and `mark_instance_available(instance_id)` flip the `status` field between `"busy"` and `"available"` (`registry.py:183-203`).
- **Nothing in the archive calls these two methods.** Grep across `archive/` returns zero hits outside the definitions themselves.
- Worse: `get_available_instances` filters on `status="available"`, so if anything ever DID call `mark_instance_busy`, the routing query would immediately drop that URL until the same caller flipped it back. There is no watchdog to reset a stuck "busy" row — a crash mid-search would strand an instance out of rotation.
- The routing query the actual worker uses (`src/activities/twitter.py:283-344`) doesn't call the registry AT ALL — it independently probes `http://found-footy-prod-twitter-{1..8}:8888/health` on a 30 s cache and round-robins the healthy set. So the registry is *populated* but *not consulted* by the search path.

**REMARKS**: This is a real bug / dead code. The rebuild's pg-backed instance registry should either wire routing through it end-to-end or drop the busy/available API and stick to heartbeat-only liveness.

### A5. Scaler service — separate binary, docker-compose driver

**PURPOSE**: Watch load signals and scale worker + twitter container counts up/down.

**BEHAVIOR**
- Separate process, entry point `python -m src.scaler.scaler_service` (`scaler_service.py:570-571`), packaged as its own prod-only container.
- Loop cadence: 30 s (`CHECK_INTERVAL`, `scaler_service.py:50`). 60 s cooldown between actions (`SCALE_COOLDOWN`).
- **Worker signal**: count of RUNNING Temporal workflows via `ListWorkflowExecutions` (`scaler_service.py:107-138`). Scale up if `active_workflows/current_workers > 5`, scale down if `< 2` (`calculate_target_workers`, `scaler_service.py:325-351`). Min 2, max 8.
- **Twitter signal**: Mongo aggregation counting events with `_monitor_complete=true AND _download_complete not true` across `fixtures_active` + `fixtures_live` (`get_active_twitter_goals`, `scaler_service.py:148-179`). Each instance handles `TWITTER_GOALS_PER_INSTANCE=2` goals. Scale up if goals/instance > 2; scale down if `active_goals < instances`. Min 2, max 8.
- Scaling method: `python_on_whales` DockerClient invokes `docker compose up --scale <service>=<n>` in-process (`scaler_service.py:280-311`); idempotent — no-op if already at target.
- Emits state-change or heartbeat log every 30 s with a full metric snapshot including `total_goals`, `todays_goals`, `total_videos` (from a second Mongo aggregation, `get_goals_summary`).

**REMARKS**: The scaler owns compose-side scaling, not the workers themselves. Rebuild target is to move signals off Mongo aggregations onto pg queries and preserve the "two independent load signals" split — workers scale on active workflow count, twitter scales on active goal count. Don't merge them.

### A6. Fallback URL when no instances

**PURPOSE**: Keep the worker from crashing during a cold-boot window.

**BEHAVIOR**
- If `get_available_instances()` returns empty (no rows or all stale), `get_instance_url()` logs a warning and returns `TWITTER_SESSION_URL` (`registry.py:166-168`).
- Env var defaults to `http://found-footy-prod-twitter:8888` — a legacy singular container name that doesn't exist in prod anymore (`registry.py:53`).
- A second, separate fallback lives in `get_healthy_twitter_urls` (`scaler_service.py:516-552`): if no `twitter-{1..8}` responds to `/health`, returns the first two URLs blindly.
- The worker's Twitter activity has a third, independent fallback: probes `twitter-{1..8}` and falls back to the first two if none respond (`activities/twitter.py:322-328`).

**REMARKS**: Three independent fallback paths, one dead default hostname. The rebuild should have exactly one.

### A7. Cache TTL (5 s local cache)

**PURPOSE**: Keep the Mongo query off the hot path when many searches fire in the same second.

**BEHAVIOR**
- `_local_cache` + `_cache_time` in the singleton; `_cache_ttl=5s` (`registry.py:47-50`).
- Refresh path: on cache miss (age > 5 s OR cache empty), query Mongo and repopulate.
- Logs `instance_cache_refreshed` only when the returned URL count differs from the cached count — quiet during steady state, noisy during scale events.
- On Mongo error the cache is returned even if stale; the singleton NEVER re-raises to callers.

**REMARKS**: The parallel cache in the worker's own routing path (`activities/twitter.py:288`, 30 s TTL) exists because the registry is not consulted. Rebuild collapses these into one.

---

### SECTION B — Consumer Surface

**Overall shape**: The archive contains a FastAPI app at `archive/api/` mounted at `/api/v1`, but it is a *migration-in-progress parallel surface*, not the primary consumer contract. The primary contract the vedanta-systems frontend actually hits today is the Express BFF inside the vedanta-systems repo (not in this tree). The worker dual-publishes to both.

### B8. Surfaces that exist

**PURPOSE**: Give the frontend fixture/event data and a real-time invalidation signal.

**BEHAVIOR**
- FastAPI app in `archive/api/app.py`, `uvicorn api.app:app --host 0.0.0.0 --port 8080` (`app.py:7`).
- Routers under `/api/v1`: `health`, `fixtures`, `events`, `search`, `stream`, `internal` (`app.py:56-63`).
- REST reads: `GET /dates`, `GET /fixtures?date=YYYY-MM-DD`, `GET /fixtures/{id}`, `GET /events/{event_id}`, `GET /search?q=` (`routers/fixtures.py`, `events.py`, `search.py`).
- SSE at `GET /stream` (`routers/stream.py`).
- Webhook-in: `POST /internal/notify` (worker → API broadcast trigger, `routers/internal.py`).
- No webhook-out.

### B9. Auth pattern

**BEHAVIOR**
- Public REST + SSE: **unauthenticated**. CORS defaults to `*` (`settings.py:28-30`) with a "prod should set this explicitly" comment.
- `/api/v1/internal/*` gated by an `X-Internal-Token` header matching `INTERNAL_TOKEN` env (`deps.py:41-52`). Empty token = no-op auth, relying on docker-network isolation (`internal.py:6-11`).
- No session cookies, no bearer tokens on the read paths, no IP allowlist.

### B10. Data shapes

**BEHAVIOR**
- `/fixtures` returns three flat arrays: `staging`, `active`, `completed` — projected via `_FIXTURE_PROJECTION` (`fixtures.py:28-61`), covering fixture identity, league, teams, goals, and per-event fields `_monitor_complete`, `_download_complete`, `_s3_urls`, `_s3_videos`, `_first_seen`, `_telemetry`.
- Video URLs are rewritten at the boundary: stored `/video/<bucket>/<key>` becomes public `/api/v1/videos/<bucket>/<key>` (`fixtures.py:64-70`) — decouples clients from the storage layout. The actual video-serving handler is not in the archive, so this rewrite currently points at a not-yet-implemented endpoint.
- `/events/{id}` returns `{event_id, found, date, fixture_id, collection, status}` where `status` is derived server-side from the raw flags ("watching" | "extracting" | "complete" | "validating", `events.py:26-57`).
- `/search` regex-matches on team + player + assist names across all three collections, groups by date, tags each fixture with `_search.{team_match, matched_event_ids, match_count}`.

### B11. Where the vedanta-systems frontend actually hits

**BEHAVIOR**
- Not this FastAPI directly. Per `deploy/INFRA-NOTES.md:9`: "found-footy has no public-facing component (no Cloudflare tunnel ingress)".
- The frontend hits the vedanta-systems Express BFF (`vedanta-systems-prod-api:3001` per `deploy/INFRA-NOTES.md:61`), which proxies to found-footy's Mongo directly today.
- The FastAPI is a same-tailnet-only service reachable at `found-footy-{env}-api.{$BASE_DOMAIN}` via Caddy (compose-file comment at `docker-compose.dev.yml:210`), staged for a future cutover.

### B12. Caddy / reverse-proxy setup

**BEHAVIOR**
- Reference Caddyfile block in `archive/deploy/INFRA-NOTES.md:24-37` — routes only Temporal-UI, Mongo-Express, MinIO, and Twitter-VNC. **No route to the FastAPI is in the archive's INFRA-NOTES** despite the compose comment claiming one should exist.
- `X-Accel-Buffering: no` header set on the SSE response (`stream.py:57`) — the load-bearing hint to Caddy not to buffer text/event-stream.
- CORS `expose_headers` includes `Content-Range` / `Accept-Ranges` / `Content-Length` (`app.py:50`) — signals the eventual video-serving endpoint will support HTTP range requests.

### B13. Real-time vs polling

**PURPOSE**: Push a "something changed, refetch" signal so the frontend doesn't poll.

**BEHAVIOR**
- SSE, not polling. `EventSourceResponse` from `sse-starlette` with typed envelopes (`stream.py`).
- Envelope schema (`envelope.py:19-38`): `{type, id, ts, data}`. Types: `connected` (once per open), `invalidate` (data changed), `heartbeat` (idle keepalive), `health`.
- Trigger path: worker's `notify_frontend_refresh` activity (`activities/monitor.py:765-849`) is called from every workflow that mutates fixture/event state (ingest, monitor, twitter, upload workflows — 5 sites) and **dual-publishes** to (a) legacy vedanta-systems Express `POST /api/found-footy/refresh` (coarse `{type:'refresh'}`) and (b) FastAPI `POST /api/v1/internal/notify` (typed envelope with entity + ids + fields).
- SSE broadcast is bounded-queue per connection (`maxsize=100`, `sse.py:60`). Slow-client policy: on `QueueFull`, drop the connection — EventSource reconnects and catches up via REST (`sse.py:75-83`).
- Per-connection heartbeats every 30 s of idle (`SSE_HEARTBEAT_INTERVAL_S`, `sse.py:99-112`).
- sse-starlette `ping=15` sends TCP-keepalive comment lines separate from envelope heartbeats (`stream.py:53`).
- Monotonic `id:` field on every event supports `Last-Event-ID` in principle but is not honored on reconnect — replay is via REST (`stream.py:12-15`).

**REMARKS**: The dual-publish is load-bearing for the rebuild. Until the FastAPI is the frontend's direct target, the Go rewrite's SSE bridge must ALSO hit the legacy Express endpoint (or the vedanta-systems Express must be decommissioned/migrated first). Preserve the envelope schema exactly — it's what the copy-paste target project referenced in `envelope.py:2-3` is designed against.

### Preserve exactly in the rebuild

- SSE envelope shape (`type`, `id`, `ts`, `data`) and the four constructor types (`connected`, `invalidate`, `heartbeat`, `health`). Copy-paste contract.
- URL prefix `/api/v1/*` including `redirect_slashes=False`.
- REST projections + video-URL rewrite (`/video/...` → `/api/v1/videos/...`).
- Server-side status derivation for events.
- Dual-publish for the invalidation trigger during migration; single-publish only after vedanta-systems Express is retired.
- Twitter scaling on active-goal count in Mongo/pg, not Temporal queue depth.
- The 30 s heartbeat cutoff for Twitter instances.

### Build from scratch / drop

- `mark_instance_busy` / `mark_instance_available` — cut them unless the routing path is redesigned to consult the registry. The current worker path bypasses the registry entirely.
- Three redundant fallback URL paths — collapse to one.
- The stale `TWITTER_SESSION_URL` default (`http://found-footy-prod-twitter:8888`) — no such container exists.
- `python_on_whales`-driven `docker compose --scale`. The rebuild's scaler should target whatever orchestration primitive replaces this.
- CORS `*` default — set explicit origin.
- Round-robin fairness across worker processes — either accept "per-process fair" or move the counter into the registry.
