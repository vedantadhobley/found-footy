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

## Target content

- **Endpoint catalog** — HTTP method / path / auth / purpose per REST
  endpoint (fixtures, events, videos, share-id redirects, admin ops)
- **Auto-derived OpenAPI spec** — from Huma struct tags on request/
  response types; committed at `docs/generated/openapi.yaml` per
  [`../rebuild-plan.md`](rebuild-plan.md) §15.3
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
