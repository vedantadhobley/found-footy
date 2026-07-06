# api-contract.md — Go rebuild

**Phase F stub.** Populated during Phase A (api surface).

Target content:

- Endpoint catalog — HTTP method / path / auth / purpose per endpoint
- Auto-derived OpenAPI spec — from Huma struct tags on request/response
  types; committed at `docs/generated/openapi.yaml` per
  [`../rebuild-plan.md`](../rebuild-plan.md) §15.3
- SSE stream contract — event kinds (`event.detected` / `event.stable`
  / `event.video_ready` / etc.), reconnect semantics, backfill window
- Webhook subscription + delivery — signature scheme, retry semantics
  via JetStream, idempotency key (`X-FF-Delivery-Id`)
- Share-id redirect contract — 302 for active, 302 for superseded via
  chain, 410 for removed, 404 for never-existed. **Load-bearing URL
  stability invariant.**
- Cache-Control policy on the 302 redirect per decisions.md 2026-07-02
  (play-latency fix)

Current design source of truth: [`../rebuild-plan.md`](../rebuild-plan.md)
§8 (public API + SSE + webhooks).
