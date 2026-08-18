# Design proposal archive

These documents record designs considered during the Go rebuild. They explain
rationale but do not own current status. Use the as-built ledgers under
[`docs/`](../..) for shipped behavior and [`todo.md`](../../todo.md) for active
work.

## Shipped rationale

- [`twitter-port.md`](./twitter-port.md) — Go Twitter service port.
- [`twitter-scaling.md`](./twitter-scaling.md) — per-event Firefox fleet; the
  post-ship environment correction is FF-001.
- [`twitter-search-query.md`](./twitter-search-query.md) — query-construction
  rationale; current strings live in code and later decisions.
- [`completion-contract.md`](./completion-contract.md) — fixture completion;
  FF-014 records the post-ship score-consistency defect.
- [`vision.md`](./vision.md) — clip validation design.
- [`nats-producer-rebuild.md`](./nats-producer-rebuild.md) — three-topic live
  feed; current wire contract is [`api.md`](../../api.md).
- [`test-corpus.md`](./test-corpus.md) — regression corpus design.

## Superseded topology

- [`monitor.md`](./monitor.md) — superseded single-monitor topology.
- [`discovery.md`](./discovery.md) — superseded standalone discovery topology.
- [`video-dedup/`](./video-dedup/) — superseded multi-workflow V-phase
  topology; some dedup rationale remains useful.

## Retired subsystem

- [`team-aliases.md`](./team-aliases.md) and
  [`alias-entity-resolution.md`](./alias-entity-resolution.md) describe the
  Wikipedia/Wikidata resolver removed on 2026-08-16.
