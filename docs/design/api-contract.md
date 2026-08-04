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
| `GET /api/v1/fixtures/{id}` | one fixture + its events — **fixture-scope refetch** (activation/completion/ingestion hints) |
| `GET /api/v1/events/{event_id}` | one event + its videos — **event-scope refetch** (goal/new-video/rank-change hints) |
| `GET /api/v1/fixtures` | bounded/by-state window (UTC timestamps; frontend groups by day) |
| `GET /api/v1/videos/{share_id}` | 302 → presigned S3 URL (browser fetches bytes from Garage directly) |
| share lookup | for og-server OG cards + deep links |

**Push→refetch rule:** every NATS hint carries `fixture_id` (always) + `event_id` (when the change
is event-level). The frontend refetches the **smallest named scope** — `event_id` present → the
event endpoint; else the fixture endpoint. One rule, two granularities. Subjects follow the dhobley
standard `found_footy.<domain>.<event_type>` (IDs in the payload, not the subject).

**No `GET /events?since=` backfill endpoint** — reconnect-replay is handled by the JetStream
durable consumer (see the eventing decision), not a read-API endpoint.
**No `POST /refresh`** — deprecated; found-footy publishes to NATS, the frontend refetches on hint.

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
