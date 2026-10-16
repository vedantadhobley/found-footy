# Design proposal archive

These documents record designs considered during the Go rebuild. They explain
rationale but do not own current status. Use the as-built ledgers under
[`docs/`](../..) for shipped behavior and [`todo.md`](../../todo.md) for active
work.

## Active proposals

- [Shared Twitter request admission](./twitter-shared-admission.md) — FF-038
  existing-pattern research, verified Temporal 1.24 Signal/receipt and Firefox
  seams, partial-scan defects, and proposed first-release policy;
  [implementation plan](./twitter-shared-admission-implementation.md) covers
  contracts, file changes, recovery and enforcement gates. Passive accounting
  is deployed as `62af131`; admission is not implemented.
- [Reversible video selection](./reversible-video-selection.md) — FF-081 direct
  selected-substitute invariant, offline restoration proof, atomic ownership
  changes and preserved singleton filtering; validation, domain planning and
  atomic reselection and versioned workflow integration are implemented locally;
  migration, rollout and natural production validation remain.
- [`provider-integrity-circuit-breaker.md`](./provider-integrity-circuit-breaker.md)
  — FF-075 wire checks and shadow rules are shipped; September 16 durable
  evidence, versioned collector, and atomic guarded writes are local, not
  deployed. Bounded delayed-event search recovery remains proposed.

## Shipped rationale

- [Clock-field normalization](./clock-field-normalization.md) — FF-094 literal
  timing fields, bounded secondary arithmetic and frozen-clock policy;
  deployed within v3 on September 17, natural-event validation pending.
- [`terminal-observation-grace.md`](./terminal-observation-grace.md) — FF-063
  bounded terminal retirement while retaining event debounce and downstream
  settlement; application code is complete and production rollout is pending.
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
