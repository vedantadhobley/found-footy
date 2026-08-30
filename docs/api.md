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
| `GET /api/v1/fixtures` | Flat fixture array containing every staging/active row plus completed fixtures on the newest configured UTC kickoff dates. This is the initial and reconnect snapshot. |
| `GET /api/v1/fixtures?ids=1,2` | Flat fixture array for up to 200 IDs. Unknown IDs are omitted; older retained rows remain addressable. |
| `GET /api/v1/events?ids=<uuid>,<uuid>` | Flat event array for up to 200 IDs. `ids` is required; unknown IDs are omitted. |
| `GET /api/v1/search?q=<text>` | Up to 100 publicly-windowed fixtures, kickoff-newest first, matched case-insensitively by competition, team, scorer, or assist. |
| `GET /api/v1/videos/{share_id}` | Playback redirect or terminal share status. |

Malformed ID lists, more than 200 IDs, and an empty search query return `400`
with `{"error":"..."}`. An empty result is `200` with `[]`. Handler failures
return `500` without exposing the internal error.

`PUBLIC_HISTORY_COMPLETED_FIXTURE_DATES` controls the completed history and
defaults to 14 distinct UTC kickoff dates that contain completed fixtures.
Current-day emptiness does not consume a date. The cutoff is computed inside
the same SQL statement as snapshot/search selection, so it is a read-model
boundary rather than a fixture-deletion clock. SQL history remains durable and
targeted ID reads can reach rows outside the public window. The worker uses the
same configured count for media retention; see the
[retention decision](./decisions/2026-08-30-retention-separates-public-media-and-audit-lifecycles.md).

### Resource composition

The response model composes one shape per resource:

```text
fixtureDTO
└── events: []eventDTO
    └── videos: []videoDTO
```

Staging fixtures carry an empty `events` array. Active and completed fixtures
carry their non-removed events; each event carries active shares joined to
non-superseded, unreclaimed assets. The handler assembles any fixture set with
four bounded reads for the request: fixtures, events, discovery completion,
and visible clips. It does not issue per-fixture or per-event child queries.
Before ranking, the read model applies FF-078's reversible
singleton-visibility rule. A timestamp-verified clip with popularity at least
three suppresses every popularity-one clip. An unverified clip with popularity
at least three suppresses only unverified popularity-one clips; it cannot hide
a timestamp-verified clip. Popularity-two clips remain visible. Omitted shares
stay active and their direct playback URLs remain valid. The query then derives
contiguous `rank` values from timestamp verification, popularity, file size,
creation time, and share ID; the stored compatibility rank is never part of the
public result. A batch-event lookup can still return a directly requested
removed event with `phase: "removed"`.

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
  "home": { "id": 505, "name": "Inter", "score": 2, "winner": true },
  "away": { "id": 489, "name": "Milan", "score": 1, "winner": false },
  "penalty": null,
  "presentation_state": "playing",
  "clock": { "minute": 62, "extra": null },
  "status": { "short": "2H", "long": "Second Half" },
  "display": "clock",
  "last_activity_at": "2026-08-14T16:47:30Z",
  "events": []
}
```

The fixture presentation is backend-derived. `presentation_state` is one of
`playing`, `finished`, `upcoming`, or `deferred`; it controls consumer grouping
and is not the Postgres `state` (`staging`, `active`, or `completed`). In
particular, an `FT` fixture presents as finished during its one-hour active
observation grace, and a monitored `PST` fixture presents as deferred.

`display` is `clock` only for `1H`, `2H`, `ET`, or `LIVE` with a reported
minute. Every pause, shootout, scheduled, terminal, deferred, and unknown
status uses `status`. The consumer formats `clock.minute` plus `clock.extra` or
renders `status.short`; it never maps provider codes. `status.long` is the
provider description for accessibility and expanded context. Clock values
remain nullable, and an unknown provider status fails closed to deferred/status.

`last_activity_at` is derived at read time from activation, first terminal
observation, and the latest first-seen time among surviving known-player
events. A completed row without a terminal observation (historical/direct
ingest) falls back to completion time. Polls, clock ticks, and unknown-player
placeholders do not advance it; removing an event can move it backward. The
later active-to-completed transition therefore does not reorder a fixture that
already rendered as finished during terminal grace.

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
durable reclaimed-object marker also forces `410`, so the API never presigns
known-missing bytes across a concurrent cleanup boundary. A never-minted share
returns `404`. Redirect cache lifetime is the configured
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
| `found-footy.<env>.fixture.status` | `{"fixtures":[{"fixture_id":1530158,"presentation_state":"playing","clock":{"minute":62,"extra":null},"status":{"short":"2H","long":"Second Half"},"display":"clock"}]}` | Replace the fixture's complete status/time presentation projection in place. Do not fetch. |
| `found-footy.<env>.fixture.update` | `{"fixture_ids":[1530158,1530163]}` | Fetch `/api/v1/fixtures?ids=1530158,1530163`; replace by fixture ID and re-bucket by `presentation_state`. |
| `found-footy.<env>.event.video` | `{"event_id":"<uuid>","fixture_id":1530158}` | Fetch `/api/v1/events?ids=<uuid>`; replace the event inside its fixture. Emitted after any accepted placement changes membership or a ranking input, including popularity-only duplicates. |

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

Within one monitor cycle, `fixture.status` and `fixture.update` are
disjoint. A presentation-state boundary or any score, event, winner, penalty,
metadata, or completion change selects `fixture.update`. A minute change or a
status change that remains in one presentation state selects
`fixture.status`; this includes `1H -> HT -> 2H` and `ET -> BT -> ET`.
An identical frozen projection emits nothing. `event.video` is asynchronous
and can arrive after the fixture completes.

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
- `score`, `clock.minute`, `clock.extra`, and `penalty` use `null` for
  not-reported. A
  normal or extra-time `winner` is derived from the current score; a terminal
  shootout uses `penalty`. A tie or incomplete relevant score returns
  `winner: null` for both sides. Exceptional terminal outcomes use the
  provider's explicit result. Do not render any `null` as zero.
- `video.url` is opaque. Follow the redirect; do not retain or parse the
  presigned target.

## Vedanta Systems handoff

This contract deliberately removes API-Football status interpretation from the
BFF and React. The consumer change must:

1. consume `presentation_state`, `clock`, `status`, and `display` from REST;
2. forward `fixture.status` as one inline SSE projection and replace
   those four fields by fixture ID;
3. preserve and union the IDs in bursty `fixture.update` messages, call
   `/api/v1/fixtures?ids=...`, then replace and re-order only those fixtures;
4. use `presentation_state` for grouping, live badges, and finished winner
   highlighting; use non-null `penalty` for shootout score formatting; and
5. retain a full fixture snapshot on initial load and every browser or NATS
   reconnect because Core NATS and SSE do not replay missed hints.

The deployed BFF preserves targeted fixture and event identity instead of
turning either signal into a generic full-window refresh. Shared schema commit
`fb04fee` replaced the fixture-clock schema, example, and README entry with
`fixture.status`; Vedanta Systems commit `81db099` landed the matching consumer
in the coordinated 2026-08-30 rollout. Found Footy's committed golden is
`internal/infra/event/testdata/found-footy.fixture.status.json`.
