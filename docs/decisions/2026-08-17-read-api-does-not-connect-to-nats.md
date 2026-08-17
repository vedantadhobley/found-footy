# The read API does not connect to NATS

## Context

The rebuild plan §8 assigned SSE and webhook delivery to the Go API, so its
composition root constructed a NATS client. The shipped boundary differs:
workers publish core-NATS live-feed hints, while the `vedanta-systems` BFF
subscribes to `found-footy.<env>.>` and owns browser SSE. The Go API serves
read-only REST from Postgres and presigns Garage objects through S3.

The API never passed its NATS client to a handler or publisher. It only closed
the client during shutdown. Nevertheless, a failed initial NATS connection
made API startup fail, turning an event-bus outage into an unrelated public
read outage.

## Decision

- Remove NATS construction and shutdown wiring from `cmd/api`.
- Remove the NATS section from the API's typed configuration profile and its
  per-service Compose client-name overrides. NATS remains required by workers.
- Keep the API attached to `luv-dev` / `luv-prod`. That network carries the
  BFF's direct HTTP calls to `found-footy-{env}-api:8081`; it is not merely a
  broker attachment.
- Reintroduce an API-side NATS client only when a concrete API publisher or
  subscriber exists. Construction must accompany that consumer rather than
  reserve a speculative dependency.

## Consequences

The read API can start and serve its current contract when NATS is unavailable.
Live browser updates may pause during that outage, but the BFF already restores
state from the REST snapshot on initial connection and NATS reconnect. Worker
event publication and BFF subscription behavior do not change.

This decision supersedes the rebuild plan §8 placement of NATS-backed SSE and
webhooks inside the API. The as-built integration contract in `docs/api.md`
is authoritative.
