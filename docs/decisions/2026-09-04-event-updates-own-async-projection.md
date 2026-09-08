# Event updates own the asynchronous event projection

## Context

Found Footy's monitor emits `fixture.status` for inline presentation movement
and `fixture.update` when a consumer must refetch a complete fixture. The
independent EventWorkflow emitted `event.video` after clip membership or a
ranking input changed. Its final `MarkDownstreamComplete` activity also changed
the public event projection from `phase=searching` to `phase=complete`, but it
emitted no live-feed hint. Consumers therefore remained stale until a later
snapshot.

The narrow subject name obscured the actual ownership boundary. Fixture topics
belong to fixture monitoring. The asynchronous downstream owns event-local
projection changes, whether the changed field is `videos` or `phase`.

## Decision

Replace `found-footy.<env>.event.video` with
`found-footy.<env>.event.update`. Keep the payload unchanged:

```json
{
  "event_id": "a4dbb584-bda7-4495-8eea-0736d252bcf0",
  "fixture_id": 1530158
}
```

EventWorkflow publishes the dirty signal only after the relevant durable write:

- every accepted placement that changes video membership or a ranking input;
- durable downstream completion, after `completed_at` and `outcome_class`
  commit.

The consumer fetches `/api/v1/events?ids=<event_id>` and upserts the returned
event. If its parent fixture is absent, it fetches that fixture by ID instead of
silently dropping the update. Event additions, removals, and provider-owned
corrections remain on `fixture.update` because they originate in reconciliation
and can change fixture membership, recency, or ordering.

Core NATS remains a best-effort hint plane. Initial connection and reconnect
still require authoritative REST recovery.

## Temporal compatibility

The `ff-085-event-update` version marker preserves the historical command graph
for already-running EventWorkflow histories. Those histories retain the
`PublishEventVideo` activity name and do not gain a completion activity. The
legacy activity stays registered but forwards to the current `event.update`
wire subject. New histories use `PublishEventUpdate` for both placements and
completion.

## Consequences

**Rollout amended 2026-09-08:** The
[coordinated hard-cutover decision](./2026-09-08-event-update-uses-coordinated-cutover.md)
supersedes the temporary dual-subject rollout below. The event contract and
Temporal compatibility requirements are unchanged.

The Found Footy worker and Vedanta Systems consumer form one coordinated
contract rollout. Deploy the consumer first with temporary acceptance of both
subjects, then deploy the worker. The payload and REST endpoints do not change.
Once pre-FF-085 histories have aged out, the legacy Temporal activity may be
removed; it is not a second wire contract.

This decision narrows the phrase “completion change” in the fixture contract to
fixture completion. EventWorkflow completion is event-local and belongs to
`event.update`.

## Superseded contract

This replaces the `event.video` subject name and video-only scope recorded by
[accepted candidates commit as one placement](./2026-08-28-accepted-candidates-commit-as-one-placement.md)
and retained by
[backend owns fixture presentation](./2026-08-30-backend-owns-fixture-presentation.md).
Their durable-placement, targeted-refetch, and fixture-presentation decisions
remain unchanged.
