# Backend owns fixture presentation

## Context

Found Footy's REST fixture exposed API-Football `short`, `long`, `elapsed`, and
`extra` fields. Vedanta Systems independently mapped provider codes to product
groups and decided whether the fixture indicator displayed a clock or a status.
Monitor also classified every status-code change as structural. A transition
such as `1H -> HT` therefore emitted `fixture.update`, and the current BFF
discarded its fixture IDs and reloaded the full date window.

The transport `staging`/`active`/`completed` state cannot solve this. An `FT`
fixture remains active during terminal observation grace but must present as
finished immediately; a monitored postponed fixture remains active but must not
present as playing.

## Decision

Found Footy derives one consumer projection from canonical provider facts:

```json
{
  "presentation_state": "playing",
  "clock": { "minute": 45, "extra": null },
  "status": { "short": "HT", "long": "Halftime" },
  "display": "status"
}
```

`presentation_state` is `playing`, `finished`, `upcoming`, or `deferred`.
`display` is `clock` only during `1H`, `2H`, `ET`, or `LIVE` with a reported
minute; otherwise it is `status`. Consumers format a clock or render the
provided short status. They do not map API-Football codes. REST and NATS use
the same `internal/contract/fixturepresentation.Projection` type.

Monitor returns one typed publication action: no-op, `status`, or
`update`. A projection change inside one presentation state is inline. A state
boundary or any score, event, winner, penalty, metadata, or completion change
requires an authoritative snapshot, and `update` wins if both occur.

`fixture.status` replaces `fixture.clock`. `fixture.update` remains an
ID-only targeted invalidation, and `event.video` remains an event-scoped
invalidation. Initial connection and reconnect still require a complete REST
snapshot because Core NATS is transient.

## Consequences

- `1H -> HT -> 2H`, `ET -> BT -> ET`, and ordinary minute movement update the
  fixture indicator without a REST read or reorder.
- `NS -> 1H`, `2H -> FT`, `P -> PEN`, and `PST -> NS` refetch and rebucket the
  fixture. First terminal observation still owns finished-fixture recency.
- The REST shape and NATS subject are breaking consumer changes. The shared
  schema and Vedanta Systems consumer must land before this producer deploys.
  No permanent dual-subject compatibility path is retained.
- Vedanta Systems should preserve and coalesce `fixture.update.fixture_ids`
  instead of converting every invalidation into a full-window refresh.

## Superseded contract

This supersedes only the `fixture.clock` payload and the broad
status-is-structural classification in the
[2026-08-14 three-subject decision](./archive-through-2026-08-16.md#2026-08-14--nats-producer-rebuild-the-3-subject-live-feed-model-supersedes-the-2026-08-04-eventing-shape).
The environment-scoped Core NATS envelope, targeted `fixture.update`,
`event.video`, and full-snapshot reconnect rules remain in force.
