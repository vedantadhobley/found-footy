# Read API and live-feed contract

This is the as-built integration contract for Found Footy consumers. REST is
the state authority. NATS messages are low-latency hints that tell a consumer
what to update or refetch. Found Footy does not expose SSE or WebSockets; a BFF
owns browser transport and reconnect behavior.

Implementation authority is
[`internal/api`](../internal/api/) for HTTP and DTOs, and
[`internal/infra/event`](../internal/infra/event/) for NATS. The workspace
schemas under `~/workspace/nats/schemas/` own the shared envelope and payload
validation; their cross-project ownership is recorded in the
[dhobley messaging topology](../../../vedanta-dhobley/docs/topology.md#messaging--events).

## HTTP surface

The API uses Chi and serves these routes. There are no single-fixture or
single-event path routes.

| Method and path | Contract |
|---|---|
| `GET /healthz` | `200 ok` for the API listener. |
| `GET /api/v1/fixtures` | Flat fixture array containing staging, active, and completed rows. This is the initial and reconnect snapshot. |
| `GET /api/v1/fixtures?ids=1,2` | Flat fixture array for up to 200 IDs. Unknown IDs are omitted. |
| `GET /api/v1/events?ids=<uuid>,<uuid>` | Flat event array for up to 200 IDs. `ids` is required; unknown IDs are omitted. |
| `GET /api/v1/search?q=<text>` | Up to 100 fixtures, kickoff-newest first, matched case-insensitively by competition, team, scorer, or assist. |
| `GET /api/v1/videos/{share_id}` | Playback redirect or terminal share status. |

Malformed ID lists, more than 200 IDs, and an empty search query return `400`
with `{"error":"..."}`. An empty result is `200` with `[]`. Handler failures
return `500` without exposing the internal error.

The unfiltered fixture window currently has no query-layer limit. Retention
removes some old data, but shared/tombstoned rows can remain. The resulting
read-amplification issue is tracked as `AUD-0813-P2-1` in
[`todo.md`](./todo.md#deferred-decisions-and-validation).

### Resource composition

The response model composes one shape per resource:

```text
fixtureDTO
└── events: []eventDTO
    └── videos: []videoDTO
```

Staging fixtures carry an empty `events` array. Active and completed fixtures
carry their non-removed events; each event carries active shares joined to
non-superseded assets, ordered by rank. A batch-event lookup can still return a
directly requested removed event with `phase: "removed"`.

Pointers are emitted explicitly. Consumers must preserve the distinction
between `null` and zero.

```jsonc
{
  "id": 1530158,
  "state": "active",
  "kickoff": "2026-08-14T16:00:00Z",
  "league": {
    "id": 135,
    "name": "Serie A",
    "season": 2026,
    "country": "Italy",
    "round": "Regular Season - 1"
  },
  "home": { "id": 505, "name": "Inter", "score": 2, "winner": null },
  "away": { "id": 489, "name": "Milan", "score": 1, "winner": null },
  "penalty": null,
  "status": { "short": "2H", "long": "Second Half", "elapsed": 62, "extra": null },
  "last_activity_at": "2026-08-14T16:47:30Z",
  "events": []
}
```

`last_activity_at` is derived at read time from activation, completion, and the
latest first-seen time among surviving known-player events. Polls, clock ticks,
and unknown-player placeholders do not advance it; removing an event can move
it backward.

```jsonc
{
  "id": "<uuid>",
  "fixture_id": 1530158,
  "type": "goal",
  "detail": "normal goal",
  "minute": 62,
  "extra": null,
  "team": { "id": 505, "name": "Inter" },
  "player": { "id": 1234, "name": "Lautaro" },
  "assist": { "id": 5678, "name": "Barella" },
  "phase": "searching",
  "debounce_count": 3,
  "videos": []
}
```

`player` and `assist` can be `null`. `phase` is one of `detected`, `searching`,
`complete`, or `removed`. Phase and clip presence are independent: clips may
arrive while search is still running, and a complete search may have no clips.

```jsonc
{
  "share_id": "s_abc123",
  "url": "/api/v1/videos/s_abc123",
  "rank": 1,
  "verified": true,
  "extracted_minute": 62,
  "popularity": 3,
  "width": 1280,
  "height": 720,
  "duration_ms": 7000
}
```

The video URL resolves an active or superseded share through its asset chain
and returns `302` to a presigned Garage URL. A removed share returns `410`; a
never-minted share returns `404`. Redirect cache lifetime is the configured
presign lifetime minus a one-minute safety margin, capped at five minutes
(FF-028). The default five-minute presign therefore sends `Cache-Control:
public, max-age=240`; a lifetime too short to provide the margin sends
`no-store`.

## NATS live feed

The read API binary does not connect to NATS. Workers publish these live-feed
hints through `internal/activity/livefeed`; the `vedanta-systems` BFF subscribes
to the environment-scoped subjects and converts them to browser SSE. REST
remains the recovery and snapshot authority if NATS is unavailable.

`EVENT_ENV` supplies the environment token. A consumer must subscribe to one
environment, such as `found-footy.prod.>`, rather than mixing dev and prod.

| Wire subject | Payload | Consumer action |
|---|---|---|
| `found-footy.<env>.fixture.clock` | `{"fixtures":[{"fixture_id":1530158,"minute":62,"extra":null}]}` | Apply the clock fields in place. Do not fetch. |
| `found-footy.<env>.fixture.update` | `{"fixture_ids":[1530158,1530163]}` | Fetch `/api/v1/fixtures?ids=1530158,1530163`; replace by fixture ID and re-bucket by state. |
| `found-footy.<env>.event.video` | `{"event_id":"<uuid>","fixture_id":1530158}` | Fetch `/api/v1/events?ids=<uuid>`; replace the event inside its fixture. |

Every payload is wrapped in the version-1 workspace envelope:

```json
{
  "id": "<uuid>",
  "ts": "<RFC3339 UTC>",
  "source": "found-footy-prod",
  "version": 1,
  "subject": "found-footy.prod.fixture.update",
  "payload": { "fixture_ids": [1530158] }
}
```

Within one monitor cycle, `fixture.clock` and `fixture.update` are disjoint.
Any structural change wins and its current clock arrives in the full fixture
refetch. A frozen clock emits nothing. `event.video` is asynchronous and can
arrive after the fixture completes.

The publisher uses core NATS, not a durable JetStream consumer contract. A
consumer must take a full `GET /api/v1/fixtures` snapshot on initial connection,
browser reconnect, and BFF-to-NATS reconnect. Lost hints then self-heal from
REST. Applying the same refetch more than once must be harmless.

## Consumer invariants

- A `fixture.update` response replaces the fixture's event list. An event that
  disappears was removed; revoke it and its displayed clips.
- `phase` and `videos` render independently. Do not derive workflow state from
  clip count.
- `player: null` is a valid unknown-player event. It is not searched until the
  provider supplies a player identity.
- `score`, `winner`, `elapsed`, `extra`, and `penalty` use `null` for
  not-reported. Do not render `null` as zero.
- `video.url` is opaque. Follow the redirect; do not retain or parse the
  presigned target.
