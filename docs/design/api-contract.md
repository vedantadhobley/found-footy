# api-contract.md — Go rebuild

**Phase F stub.** Populated during Phase A (api surface).

## Scope

found-footy exposes a **structured REST surface** for state queries
(fixtures, events, videos, share-id redirects) and **publishes to NATS**
for real-time events consumers need to react to. Nothing more.

**SSE / WebSocket / long-poll bridges are OUT OF SCOPE.** Those
belong to [vedanta-systems](https://vedanta.systems) — the public
portal BFF that fronts found-footy for browsers. vedanta-systems
subscribes to found-footy's NATS stream and translates to whatever
transport the browser needs (SSE today, likely still SSE in 2026).
Rationale: SSE / WebSocket is a *presentation* concern (which
browser tab is currently on screen, what backfill it wants, which
event kinds it filters); presentation lives in the BFF, not the
core service. Keeping found-footy transport-agnostic on the outbound
side means other consumers (mobile app if we ever build one, ops
dashboards, external partners) can plug into the same NATS stream
without duplicating the bridge logic.

## Boundary rule (2026-07-21 NATS scope decision)

Per [decisions.md 2026-07-21 NATS-scope entry](../decisions.md#2026-07-21--nats-scope-inter-project-only-pg-notify-for-intra-project-pub-sub):

- **Intra-project** (found-footy internal — Monitor → Discovery → V-phase
  → Asset): pg NOTIFY / LISTEN. Not exposed externally.
- **Inter-project** (found-footy → vedanta-systems, and any future
  external consumer): NATS subjects. This IS the api-contract's
  push-side.

vedanta-systems reaches found-footy two ways: NATS subscription for
push, and REST for pull / on-demand queries. Both surfaces need
documentation here.

## Endpoint catalog (settled 2026-08-04 — see [decisions.md](../decisions.md))

Built on **Chi**, not Chi+Huma (small read surface, one client we control — Huma's
auto-OpenAPI/validation isn't worth its weight; hand-shaped JSON matching the frontend).
The API is **timezone-agnostic** (no `date` param — the frontend buckets by local tz) and
the **update unit is the fixture** (`GET /fixtures/{id}` is the surgical refetch the frontend
hits per NATS push hint; replaces Python's coarse "refetch everything every 30s").

| Method / path | Purpose |
|---|---|
| `GET /api/v1/fixtures` | flat `[Fixture]`. No arg = full window (initial load, UTC timestamps; frontend buckets by state + day). `?ids=a,b,c` = **batch refetch** — the monitor cycle's delta in ONE call; fixtures come full, so it covers new goals AND minute/status/score bumps across every changed match |
| `GET /api/v1/fixtures/{id}` | one `Fixture` — single fixture-scope refetch |
| `GET /api/v1/events?ids=a,b` | flat `[Event]` — **batch** (between-cycle single-event updates coalesced) |
| `GET /api/v1/events/{event_id}` | one `Event` — single event-scope refetch |
| `GET /api/v1/videos/{share_id}` | 302 → presigned Garage URL (browser fetches bytes directly) |

Passing a list works exactly like passing one — same DTO, just an array. Batch is capped at 200 ids;
unknown ids are silently omitted (a fixture may have aged out of retention since the hint).

**Push→refetch rule (granular publish, BFF coalesces).** found-footy publishes **granular per-event**
hints — one message per domain fact (`goal.detected` / `goal.removed` / `fixture.status` /
`clip.ready`), ids in the payload, subjects `found_footy.<domain>.<event_type>`. The **batching lives
in vedanta-systems' BFF, not here** (SSE + coalescing are its job per the boundary rule): it debounces
a short *time* window (~200ms; cycle-agnostic), dedups the `fixture_id`s, fires **one**
`GET /fixtures?ids=…` (fixtures come full, events nested — no per-fixture fan-out), and pushes the
result to the stupid browser over SSE. The browser knows nothing of monitor cycles, NATS, or
coalescing — it just refetches the id-set the BFF hands it. A between-cycle `clip.ready` drives a
`GET /events/{id}` (or `?ids=` batch). Publishing granular (NOT per-cycle blobs) keeps the stream a
durable, replayable log of facts — right for JetStream + webhooks — and lets each consumer batch to
its own needs (browser BFF coalesces; a webhook delivers each event). Emitting the stream is the
composer's job (the eventing task); this contract just guarantees the batch reads it leans on exist.

**No `GET /events?since=` backfill endpoint** — reconnect-replay is handled by the JetStream
durable consumer (see the eventing decision), not a read-API endpoint.
**No `POST /refresh`** — deprecated; found-footy publishes to NATS, the frontend refetches on hint.

## Response shapes (settled 2026-08-09)

Hand-shaped JSON from the Go domain models — **not** the Python Mongo passthrough
(field redesign explicitly greenlit; vedanta-systems' in-progress redesign consumes
this). **One schema per resource, composed:** `Fixture` contains `[]Event`, `Event`
contains `[]Video`. Each is defined once and reused — the `Event` from
`/events/{id}` is identical to the one nested in a `Fixture`. There is **no**
separate "event-level" vs "fixture-level" schema.

**`Event.fixture_id` is the one field that makes event-scope work.** It's on every
`Event` (redundant when nested, load-bearing at the top level) so the frontend can
splice an event-scope refetch into the fixture it already has cached, without
special-casing where the event came from.

**Fixtures are full, and the shape is flat.** Every fixtures response — the full
window, a `?ids=` batch, or a single `/fixtures/{id}` — carries each fixture WITH
its events+videos (not summaries), so one call renders/reconciles whole matches and
the frontend only surgical-refetches on hints. `GET /fixtures` returns a flat
`[Fixture]` (the frontend buckets by `state`), NOT a `{staging,active,completed}`
object — so "pass a list" is identical to "pass one", just an array. The window is
bounded (completed capped to recent); a `?summary` variant is a later addition if a
busy day's payload gets heavy.

### Fixture

```json
{
  "id": 1234567,
  "state": "staging|active|completed",
  "kickoff": "2026-08-14T19:00:00Z",
  "league": { "id": 140, "name": "La Liga", "season": 2026 },
  "home": { "id": 529, "name": "Barcelona", "score": 2, "winner": true },
  "away": { "id": 541, "name": "Real Madrid", "score": 1, "winner": false },
  "status": { "short": "2H", "long": "Second Half", "elapsed": 67, "extra": null },
  "last_activity_at": "2026-08-14T19:52:00Z",
  "events": [ /* Event, … */ ]
}
```

### Event

```json
{
  "id": "…uuid…",
  "fixture_id": 1234567,
  "type": "goal",
  "detail": "Normal Goal",
  "minute": 67,
  "extra": null,
  "team": { "id": 529, "name": "Barcelona" },
  "player": { "id": 152, "name": "Lewandowski" },
  "videos": [ /* Video, … */ ]
}
```

### Video (one LIVE clip = active share + its live asset)

```json
{
  "share_id": "s_a1b2c3d4e5f6",
  "url": "/api/v1/videos/s_a1b2c3d4e5f6",
  "rank": 1,
  "verified": true,
  "extracted_minute": 67,
  "popularity": 3,
  "width": 1920, "height": 1080, "duration_ms": 8000
}
```

`videos` is the #171 read model realized: **live assets only** (`superseded_by IS
NULL`), active shares, ordered by `CompareShares` (`rank: 1` = primary).
Superseded/removed clips never appear here, but their `s_…` URLs still resolve via
the 302 handler. `url` is the share-id endpoint; the browser follows the 302 to the
presigned Garage URL.

### Endpoint → DTO

| Endpoint | Returns |
|---|---|
| `GET /api/v1/fixtures` (± `?ids=`) | flat `[Fixture]` — frontend buckets by `state` |
| `GET /api/v1/fixtures/{id}` | `Fixture` |
| `GET /api/v1/events?ids=` | flat `[Event]` |
| `GET /api/v1/events/{event_id}` | `Event` |
| `GET /api/v1/videos/{share_id}` | 302 → presigned URL (see redirect contract below) |

## Target content (remaining to spec here)

- **NATS subject catalog** — subject names for the events found-footy
  publishes for inter-project consumption (`event.detected` /
  `event.stable` / `event.video_ready` / etc.), JetStream retention
  + replay semantics, subscription pattern for vedanta-systems.
  **Consumers are responsible for their own presentation transport**
  (SSE, WebSocket, whatever).
- **Webhook subscription + delivery** — for any external partner that
  can't run NATS: signature scheme, retry semantics via JetStream,
  idempotency key (`X-FF-Delivery-Id`)
- **Share-id redirect contract** — 302 for active, 302 for superseded
  via chain, 410 for removed, 404 for never-existed.
  **Load-bearing URL stability invariant.**
- **Cache-Control policy on the 302 redirect** per
  [decisions.md 2026-07-02 play-latency fix](../decisions.md)

## Deliberately excluded from this doc

- SSE endpoint definitions
- WebSocket protocol
- Browser reconnect / backfill logic
- Presentation-side filtering of event kinds

All of the above live in vedanta-systems' own api-contract, not this
one. If future found-footy code appears to need SSE (e.g. a browser
consumer without vedanta-systems in front), stop and rediscuss —
the split exists to keep found-footy pure infrastructure.

## Related

- [`../rebuild-plan.md`](rebuild-plan.md) §8 — public API surface (still lists SSE as an option; this doc supersedes that for the found-footy side)
- [decisions.md 2026-07-21 NATS scope](../decisions.md)
- vedanta-systems' own api-contract (external to this repo) — owns everything this doc excludes
