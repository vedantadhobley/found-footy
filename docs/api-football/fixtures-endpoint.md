# /fixtures endpoint

**Status: Seeded from vendor doc v3.9.3, 2026-07-09.**

**Source**: `docs/api-football/vendor/api-football-v3.9.3.{pdf,html}`
→ "Fixtures" section (PDF pages 58-62). Live URL:
<https://www.api-football.com/documentation-v3> (Cloudflare-blocked
to agents).

## Base URL

```
https://v3.football.api-sports.io/fixtures
```

Direct API-Sports endpoint. RapidAPI mirror uses a different host
and header name; we're not on it, so ignore.

## Authentication

Single required header:

| Header | Value | Notes |
|---|---|---|
| `x-apisports-key` | plan API key | Direct API-Sports; NOT the RapidAPI header |

## Query parameters (page 60)

Any parameter can be combined with any other except where noted.

| Param      | Type    | Format / Enum                 | Notes                                                    |
|------------|---------|-------------------------------|----------------------------------------------------------|
| `id`       | integer | fixture id                    | Single fixture                                           |
| `ids`      | string  | `id-id-id`                    | **Max 20 ids** per call, hyphen-separated                |
| `live`     | string  | `all` or `id-id` (league ids) | All fixtures currently in play; or filtered by leagues   |
| `date`     | string  | `YYYY-MM-DD`                  | All fixtures on a given calendar date                    |
| `league`   | integer | league id                     |                                                          |
| `season`   | integer | `YYYY` (4 chars)              | Season start year                                        |
| `team`     | integer | team id                       |                                                          |
| `last`     | integer | ≤ 2 chars                     | Last X fixtures                                          |
| `next`     | integer | ≤ 2 chars                     | Next X fixtures                                          |
| `from`     | string  | `YYYY-MM-DD`                  | Window start                                             |
| `to`       | string  | `YYYY-MM-DD`                  | Window end                                               |
| `round`    | string  | free-form                     | e.g. `Regular Season - 1`; discoverable via `/fixtures/rounds` |
| `status`   | string  | short code or `SHORT-SHORT`   | One or more fixture status short codes; see [status-codes.md](./status-codes.md) |
| `venue`    | integer | venue id                      |                                                          |
| `timezone` | string  | e.g. `Europe/London`          | Timezone from `/timezone` endpoint                       |

## Response envelope

Every response wraps the array in:

```json
{
  "get": "fixtures",
  "parameters": { "live": "all" },
  "errors": [],
  "results": 4,
  "paging": { "current": 1, "total": 1 },
  "response": [ /* array of APIFixture */ ]
}
```

- `errors` — array (or object; see the "soft errors" note in
  [rate-limits.md](./rate-limits.md)). Non-empty errors can coexist
  with HTTP 200 + a valid `response` array. The adapter rejects any
  nonempty value before returning fixture data.
- `results` — must be present and equal the decoded `response` length.
- `paging` — must be present and exactly `{current:1,total:1}`; the adapter
  rejects silent partial pages. Fixtures endpoints typically fit in one page.

For `ids=` queries, the adapter also requires every requested fixture exactly
once and requires each fixture's `events` field to be an array. Missing and
`null` events are contract failures; explicit `[]` means a valid empty event
inventory. See [`FF-075`](../todo.md#ff-075--successful-provider-responses-can-destructively-regress-live-state).

## Documented response codes (page 61)

| Code | Meaning                | Notes                                              |
|------|------------------------|----------------------------------------------------|
| 200  | OK                     | Standard success                                   |
| 204  | No Content             | Query valid, no data (e.g. `date=` with no fixtures) |
| 499  | Time Out               | Vendor-side timeout (non-standard code)            |
| 500  | Internal Server Error  | Vendor-side failure                                |

> **⚠ 429 is NOT in the documented response set for `/fixtures`.**
> Our adapter treats 429 as a distinct outcome class, but this is
> observed-in-production behavior, not doc-specified. The doc's
> "Rate Limiting Policy" (page 3) states: *"Excess traffic may be
> temporarily or permanently blocked without notice."* — implying
> excess requests may fail with vendor-choice error codes. See
> [rate-limits.md](./rate-limits.md) for what we've actually seen.

## What `/fixtures?ids=` returns per fixture

Per the request-sample comment on page 61:

> *"In this request events, lineups, statistics fixture and players
> fixture are returned in the response."*

So an id-based query gets a FULL fixture record with events inline
— no separate `/fixtures/events` call needed. This is what our
Monitor relies on and matches the Python adapter's assumption.

Same holds for `/fixtures?id={single}` and `/fixtures?live=all`.

Whether `/fixtures?date=` or `/fixtures?league=&season=` include
events inline is not clearly stated for those variants — safe
assumption is they don't (they're bulk-discovery queries; the
inline-events property appears tied to id/live queries).

## Top-level per-fixture shape

Reconstructed from Python + captured samples; not verbatim from doc
schema (doc uses collapsed `{...}` in samples). See per-topic docs
for detail:

- Events array — [events-shape.md](./events-shape.md)
- Status codes — [status-codes.md](./status-codes.md)

```json
{
  "fixture": {
    "id": 215662,
    "date": "2019-10-20T13:00:00+00:00",
    "timestamp": 1571576400,
    "referee": "M. Oliver",
    "timezone": "UTC",
    "periods": { "first": 1571576400, "second": 1571580000 },
    "venue":  { "id": 556, "name": "Old Trafford", "city": "Manchester" },
    "status": { "long": "Match Finished", "short": "FT", "elapsed": 90, "extra": null }
  },
  "league":   { "id": 39, "name": "Premier League", "country": "England", "season": 2019, "round": "Regular Season - 9" },
  "teams":    { "home": { "id": 33, "name": "Manchester United", "winner": null }, "away": { "id": 40, "name": "Liverpool", "winner": null } },
  "goals":    { "home": 1, "away": 1 },
  "score":    {
    "halftime":  { "home": 0, "away": 1 },
    "fulltime":  { "home": 1, "away": 1 },
    "extratime": { "home": null, "away": null },
    "penalty":   { "home": null, "away": null }
  },
  "events":   [ /* only on id / live queries */ ],
  "lineups":  [ /* only on id queries */ ],
  "statistics": [ /* only on id queries */ ],
  "players":  [ /* only on id queries */ ]
}
```

The `fixture.status.short` field is where we read the SHORT code
from [status-codes.md](./status-codes.md).

## Update frequency + recommended call rate (page 59)

- **Update frequency**: 15 seconds. Data may lag reality depending
  on the competition.
- **Recommended calls**: 1/min for leagues/teams/fixtures that have
  at least one fixture in progress; otherwise 1/day.

Our Monitor polls every 30s — half the doc's minimum recommended
cadence, giving us headroom under the update frequency without
being tight.

## Companion endpoints

Related to fixtures but separate:

| Endpoint                 | Purpose                                                        |
|--------------------------|----------------------------------------------------------------|
| `/fixtures/events`       | Events for one fixture (same shape as inline `events` array)   |
| `/fixtures/headtohead`   | H2H record between two teams                                   |
| `/fixtures/statistics`   | Team-level stats (shots, possession, cards)                    |
| `/fixtures/lineups`      | Formation + start XI + substitutes                             |
| `/fixtures/players`      | Per-player stats for a fixture                                 |
| `/fixtures/rounds`       | Round names for a league+season (e.g. `Regular Season - 1..38`) |

All of these are covered inline by `/fixtures?ids=` so we rarely
need the dedicated endpoints for our use case.
