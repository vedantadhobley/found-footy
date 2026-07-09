# /fixtures endpoint

**Status: STUB. Not yet seeded from docs.**

**Source URL**: <https://www.api-football.com/documentation-v3> →
Fixtures section.

## What we know from our own code

Our adapter at `internal/infra/apifootball/fixtures.go` hits:

- `GET /fixtures?from=YYYY-MM-DD&to=YYYY-MM-DD` — window fetch
- `GET /fixtures?ids=1234-5678-9012` — batched by-ID (**cap 20 IDs
  per call** — verified in Python + our own adapter enforces this)
- `GET /fixtures?live=all` — all currently-live matches (not
  currently called by our code but mock supports it)

## Envelope

Every response wraps the array in:

```json
{
  "get": "fixtures",
  "parameters": { "ids": "1234-5678" },
  "errors": [],
  "results": 2,
  "paging": { "current": 1, "total": 1 },
  "response": [ /* array of APIFixture */ ]
}
```

- **`errors`** field: array (or dict — TBD, Python treats it as
  dict-like with `.get()`). If populated, contains vendor-side
  warnings. Non-empty errors can coexist with HTTP 200 + a valid
  `response` array (soft errors — bad param but some data still
  returned). Our adapter currently doesn't inspect this field.

- **`results`** field: count of items in `response`. Sanity check.

- **`paging`** field: for paginated queries. Fixtures endpoints
  typically fit in one page.

## Per-fixture shape

See individual sub-docs for detail:
- Events per fixture — [events-shape.md](./events-shape.md)
- Status codes — [status-codes.md](./status-codes.md)

Top-level per-fixture:

```json
{
  "fixture": { "id", "date", "timestamp", "status": {...}, "venue": {...} },
  "league": { "id", "name", "country", "season", "round" },
  "teams": { "home": {...}, "away": {...} },
  "goals": { "home": 1, "away": 0 },
  "score": {
    "halftime":  {...},
    "fulltime":  {...},
    "extratime": {...},
    "penalty":   {...}
  },
  "events": [ /* only present when detailed=true or via /fixtures/{id} */ ]
}
```

## Open questions

1. Under what query does the `events` array appear? Our production
   test earlier tonight showed events on `/fixtures?live=all` — does
   `/fixtures?ids=` also include them? (Python's `fixtures_batch`
   assumes yes; our tests assume yes.)
2. What's the daily quota per plan tier? (Pro plan we're on shows
   7500/day in the `/status` probe; docs would formalize this.)
3. Rate limit headers we should scrape — see
   [rate-limits.md](./rate-limits.md).

## Doc sections to paste in when you have time

- Request parameters spec (`from`, `to`, `date`, `ids`, `live`,
  `league`, `season`, `team`, `venue`, `status`, `timezone`)
- Response envelope schema
- Per-plan rate limits + quotas
- The full APIFixture shape as documented
