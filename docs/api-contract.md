# API Contract — REST + SSE pattern

**Status**: live in dev as of Phase 6 (2026-05-26). Production cutover
is per-endpoint over the next few sessions.

This document is the **authoritative source** for the URL shapes, JSON
shapes, and SSE envelope protocol the new `api/` package serves. The
vedanta-systems React frontend reads against this contract (over time;
migration is incremental). It also doubles as the **reference**
project #2 will copy-adapt when it lands.

If something here disagrees with the code, the **code wins** for what's
running but file an issue — the contract drift means either docs need an
update or behavior needs a revert.

---

## URL stability

**Non-negotiable**: shared links already in the wild MUST continue to
resolve. That means:

- `/api/v1/*` paths are versioned in the prefix. New shapes get
  `/api/v2/*`; never break v1.
- Event ids are opaque, stable forever once issued. Format is
  `{fixture}_{team}_{player}_{type}_{seq}` (see `docs/orchestration.md`).
- Video URLs returned by `/api/v1/fixtures` and friends are stable
  paths under `/api/v1/videos/<bucket>/<key>`. The MinIO key is
  derived from the file hash, so the same content always lives at the
  same URL.
- Schema additions are allowed without bumping `v1` (new fields appear,
  clients ignore what they don't recognize). Field removals or type
  changes ARE breaking and require `v2`.

The legacy `/api/found-footy/*` paths (served by vedanta-systems' Express
router) stay live indefinitely during the migration. Both surfaces hit
the same MongoDB; both URLs work.

---

## Authentication

| Surface | Auth | Why |
|---|---|---|
| `/api/v1/health`, `/api/v1/dates`, `/api/v1/fixtures*`, `/api/v1/events*`, `/api/v1/search`, `/api/v1/stream` | None | Internal tailnet, public read API model |
| `/api/v1/internal/*` | `X-Internal-Token` header matching `INTERNAL_TOKEN` env var | Worker → API push channel; protected even though internal-network-only |
| `/api/v1/videos/*` (future) | None at the API layer; MinIO path validated against MongoDB | Validation prevents bucket enumeration |

In dev, `INTERNAL_TOKEN=""` (empty) is the no-auth-required signal — relies
on docker network isolation. Prod **must** set the token to a strong
shared secret.

---

## REST endpoints

### `GET /api/v1/health`

```json
{
  "status": "ok" | "degraded",
  "mongo": "up" | "down",
  "sse_connections": <int>,
  "timestamp": "<ISO 8601>"
}
```

### `GET /api/v1/dates`

```json
{ "dates": ["2026-05-27", "2026-05-26", ...] }
```

YYYY-MM-DD strings, most recent first. Capped at the last 90 days of
completed fixtures (plus all active and staging).

### `GET /api/v1/fixtures?date=<YYYY-MM-DD>` (recommended)

```json
{
  "staging": [<fixture>, ...],
  "active": [<fixture>, ...],
  "completed": [<fixture>, ...],
  "date": "2026-05-27"
}
```

Without `?date=`, returns every fixture in every collection. Discouraged
on mobile — kept for back-compat.

### `GET /api/v1/fixtures/<fixture_id>`

```json
{
  "collection": "fixtures_active" | "fixtures_completed" | "fixtures_staging",
  "fixture": <fixture>
}
```

404 with `{"error":"not_found","message":"fixture not found","resource":"fixture","id":"<id>"}`
if not found in any collection.

### `GET /api/v1/events/<event_id>`

For "shared event link" routing — looks up which date/fixture an event
belongs to so the frontend can navigate to the right view.

```json
{
  "event_id": "1545408_40_234_Goal_1",
  "found": true,
  "date": "2026-05-26",
  "fixture_id": 1545408,
  "collection": "fixtures_completed",
  "status": "watching" | "complete" | "extracting" | "validating"
}
```

`found: false` is the not-found response (200 OK with `found: false`,
NOT 404) — keeps the shared-link flow simple.

#### The `status` field

Phase 6 add. Frontend reads this DIRECTLY instead of computing from
`_monitor_complete` / `_download_complete` / `_s3_videos`. Decouples the
UI semantic from the workflow lifecycle.

| status | meaning | derivation |
|---|---|---|
| `watching` | At least one S3 video exists; user can play something | `len(_s3_videos) > 0` |
| `complete` | All 10 download attempts ran, no videos found (low-coverage match — nothing's coming) | `_download_complete && len(_s3_videos) == 0` |
| `extracting` | Monitor confirmed real event, scraping in progress | `_monitor_complete && !_download_complete && len(_s3_videos) == 0` |
| `validating` | Debouncing API report (waiting for stability + known player) | `!_monitor_complete` |

The `extracting → watching` flip happens **as soon as the first usable
clip lands**, not when all 10 attempts finish. This fixes the long-
standing "extracting persists for 8-10 min after a goal is already
playable" UX bug.

### `GET /api/v1/search?q=<term>`

```json
{
  "results": [
    {"date": "2026-05-27", "fixtures": [<fixture with _search>, ...]},
    {"date": "2026-05-26", "fixtures": [...]}
  ],
  "query": "<echo>",
  "total_fixtures": <int>
}
```

Per-fixture `_search` annotation:
```json
{
  "team_match": true | false,
  "matched_event_ids": ["<event_id>", ...],
  "match_count": <int>
}
```

Empty / too-short queries (< 2 chars) return `{"results": [], "query": "", "total_fixtures": 0}` without hitting Mongo.

### `GET /api/v1/videos/<bucket>/<key>` (deferred — Phase 6 part 2)

Range-aware proxy from MinIO. Honors `Range` requests for video seeking.
Validates `(bucket, key)` against actual S3 URLs stored on fixture events
before serving (prevents bucket enumeration).

Not yet implemented in the new API. The legacy Express endpoint at
`/api/found-footy/video/*` continues to serve.

### `POST /api/v1/internal/notify` (worker only)

```json
// request
{
  "entity": "fixtures",
  "ids": ["1545408", "1544608"],
  "fields": ["events._download_complete"]   // optional
}

// response
{
  "delivered_to": <int>,                     // number of SSE connections that got it
  "event_id": "<monotonic>"
}
```

Headers: `X-Internal-Token: <secret>` required when `INTERNAL_TOKEN` env
var is non-empty.

---

## SSE protocol — `GET /api/v1/stream`

Standard `text/event-stream`. Each event has the shape:

```
id: <monotonic counter>
event: <type>
data: <JSON-encoded envelope>

```

(Blank line terminates each event per the SSE spec.)

The `data` JSON is always the same envelope:

```json
{
  "type": "<event_type>",
  "id": "<monotonic counter; matches the `id:` field>",
  "ts": <unix milliseconds>,
  "data": { /* type-specific */ }
}
```

### Event types

| `type` | Sent when | `data` payload |
|---|---|---|
| `connected` | Once per connection, immediately after stream opens | `{}` |
| `invalidate` | Worker mutated MongoDB → broadcast via `/internal/notify` | `{"entity": "<resource>", "ids": ["<id>", ...], "fields": ["..."]?}` |
| `heartbeat` | Every `SSE_HEARTBEAT_INTERVAL_S` (30s) of idle. Lets client detect dead connections fast | `{}` |
| `health` | (future) On health snapshot changes | `{"status": "ok" \| "degraded", "details": {...}?}` |

The `connected` event lets clients distinguish first-connect (refresh
their state from REST) vs reconnect (might have already-fresh data).

### Client pattern

```ts
const es = new EventSource('/api/v1/stream')

es.addEventListener('connected', () => {
  // First connect or reconnect — refetch what's on screen from REST
})

es.addEventListener('invalidate', (e) => {
  const env = JSON.parse(e.data)
  const { entity, ids } = env.data
  // If anything we have cached matches, refetch just those via REST
  if (entity === 'fixtures') {
    queryClient.invalidateQueries({ queryKey: ['fixtures', ...ids] })
  }
})

es.addEventListener('heartbeat', () => {
  // Optional: track last-seen for stale-connection UI affordance
})

es.addEventListener('error', () => {
  // EventSource auto-reconnects; no manual handling needed.
  // Last-Event-ID is sent automatically.
})
```

### Important: messages can be dropped

For slow clients, the server's per-connection queue is bounded
(`SSE_MAX_QUEUE_PER_CONNECTION = 100`). A full queue causes the
connection to be dropped on the next broadcast — the client's
EventSource will reconnect, fire `connected` again, and the client
refetches its state from REST. **The pattern explicitly tolerates
SSE message loss** because REST is the source of truth and SSE is
just an invalidation hint.

### Caddy / nginx proxy behavior

The stream response sets `X-Accel-Buffering: no` which both Caddy and
nginx honor — they pass bytes through unbuffered. Don't set proxy
buffering on at the proxy layer.

---

## Error responses

All non-2xx responses use the consistent shape:

```json
{
  "error": "<machine-readable code>",
  "message": "<human readable>",
  ...                                  // optional extra fields per error class
}
```

| HTTP | `error` code | Source |
|---|---|---|
| 400 | `bad_request` | Malformed query string / body |
| 401 | `unauthorized` | Missing/invalid `X-Internal-Token` |
| 404 | `not_found` | Resource doesn't exist; carries `resource` + `id` |
| 503 | `service_unavailable` | Mongo/MinIO down etc. |

Clients should **switch on `body.error`**, not on the human message.

---

## Reusability for project #2

This pattern is intentionally portable. To stand up the same API + SSE
contract for another project (`long-exposure`, `spin-cycle`, etc.):

1. **Copy the `api/` package** as-is into the new project. It has
   minimal project-specific code: `settings.py` reads env vars and the
   routers are project-shaped (CRUD over project resources). The
   `sse.py`, `envelope.py`, `errors.py`, `deps.py`, and `app.py`
   modules are project-agnostic.

2. **Rewrite the routers** to expose project resources. Keep the URL
   shape (`/api/v1/<resource>`), the JSON envelope (typed errors,
   `_search`-style annotations on listing endpoints, etc.), and the
   SSE event types (`invalidate`, `heartbeat`, `connected`, `health`).

3. **Add to docker-compose**: copy the `api` service block from
   `docker-compose.dev.yml` / `docker-compose.yml`, rename containers
   (`<project>-{dev,prod}-api`), and add the project's networks.

4. **Add the Caddy route** in
   `~/workspace/proxy/caddy/caddy.d/<project>.caddy`:
   ```
   http://<project>-{dev,prod}-api.{$BASE_DOMAIN} {
     reverse_proxy <project>-{env}-api:8080
   }
   ```

5. **Update the worker** to dual-publish to both the legacy frontend
   endpoint (if any) and the new `/api/v1/internal/notify`. Use the
   same `entity / ids / fields` shape so the frontend's SSE handler
   doesn't have to switch on the source.

When the second project ships, extract the truly-shared bits
(`sse.py`, `envelope.py`, `errors.py`, `app.py`'s factory wiring) to a
new `~/workspace/dev/api-kit/` repo with both projects as callers.
Don't pre-extract — the right shape only emerges with two real callers.

---

## Implementation references

- `api/app.py` — FastAPI factory + lifespan + CORS middleware
- `api/sse.py` — SSEManager + per-connection queues + heartbeat interleaving
- `api/envelope.py` — typed event constructors (`invalidate`, `heartbeat`, `connected`, `health`)
- `api/errors.py` — HTTPException subclasses with consistent JSON shape
- `api/routers/` — per-resource endpoint modules
- `src/activities/monitor.py:notify_frontend_refresh` — worker side of the dual-publish path
- `deploy/INFRA-NOTES.md` — Caddy + docker network setup
